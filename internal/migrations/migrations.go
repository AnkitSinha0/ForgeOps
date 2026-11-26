// Package migrations applies the versioned SQL files in sql/ to a database.
//
// Each file runs once, in name order, and is recorded in schema_migrations.
// All pending files run in ONE transaction: Postgres DDL is transactional, so a
// failing migration leaves the schema exactly as it was, never half-applied.
package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// The SQL is compiled into the binary, so a migration can't go missing from a
// container image or depend on the working directory.
//
//go:embed sql/*.sql
var files embed.FS

// migrationLockKey is the first half of the advisory lock key: an arbitrary constant
// that only ForgeOps migrations use ("FOMG" in ASCII).
const migrationLockKey = 0x464F4D47

// Migrate applies every migration that schema_migrations doesn't list yet.
func Migrate(ctx context.Context, db *gorm.DB, log *zap.Logger) error {
	names, err := fs.Glob(files, "sql/*.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(names) // 0001_, 0002_, ... the zero padding makes text order = numeric order

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Two controlplane replicas booting at once would both see "0001 not applied"
		// and both run it. The lock makes the second wait, then see it applied.
		//
		// It has to come before anything else, including creating schema_migrations:
		// concurrent CREATE TABLE IF NOT EXISTS is not safe in Postgres (both pass the
		// "if not exists" check, the loser fails on pg_type with 23505). A table lock
		// can't guard that, because the table may not exist yet, so this is an advisory
		// lock. The _xact_ variant is released at commit or rollback, so a crash can't
		// leave it held. It is keyed per schema, so tests in separate schemas don't wait
		// on each other.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?::int4, hashtext(current_schema()))", migrationLockKey).Error; err != nil {
			return fmt.Errorf("taking the migration lock: %w", err)
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text        PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`).Error; err != nil {
			return fmt.Errorf("creating schema_migrations: %w", err)
		}
		var applied []string
		if err := tx.Raw("SELECT version FROM schema_migrations").Scan(&applied).Error; err != nil {
			return fmt.Errorf("reading schema_migrations: %w", err)
		}
		done := make(map[string]bool, len(applied))
		for _, v := range applied {
			done[v] = true
		}

		for _, name := range names {
			version := strings.TrimSuffix(strings.TrimPrefix(name, "sql/"), ".sql")
			if done[version] {
				continue
			}
			body, err := files.ReadFile(name)
			if err != nil {
				return fmt.Errorf("reading %s: %w", name, err)
			}
			if err := tx.Exec(string(body)).Error; err != nil {
				return fmt.Errorf("applying %s: %w", version, err)
			}
			if err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version).Error; err != nil {
				return fmt.Errorf("recording %s: %w", version, err)
			}
			log.Info("applied migration", zap.String("version", version))
		}
		return nil
	})
}
