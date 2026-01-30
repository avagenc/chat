package config

import (
	"fmt"

	"github.com/ilyakaznacheev/cleanenv"
)

type Target struct {
	Nayo string `env:"AVAGENC_NAYO_SERVICE_URL" env-required:"true"`
}

func LoadTarget() (*Target, error) {
	cfg := Target{}

	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to load target config: %w", err)
	}

	return &cfg, nil
}
