// Command router is the ForgeOps edge: it maps <deployment>.<domain> to the
// right container and terminates TLS. Phase 0: boots, connects to Redis, serves
// a placeholder proxy listener plus health probes on a separate ops port.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/AnkitSinha0/forgeops/internal/config"
	"github.com/AnkitSinha0/forgeops/internal/database"
	"github.com/AnkitSinha0/forgeops/internal/health"
	"github.com/AnkitSinha0/forgeops/internal/httpserver"
	"github.com/AnkitSinha0/forgeops/pkg/logger"
	"golang.org/x/sync/errgroup"
)

const serviceName = "router"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	log, err := logger.New(cfg.App.Env, serviceName)
	if err != nil {
		return fmt.Errorf("building logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Redis only: the router is the internet-facing process, so it gets no
	// database credentials. Everything it needs (host -> backend) will live in
	// the Redis registry from Phase 5.
	rdb, err := database.NewRedis(ctx, cfg.Redis, log)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	hh := health.NewHandler(serviceName, log,
		health.Check{Name: "redis", Ping: database.PingRedis(rdb)},
	)

	// Placeholder until the registry-backed reverse proxy lands in Phase 5.
	noBackend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no deployment is bound to this host", http.StatusNotFound)
	})

	proxy := httpserver.New(cfg.Router.Addr, noBackend)
	ops := httpserver.New(cfg.Router.OpsAddr, hh.OpsMux())

	// If either listener fails, gctx is cancelled and the other shuts down too:
	// a router that serves traffic but cannot report health (or vice versa) is
	// worse than one that exits and gets restarted.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpserver.Serve(gctx, "proxy", proxy, cfg.App.ShutdownTimeout, log) })
	g.Go(func() error { return httpserver.Serve(gctx, "ops", ops, cfg.App.ShutdownTimeout, log) })
	return g.Wait()
}
