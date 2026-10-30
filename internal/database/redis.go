package database

import (
	"context"
	"fmt"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/config"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// NewRedis creates a client and pings it. go-redis connects lazily, so without
// the ping a wrong address would only surface on the first real command.
func NewRedis(ctx context.Context, cfg config.RedisConfig, log *zap.Logger) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("pinging redis at %s: %w", cfg.Addr, err)
	}

	log.Info("connected to redis", zap.String("addr", cfg.Addr))
	return client, nil
}

// PingRedis is the readiness check for Redis.
func PingRedis(client *redis.Client) func(context.Context) error {
	return func(ctx context.Context) error {
		return client.Ping(ctx).Err()
	}
}
