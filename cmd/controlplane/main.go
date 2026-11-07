// Command controlplane is the ForgeOps API: tenants, API keys, projects, env vars and
// deployments so far, with webhooks and log streams to come. Run with no arguments it
// serves the API. `controlplane admin ...` runs one-off server tasks (see admin.go).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AnkitSinha0/forgeops/internal/config"
	"github.com/AnkitSinha0/forgeops/internal/database"
	"github.com/AnkitSinha0/forgeops/internal/handlers"
	"github.com/AnkitSinha0/forgeops/internal/health"
	"github.com/AnkitSinha0/forgeops/internal/httpserver"
	"github.com/AnkitSinha0/forgeops/internal/migrations"
	"github.com/AnkitSinha0/forgeops/internal/repositories"
	"github.com/AnkitSinha0/forgeops/internal/secrets"
	"github.com/AnkitSinha0/forgeops/internal/services"
	"github.com/AnkitSinha0/forgeops/pkg/logger"
)

const serviceName = "controlplane"

// main only translates run's error into an exit code. Keeping the real work in
// run means every defer (closing pools, flushing logs) executes on failure —
// log.Fatal / os.Exit inside the work would skip them.
func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		return run()
	}
	if args[0] == "admin" {
		return runAdmin(args[1:])
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], adminUsage)
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

	// Checked before connecting to anything: a missing key must stop the boot, not
	// surface as a 500 the first time someone sets an env var.
	keys, err := secrets.ParseKeyring(cfg.Secrets.Keys)
	if err != nil {
		return err
	}

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

	projects := repositories.NewProjectRepository(db)
	r := handlers.NewRouter(handlers.Deps{
		Log: log,
		Health: health.NewHandler(serviceName, log,
			health.Check{Name: "postgres", Ping: database.PingPostgres(db)},
			health.Check{Name: "redis", Ping: database.PingRedis(rdb)},
		),
		Tenants:     services.NewTenantService(repositories.NewTenantRepository(db)),
		APIKeys:     services.NewAPIKeyService(repositories.NewAPIKeyRepository(db)),
		Projects:    services.NewProjectService(projects),
		EnvVars:     services.NewEnvVarService(projects, repositories.NewEnvVarRepository(db), keys),
		Deployments: services.NewDeploymentService(projects, repositories.NewDeploymentRepository(db)),
	})

	srv := httpserver.New(cfg.ControlPlane.Addr, r)
	return httpserver.Serve(ctx, "api", srv, cfg.App.ShutdownTimeout, log)
}
