// Package migrations embeds per-service SQL migrations.
package migrations

import "embed"

//go:embed id/*.sql market/*.sql pay/*.sql sale/*.sql
var FS embed.FS
