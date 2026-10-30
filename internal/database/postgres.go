package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/config"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewPostgres opens a pooled connection and proves it works with a ping, so a
// bad DSN fails at boot instead of on the first request. The caller owns the
// pool and must close it (see ClosePostgres).
func NewPostgres(ctx context.Context, cfg config.DatabaseConfig, env string, log *zap.Logger) (*gorm.DB, error) {
	if cfg.DSN == "" {
		return nil, errors.New("DATABASE_DSN is required")
	}

	level := gormlogger.Warn
	if env == "development" {
		level = gormlogger.Info
	}

	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(level),
		// gorm.Open would otherwise ping with no deadline; an unreachable host
		// could hang boot for minutes. We ping ourselves below, with a timeout.
		DisableAutomaticPing: true,
	})
	if err != nil {
		return nil, fmt.Errorf("opening postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("getting underlying sql.DB: %w", err)
	}

	// database/sql defaults to unlimited open connections — under load that can
	// exceed Postgres' max_connections (100 by default) across three services.
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	// Recycle connections so a Postgres restart or failover is picked up.
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}

	log.Info("connected to postgres",
		zap.Int("max_open_conns", cfg.MaxOpenConns),
		zap.Int("max_idle_conns", cfg.MaxIdleConns),
	)
	return db, nil
}

// PingPostgres is the readiness check for Postgres.
func PingPostgres(db *gorm.DB) func(context.Context) error {
	return func(ctx context.Context) error {
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
}

// ClosePostgres releases every pooled connection.
func ClosePostgres(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
