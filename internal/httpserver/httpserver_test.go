package httpserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Security audit 2026-11-11, finding L9: when graceful shutdown ran out of time, Serve
// returned but left in-flight connections open, so a slow (or hostile) client kept
// being served after "shutdown". Red against f85ea24: the client got its full answer
// about 3 s later.
func TestShutdownTimeoutClosesInFlightConnections(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	started := make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(3 * time.Second) // ignores its context, like a stuck handler
		_, _ = io.WriteString(w, "done")
	})
	srv := New(addr, slow)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, "test", srv, 200*time.Millisecond, zap.NewNop()) }()

	var resp *http.Response
	var reqErr error
	got := make(chan struct{})
	go func() {
		defer close(got)
		for range 50 { // wait for the listener
			resp, reqErr = http.Get("http://" + addr + "/")
			if reqErr == nil || !isRefused(reqErr) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the handler")
	}
	cancel()
	if err := <-served; err == nil {
		t.Fatal("Serve reported a clean stop although a request was still running")
	}

	select {
	case <-got:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the in-flight connection was still open after the shutdown timeout")
	}
	if reqErr == nil {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("the client was still answered after shutdown: %q", body)
	}
}

func isRefused(err error) bool {
	var op *net.OpError
	for e := err; e != nil; {
		if o, ok := e.(*net.OpError); ok {
			op = o
			break
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return op != nil && op.Op == "dial"
}
