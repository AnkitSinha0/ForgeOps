package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// A private type, so no other package can set or collide with these context values.
type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxTenantID
)

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// tenantFrom returns the authenticated tenant. There is deliberately no fallback: if a
// tenant route is ever registered outside the auth group, this panics, the request
// gets a 500, and no query ever runs with an empty tenant.
func tenantFrom(ctx context.Context) uuid.UUID {
	id, ok := ctx.Value(ctxTenantID).(uuid.UUID)
	if !ok {
		panic("handlers: tenant route reached without authentication")
	}
	return id
}

// requestID gives every request an ID that ties its response to its log lines. Any
// X-Request-Id the client sends is ignored, because it could otherwise forge log entries.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// accessLog records one line per request. It logs the route pattern rather than the raw
// path and never touches headers or bodies, so credentials and values stay out of the
// logs. It sits outside recoverPanics so it also sees the 500 a panic turns into.
func (h *handler) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		h.log.Info("request",
			zap.String("request_id", requestIDFrom(r.Context())),
			zap.String("method", r.Method),
			zap.String("route", route),
			zap.Int("status", ww.Status()),
			zap.Duration("duration", time.Since(start)),
		)
	})
}

// recoverPanics turns a panic into the normal JSON 500. The stack goes to the log, and
// the caller learns nothing about it.
func (h *handler) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec) // net/http's own signal to drop the connection
			}
			h.log.Error("panic in handler",
				zap.String("request_id", requestIDFrom(r.Context())),
				zap.String("panic", fmt.Sprint(rec)),
				zap.Stack("stack"),
			)
			// If the response has already started, its status can't be changed.
			if ww, ok := w.(middleware.WrapResponseWriter); !ok || ww.Status() == 0 {
				writeProblem(w, r, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// apiHeaders stops browsers and proxies from sniffing or caching API responses, which
// can include a freshly created API key.
func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// requestTimeout bounds each request's database work. Every query uses this context, so
// a stuck query is cancelled here. It is shorter than the server's 15 s WriteTimeout,
// so the client gets a real error rather than a dropped connection.
func requestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticate resolves the bearer key to a tenant and puts it in the context. The
// tenant comes only from here, never from the URL or the body, so a caller has no way
// to name a different tenant.
//
// Missing, malformed, unknown and revoked keys all get the same 401. If the check
// itself fails (the database is down), the answer is not a 401: that would tell a
// valid client its key is bad when it isn't.
func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		tenantID, err := h.apiKeys.Authenticate(r.Context(), token)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxTenantID, tenantID)))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
