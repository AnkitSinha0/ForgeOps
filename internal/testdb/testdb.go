// Package testdb gives integration tests a real, private Postgres database.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/database"
	"github.com/AnkitSinha0/forgeops/internal/migrations"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// New returns a connection to a fresh schema (a private namespace inside the
// database) with the real migrations applied, and drops it when the test ends.
// Tests share no rows, so they can run in parallel and in any order. The test is
// skipped if TEST_DATABASE_DSN isn't set.
func New(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set (make up && make test-db)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	schema := "test_" + hex.EncodeToString(buf)
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("creating schema: %v", err)
	}

	// search_path in the DSN makes every pooled connection resolve unqualified
	// table names to the test schema.
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing DSN: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: gormlogger.Discard, NowFunc: database.Now})
	if err != nil {
		t.Fatalf("connecting to schema: %v", err)
	}
	if err := migrations.Migrate(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
