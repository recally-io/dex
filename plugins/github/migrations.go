package github

import "embed"

// Migrations is the schema of the plugin. dex applies it with plugin.Migrate.
//
//go:embed migrations/*.sql
var Migrations embed.FS
