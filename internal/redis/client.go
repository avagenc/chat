package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/avagenc/api-gateway/internal/config"
	"github.com/redis/go-redis/v9"
)

func NewClient(cfg *config.Redis) (*redis.Client, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse redis config: %w", err)
	}

	opts.PoolSize = cfg.PoolSize
	opts.MinIdleConns = cfg.MinIdle

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("unable to connect to redis: %w", err)
	}

	return client, nil
}
