// Package httpserver holds the one way ForgeOps runs an HTTP listener:
// hardened timeouts plus graceful shutdown tied to a context.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// New returns a server with explicit timeouts. The zero-value http.Server has
// none, so one slow or malicious client can hold a connection open forever.
func New(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second, // slowloris: headers trickled one byte at a time
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// Serve blocks until ctx is cancelled or the listener fails. On cancellation it
// stops accepting connections and gives in-flight requests up to
// shutdownTimeout to finish. A non-nil error means the server did not stop
// cleanly (e.g. the port was already taken).
func Serve(ctx context.Context, name string, srv *http.Server, shutdownTimeout time.Duration, log *zap.Logger) error {
	log = log.With(zap.String("listener", name), zap.String("addr", srv.Addr))

	// Bind synchronously (instead of ListenAndServe in the goroutine) so a taken
	// port is reported here, and "listening" is only logged once it is true.
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("%s listener: %w", name, err)
	}
	log.Info("listening")

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		// Serve only returns before Shutdown on a real failure.
		return fmt.Errorf("%s listener on %s: %w", name, srv.Addr, err)
	case <-ctx.Done():
	}

	// ctx is already cancelled, so the drain deadline needs a fresh context.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("%s listener shutdown: %w", name, err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s listener: %w", name, err)
	}

	log.Info("stopped cleanly")
	return nil
}
