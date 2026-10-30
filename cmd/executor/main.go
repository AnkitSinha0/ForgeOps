// Command executor is the ForgeOps build worker: it claims builds from the
// Redis queue, clones the repo and builds it inside a sandbox. Phase 0: boots,
// connects to Postgres + Redis, serves health probes on its ops port.
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
	"github.com/AnkitSinha0/forgeops/pkg/logger"
)

const serviceName = "executor"

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

	// Postgres: build status/records. Redis: the build queue (Phase 4).
	db, err := database.NewPostgres(ctx, cfg.Database, cfg.App.Env, log)
	if err != nil {
		return err
	}
	defer func() { _ = database.ClosePostgres(db) }()

	rdb, err := database.NewRedis(ctx, cfg.Redis, log)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	hh := health.NewHandler(serviceName, log,
		health.Check{Name: "postgres", Ping: database.PingPostgres(db)},
		health.Check{Name: "redis", Ping: database.PingRedis(rdb)},
	)

	log.Info("executor idle: the build-queue consumer arrives in Phase 4")

	srv := httpserver.New(cfg.Executor.OpsAddr, hh.OpsMux())
	return httpserver.Serve(ctx, "ops", srv, cfg.App.ShutdownTimeout, log)
}
