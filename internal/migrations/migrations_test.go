package migrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// emptySchema returns a connection to a brand-new schema with nothing in it, not even
// schema_migrations: the state of a fresh production database. (testdb.New can't be
// used here: it imports this package and has already migrated.)
func emptySchema(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set (make up && make test-db)")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	schema := "test_" + hex.EncodeToString(buf)
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("creating schema: %v", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing DSN: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("connecting to schema: %v", err)
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

// Several controlplane replicas (or `admin create-tenant` next to a running server)
// can boot against a fresh database at the same moment. Every one of them must come
// up, and every migration must be applied exactly once.
//
// Security audit 2026-11-11, finding M1: this was red before the fix. 5 of 6
// concurrent boots failed with SQLSTATE 23505 on pg_type_typname_nsp_index, because
// CREATE TABLE IF NOT EXISTS schema_migrations ran before, and outside, the lock.
func TestConcurrentFirstBootAllSucceed(t *testing.T) {
	t.Parallel()
	const replicas, rounds = 6, 3
	names, err := migrationVersions()
	if err != nil {
		t.Fatal(err)
	}
	for round := range rounds {
		db := emptySchema(t)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		var wg sync.WaitGroup
		errs := make([]error, replicas)
		start := make(chan struct{})
		for i := range replicas {
			wg.Go(func() {
				<-start
				errs[i] = Migrate(ctx, db, zap.NewNop())
			})
		}
		close(start)
		wg.Wait()
		cancel()
		for i, err := range errs {
			if err != nil {
				t.Errorf("round %d replica %d: %v", round, i, err)
			}
		}
		var applied []string
		if err := db.Raw("SELECT version FROM schema_migrations ORDER BY version").Scan(&applied).Error; err != nil {
			t.Fatal(err)
		}
		if len(applied) != len(names) {
			t.Fatalf("round %d: schema_migrations = %v, want each of %v once", round, applied, names)
		}
	}
}

func migrationVersions() ([]string, error) {
	return fs.Glob(files, "sql/*.sql")
}
