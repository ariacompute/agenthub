// Package migrations embeds the idempotent PostgreSQL schema migrations.
//
// The files are embedded at build time so the released binary is self
// contained: internal/store applies them in filename order at startup and
// records every applied file in schema_migrations.
package migrations

import "embed"

// FS holds every *.sql migration of this directory.
//
//go:embed *.sql
var FS embed.FS
