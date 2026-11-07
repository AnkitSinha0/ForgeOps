package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/AnkitSinha0/forgeops/internal/services"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// maxBodyBytes bounds every JSON body. The largest legitimate one is an env var value
// of 64 KiB, which JSON escaping can inflate to several times that size.
const maxBodyBytes = 1 << 20 // 1 MiB

// errorBody is the one shape every error response has.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Field     string `json:"field,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// listBody is the one shape every list response has. Items is never null, and
// NextCursor is null on the last page.
type listBody[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// writeJSON encodes to a buffer first, so an encoding failure can still become a 500
// rather than a 200 with half a body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		http.Error(w, `{"error":{"code":"internal","message":"internal error"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorBody{errorDetail{Code: code, Message: message, RequestID: requestIDFrom(r.Context())}})
}

// requestError is a problem with the request itself, found before any service ran.
type requestError struct {
	status  int
	code    string
	message string
}

func (e *requestError) Error() string { return e.message }

// fail is the one place errors become HTTP responses. Expected errors get a precise
// answer. Everything else is logged and the caller gets a generic 500, because raw
// database errors can describe the schema and the data in it.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var reqErr *requestError
	var valErr *services.ValidationError
	switch {
	case errors.As(err, &reqErr):
		writeProblem(w, r, reqErr.status, reqErr.code, reqErr.message)
	case errors.As(err, &valErr):
		writeJSON(w, http.StatusBadRequest, errorBody{errorDetail{
			Code: "invalid_request", Message: valErr.Error(), Field: valErr.Field, RequestID: requestIDFrom(r.Context()),
		}})
	case errors.Is(err, services.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, services.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", "conflicts with an existing resource")
	case errors.Is(err, services.ErrInvalid):
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "a value is invalid")
	case errors.Is(err, services.ErrUnauthenticated):
		writeUnauthorized(w, r)
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		// The client went away, so there is nobody to answer and nothing went wrong here.
		writeProblem(w, r, http.StatusServiceUnavailable, "unavailable", "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		h.log.Warn("request timed out", zap.String("request_id", requestIDFrom(r.Context())), zap.Error(err))
		writeProblem(w, r, http.StatusServiceUnavailable, "timeout", "the request took too long")
	default:
		h.log.Error("request failed", zap.String("request_id", requestIDFrom(r.Context())), zap.Error(err))
		writeProblem(w, r, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "a valid API key is required")
}

// decodeJSON reads exactly one JSON object into dst. It refuses unknown fields, so a
// request carrying tenant_id gets a 400 instead of having it silently ignored. It also
// refuses oversized bodies and anything after the object.
//
// Messages don't quote the body: it may hold a secret, and json errors can echo input.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &requestError{http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json"}
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &requestError{http.StatusRequestEntityTooLarge, "body_too_large", "the request body is too large"}
		}
		if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return &requestError{http.StatusBadRequest, "invalid_request", "unknown field " + field}
		}
		return &requestError{http.StatusBadRequest, "invalid_request", "the request body is not valid JSON for this endpoint"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &requestError{http.StatusRequestEntityTooLarge, "body_too_large", "the request body is too large"}
		}
		return &requestError{http.StatusBadRequest, "invalid_request", "the request body must hold a single JSON object"}
	}
	return nil
}

const defaultLimit = 50

// pageParams reads ?limit and ?cursor. A present but malformed value is an error rather
// than a silent default, and the services check the range. It parses the query itself
// because r.URL.Query() silently drops pairs it can't parse: a corrupted cursor would
// send a client back to page 1 forever instead of telling it something is wrong.
func pageParams(r *http.Request) (limit int, cursor string, err error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, "", &requestError{http.StatusBadRequest, "invalid_request", "the query string is malformed"}
	}
	if len(q["limit"]) > 1 || len(q["cursor"]) > 1 {
		return 0, "", &requestError{http.StatusBadRequest, "invalid_request", "limit and cursor may each appear once"}
	}
	limit = defaultLimit
	if text := q.Get("limit"); text != "" {
		limit, err = strconv.Atoi(text)
		if err != nil {
			return 0, "", &requestError{http.StatusBadRequest, "invalid_request", "limit must be a number"}
		}
	}
	return limit, q.Get("cursor"), nil
}

// envKeyParam reads the {key} path segment. chi hands back the segment as it was sent
// when the path contains escapes, so A%5FB has to be decoded to mean A_B. If it can't
// be decoded it stays as sent, and the service's key pattern refuses the "%".
func envKeyParam(r *http.Request) string {
	raw := chi.URLParam(r, "key")
	key, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return key
}

// parseID reads a UUID from the path. A malformed ID can't name any row, so it gets
// the same 404 as an ID that doesn't exist.
func parseID(text string) (uuid.UUID, error) {
	id, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil, services.ErrNotFound
	}
	return id, nil
}

func listOf[M, V any](p services.Page[M], view func(M) V) listBody[V] {
	body := listBody[V]{Items: make([]V, 0, len(p.Items))}
	for _, m := range p.Items {
		body.Items = append(body.Items, view(m))
	}
	if p.NextCursor != "" {
		body.NextCursor = &p.NextCursor
	}
	return body
}
