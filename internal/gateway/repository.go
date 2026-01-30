package gateway

import (
	"context"

	"github.com/redis/go-redis/v9"
)

type redisRepository struct {
	client *redis.Client
}

func NewRepository(client *redis.Client) Repository {
	return &redisRepository{client: client}
}

func (r *redisRepository) IsUserBlocked(ctx context.Context, userID string) (bool, error) {
	val, err := r.client.SIsMember(ctx, "users:blocked:payment", userID).Result()
	if err != nil {
		return false, err
	}
	return val, nil
}
