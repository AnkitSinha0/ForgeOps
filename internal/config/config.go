// Package config loads settings for all three ForgeOps binaries.
//
// Precedence (highest wins): real environment variables > .env file > defaults below.
// Every key maps to an env var by upper-casing it and replacing "." with "_",
// e.g. "database.max_open_conns" <- DATABASE_MAX_OPEN_CONNS.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	App          AppConfig
	ControlPlane ControlPlaneConfig
	Executor     ExecutorConfig
	Router       RouterConfig
	Database     DatabaseConfig
	Redis        RedisConfig
	Secrets      SecretsConfig
}

type SecretsConfig struct {
	// Keys encrypts env vars at rest: "version:base64key" pairs (see secrets.ParseKeyring).
	// Secret, never defaulted. Only the controlplane reads it, and it refuses to boot
	// without one rather than failing on the first request.
	Keys string
}

type AppConfig struct {
	Env             string        // "development" or "production"
	ShutdownTimeout time.Duration // how long in-flight requests get to finish on SIGTERM
}

type ControlPlaneConfig struct {
	Addr string // API listener; also serves /healthz and /readyz
}

type ExecutorConfig struct {
	OpsAddr string // the executor has no public API, only health (and /metrics in Phase 10)
}

type RouterConfig struct {
	Addr    string // public proxy listener — every path here belongs to tenant apps
	OpsAddr string // health lives on a separate port so it never shadows a tenant's /healthz
}

type DatabaseConfig struct {
	DSN             string // secret: comes from DATABASE_DSN, never defaulted
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// Load reads configuration. It does not validate connection strings — each
// connector (database.NewPostgres, ...) rejects the empty values it needs,
// because the router legitimately runs without Postgres.
func Load() (*Config, error) {
	// Missing .env is fine (containers inject real env vars instead).
	// godotenv never overwrites a variable that is already set.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading .env: %w", err)
	}

	// A private instance instead of the viper global: three binaries share this
	// package, and tests can call Load() repeatedly without leaking state.
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("app.env", "development")
	v.SetDefault("app.shutdown_timeout", "15s")
	v.SetDefault("controlplane.addr", ":8080")
	v.SetDefault("executor.ops_addr", ":8081")
	v.SetDefault("router.addr", ":8000")
	v.SetDefault("router.ops_addr", ":8082")
	v.SetDefault("database.max_open_conns", 25)
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.conn_max_lifetime", "5m")
	v.SetDefault("redis.addr", "localhost:6380")
	v.SetDefault("redis.db", 0)

	cfg := &Config{
		App: AppConfig{
			Env:             v.GetString("app.env"),
			ShutdownTimeout: v.GetDuration("app.shutdown_timeout"),
		},
		ControlPlane: ControlPlaneConfig{
			Addr: v.GetString("controlplane.addr"),
		},
		Executor: ExecutorConfig{
			OpsAddr: v.GetString("executor.ops_addr"),
		},
		Router: RouterConfig{
			Addr:    v.GetString("router.addr"),
			OpsAddr: v.GetString("router.ops_addr"),
		},
		Database: DatabaseConfig{
			DSN:             v.GetString("database.dsn"),
			MaxOpenConns:    v.GetInt("database.max_open_conns"),
			MaxIdleConns:    v.GetInt("database.max_idle_conns"),
			ConnMaxLifetime: v.GetDuration("database.conn_max_lifetime"),
		},
		Redis: RedisConfig{
			Addr:     v.GetString("redis.addr"),
			Password: v.GetString("redis.password"),
			DB:       v.GetInt("redis.db"),
		},
		Secrets: SecretsConfig{
			Keys: v.GetString("secrets.keys"),
		},
	}

	if cfg.App.Env != "development" && cfg.App.Env != "production" {
		return nil, fmt.Errorf("APP_ENV must be development or production, got %q", cfg.App.Env)
	}
	if cfg.App.ShutdownTimeout <= 0 {
		return nil, fmt.Errorf("APP_SHUTDOWN_TIMEOUT must be positive, got %s", cfg.App.ShutdownTimeout)
	}
	if err := cfg.Database.check(); err != nil {
		return nil, err
	}
	if cfg.IsProduction() {
		if err := cfg.checkProduction(); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// check rejects pool settings database/sql would accept but misread: 0 open
// connections means "unlimited", not "none", and a negative lifetime means "forever".
func (d DatabaseConfig) check() error {
	if d.MaxOpenConns < 1 {
		return fmt.Errorf("DATABASE_MAX_OPEN_CONNS must be at least 1, got %d", d.MaxOpenConns)
	}
	if d.MaxIdleConns < 0 || d.MaxIdleConns > d.MaxOpenConns {
		return fmt.Errorf("DATABASE_MAX_IDLE_CONNS must be between 0 and DATABASE_MAX_OPEN_CONNS (%d), got %d", d.MaxOpenConns, d.MaxIdleConns)
	}
	if d.ConnMaxLifetime < 0 {
		return fmt.Errorf("DATABASE_CONN_MAX_LIFETIME must not be negative, got %s", d.ConnMaxLifetime)
	}
	return nil
}

// checkProduction refuses the two local-dev shortcuts that must never reach a real
// deployment: Postgres without TLS, and Redis without a password. (Errors name the
// setting, never its value: the DSN holds a password.)
func (c *Config) checkProduction() error {
	if strings.Contains(strings.ToLower(c.Database.DSN), "sslmode=disable") {
		return errors.New("APP_ENV=production: DATABASE_DSN must not use sslmode=disable; use sslmode=verify-full")
	}
	if c.Redis.Password == "" {
		return errors.New("APP_ENV=production: REDIS_PASSWORD is required")
	}
	return nil
}

func (c *Config) IsProduction() bool {
	return c.App.Env == "production"
}
