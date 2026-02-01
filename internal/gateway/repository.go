package gateway

import (
	"context"

	"github.com/redis/go-redis/v9"
)

type RedisRepository struct {
	client *redis.Client
}

func NewRepository(client *redis.Client) *RedisRepository {
	return &RedisRepository{client: client}
}

func (r *RedisRepository) IsUserBlocked(ctx context.Context, userID string) (bool, error) {
	val, err := r.client.SIsMember(ctx, "users:blocked:payment", userID).Result()
	if err != nil {
		return false, err
	}
	return val, nil
}
