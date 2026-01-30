package config

import (
	"fmt"

	"github.com/ilyakaznacheev/cleanenv"
)

type Security struct {
	APIKey                  string `env:"AVAGENC_API_KEY" env-required:"true"`
	IdentitySupabaseJWKSURL string `env:"AVAGENC_IDENTITY_SUPABASE_JWKS_URL" env-required:"true"`
}

func LoadSecurity() (*Security, error) {
	cfg := Security{}

	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to load security config: %w", err)
	}

	return &cfg, nil
}
