// Command controlplane is the ForgeOps API: projects, deployments, webhooks,
// env vars and log streams. Phase 0: boots, connects to Postgres + Redis,
// serves health probes.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AnkitSinha0/forgeops/internal/config"
	"github.com/AnkitSinha0/forgeops/internal/database"
	"github.com/AnkitSinha0/forgeops/internal/health"
	"github.com/AnkitSinha0/forgeops/internal/httpserver"
	"github.com/AnkitSinha0/forgeops/internal/migrations"
	"github.com/AnkitSinha0/forgeops/pkg/logger"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const serviceName = "controlplane"

// main only translates run's error into an exit code. Keeping the real work in
// run means every defer (closing pools, flushing logs) executes on failure —
// log.Fatal / os.Exit inside the work would skip them.
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

	// Cancelled on Ctrl+C (SIGINT) or on `docker stop` / Kubernetes (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.NewPostgres(ctx, cfg.Database, cfg.App.Env, log)
	if err != nil {
		return err
	}
	defer func() { _ = database.ClosePostgres(db) }()

	// Only the controlplane migrates: the schema has one owner, and the executor
	// never boots against a half-migrated database it would have to wait for.
	if err := migrations.Migrate(ctx, db, log); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}

	rdb, err := database.NewRedis(ctx, cfg.Redis, log)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	hh := health.NewHandler(serviceName, log,
		health.Check{Name: "postgres", Ping: database.PingPostgres(db)},
		health.Check{Name: "redis", Ping: database.PingRedis(rdb)},
	)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer) // a panicking handler returns 500 instead of dropping the connection
	r.Get("/healthz", hh.Live)
	r.Get("/readyz", hh.Ready)

	srv := httpserver.New(cfg.ControlPlane.Addr, r)
	return httpserver.Serve(ctx, "api", srv, cfg.App.ShutdownTimeout, log)
}
