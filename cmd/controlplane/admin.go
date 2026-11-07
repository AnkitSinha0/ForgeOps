package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AnkitSinha0/forgeops/internal/config"
	"github.com/AnkitSinha0/forgeops/internal/database"
	"github.com/AnkitSinha0/forgeops/internal/migrations"
	"github.com/AnkitSinha0/forgeops/internal/repositories"
	"github.com/AnkitSinha0/forgeops/internal/services"
	"github.com/AnkitSinha0/forgeops/pkg/logger"
	"gorm.io/gorm"
)

const adminUsage = `usage:
  controlplane admin create-tenant --name "Acme Inc" --slug acme
  controlplane admin create-key --tenant acme --name laptop`

// runAdmin handles tasks that need server access rather than an API credential. A new
// install has no tenants, so no API call could authenticate to create the first one.
// Doing it here adds no public route and no shared admin password. It also recovers a
// tenant that has lost all its keys.
//
// The new key goes to stdout and everything else to stderr, so a script can capture it:
//
//	KEY=$(controlplane admin create-tenant --name "Acme Inc" --slug acme)
func runAdmin(args []string) error {
	if len(args) == 0 {
		return errors.New(adminUsage)
	}

	switch args[0] {
	case "create-tenant":
		fs := flag.NewFlagSet("create-tenant", flag.ContinueOnError)
		name := fs.String("name", "", "display name")
		slug := fs.String("slug", "", "URL-safe identifier, unique across all tenants")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return withAdminDB(func(ctx context.Context, db *gorm.DB) error {
			t, key, err := services.NewTenantService(repositories.NewTenantRepository(db)).Create(ctx, *name, *slug)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "created tenant %s (%s). Store this API key now, it can't be shown again:\n", t.Slug, t.ID)
			fmt.Println(key)
			return nil
		})

	case "create-key":
		fs := flag.NewFlagSet("create-key", flag.ContinueOnError)
		tenant := fs.String("tenant", "", "tenant slug")
		name := fs.String("name", "admin", "a label for the key")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return withAdminDB(func(ctx context.Context, db *gorm.DB) error {
			t, err := services.NewTenantService(repositories.NewTenantRepository(db)).FindBySlug(ctx, *tenant)
			if err != nil {
				return fmt.Errorf("tenant %q: %w", *tenant, err)
			}
			_, key, err := services.NewAPIKeyService(repositories.NewAPIKeyRepository(db)).Create(ctx, t.ID, *name)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "created a key for tenant %s. Store it now, it can't be shown again:\n", t.Slug)
			fmt.Println(key)
			return nil
		})
	}
	return fmt.Errorf("unknown admin command %q\n%s", args[0], adminUsage)
}

// withAdminDB connects and migrates, because a CLI command can be the very first thing
// run against a new database. It needs neither Redis nor the encryption key.
func withAdminDB(fn func(context.Context, *gorm.DB) error) error {
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

	db, err := database.NewPostgres(ctx, cfg.Database, cfg.App.Env, log)
	if err != nil {
		return err
	}
	defer func() { _ = database.ClosePostgres(db) }()

	if err := migrations.Migrate(ctx, db, log); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}
	return fn(ctx, db)
}
