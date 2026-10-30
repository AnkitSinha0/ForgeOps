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

// Migrate applies every migration that schema_migrations doesn't list yet.
func Migrate(ctx context.Context, db *gorm.DB, log *zap.Logger) error {
	names, err := fs.Glob(files, "sql/*.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(names) // 0001_, 0002_, ... the zero padding makes text order = numeric order

	db = db.WithContext(ctx)
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text        PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`).Error; err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Two controlplane replicas booting at once would both see "0001 not applied"
		// and both run it. The lock makes the second wait, then see it applied.
		if err := tx.Exec("LOCK TABLE schema_migrations IN EXCLUSIVE MODE").Error; err != nil {
			return fmt.Errorf("locking schema_migrations: %w", err)
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
