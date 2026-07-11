package postgres

import "embed"

// MigrationsFS embeds the migrations SQL files for use in DB runner.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
