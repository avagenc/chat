package config

import (
	"fmt"

	"github.com/ilyakaznacheev/cleanenv"
)

type Redis struct {
	URL      string `env:"REDIS_URL" env-required:"true"`
	PoolSize int    `env:"REDIS_POOL_SIZE" env-default:"20"`
	MinIdle  int    `env:"REDIS_MIN_IDLE" env-default:"5"`
}

func LoadRedis() (*Redis, error) {
	cfg := Redis{}

	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to load database config: %w", err)
	}

	return &cfg, nil
}
