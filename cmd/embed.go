package cmd

import "embed"

//go:embed migrate/migrations/*.sql
var EmbedMigrations embed.FS
