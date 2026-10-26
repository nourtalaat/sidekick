// Package migrations holds the goose migrations for the Sidekick database,
// embedded in the binary. Add a new one with `make migrate-new name=...`.
package migrations

import "embed"

// FS holds every migration file. The file names start with a sequential
// version number, for example 00002_add_jobs.sql.
//
//go:embed *.sql
var FS embed.FS
