// Package vector holds the goose migrations of vector memory, embedded in
// the binary. They are a second set, with their own version table
// (goose_db_version_vector), and run only when memory.vector.enabled is
// true, because they need the pgvector extension. A Postgres without
// pgvector never runs them, so it never needs the extension.
//
// The files are numbered from 00001, apart from the main set in the
// package above. The length of the vectors is set per install, so the
// migrations leave the embedding column without a length; store.MigrateVector
// gives it the configured length and builds the index after they ran.
package vector

import "embed"

// FS holds every vector migration file, named like 00001_add_memory_embeddings.sql.
//
//go:embed *.sql
var FS embed.FS
