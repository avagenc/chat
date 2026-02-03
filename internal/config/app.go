package config

import (
	"fmt"

	"github.com/ilyakaznacheev/cleanenv"
)

type App struct {
	Name    string
	Version string
	Env     string `env:"APP_ENV" env-required:"true"`
}

func LoadApp() (*App, error) {
	cfg := App{
		Name:    "api-gateway",
		Version: "v0.0.1",
	}

	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to load app config: %w", err)
	}

	return &cfg, nil
}
