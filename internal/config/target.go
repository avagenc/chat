package config

import (
	"fmt"

	"github.com/ilyakaznacheev/cleanenv"
)

type Target struct {
	Nayu string `env:"NAYU_API_URL" env-required:"true"`
	Zee  string `env:"ZEE_API_URL" env-required:"true"`
}

func LoadTarget() (*Target, error) {
	cfg := Target{}

	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to load target config: %w", err)
	}

	return &cfg, nil
}
