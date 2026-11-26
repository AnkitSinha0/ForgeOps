package config

import (
	"strings"
	"testing"
)

// Security audit 2026-11-11, finding L10. Red against f85ea24: every case loaded.
// (t.Setenv rules out t.Parallel here.)

func TestPoolSettingsAreRangeChecked(t *testing.T) {
	for _, c := range []struct{ name, key, value string }{
		{"zero open conns means unlimited in database/sql", "DATABASE_MAX_OPEN_CONNS", "0"},
		{"negative open conns", "DATABASE_MAX_OPEN_CONNS", "-1"},
		{"more idle than open", "DATABASE_MAX_IDLE_CONNS", "100"},
		{"negative idle", "DATABASE_MAX_IDLE_CONNS", "-1"},
		{"negative lifetime", "DATABASE_CONN_MAX_LIFETIME", "-5m"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(c.key, c.value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s loaded", c.key, c.value)
			}
		})
	}
	if _, err := Load(); err != nil {
		t.Fatalf("defaults refused: %v", err)
	}
}

// The dev defaults (no TLS to Postgres, no Redis password) must not reach production
// by accident.
func TestProductionRefusesDevDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("REDIS_PASSWORD", "s3cret")
	t.Setenv("DATABASE_DSN", "postgres://u:p@db:5432/forgeops?sslmode=disable")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "sslmode") {
		t.Fatalf("sslmode=disable in production: err = %v", err)
	}

	t.Setenv("DATABASE_DSN", "postgres://u:p@db:5432/forgeops?sslmode=verify-full")
	t.Setenv("REDIS_PASSWORD", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REDIS_PASSWORD") {
		t.Fatalf("empty REDIS_PASSWORD in production: err = %v", err)
	}

	t.Setenv("REDIS_PASSWORD", "s3cret")
	if _, err := Load(); err != nil {
		t.Fatalf("a proper production config was refused: %v", err)
	}
}
