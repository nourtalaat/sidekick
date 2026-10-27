package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// searchPathParam is the Postgres setting that names the schema unqualified
// table names resolve in.
const searchPathParam = "search_path"

// schemaPattern is what a schema name given to OpenSchema may look like:
// a plain Postgres identifier that needs no quoting.
var schemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ErrBadSchemaName is returned for a schema name that schemaPattern does not
// match.
var ErrBadSchemaName = errors.New("schema names are lower-case letters, digits and '_', at most 63 of them, not starting with a digit")

// OpenSchema is Open with the search_path of every connection set to
// schema, so Sidekick's tables, and the migrations that make them, live in
// that schema and not in the database's default one. The schema must
// exist; see CreateSchema.
func OpenSchema(ctx context.Context, url, schema string, maxConns int32) (*pgxpool.Pool, error) {
	if !schemaPattern.MatchString(schema) {
		return nil, fmt.Errorf("open schema %q: %w", schema, ErrBadSchemaName)
	}

	pool, err := open(ctx, url, maxConns, func(cfg *pgxpool.Config) {
		cfg.ConnConfig.RuntimeParams[searchPathParam] = schema
	})
	if err != nil {
		return nil, fmt.Errorf("open schema %s: %w", schema, err)
	}

	return pool, nil
}

// CreateSchema creates the schema called schema through pool.
func CreateSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("create schema %q: %w", schema, ErrBadSchemaName)
	}

	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("create schema %s: %w", schema, err)
	}

	return nil
}

// DropSchema drops the schema called schema through pool, with everything
// in it.
func DropSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("drop schema %q: %w", schema, ErrBadSchemaName)
	}

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		return fmt.Errorf("drop schema %s: %w", schema, err)
	}

	return nil
}

// SchemaLockKey returns the advisory lock key to use in place of key for
// the tables in schema. Advisory locks belong to the database, not to a
// schema, so a process whose tables are in a schema of their own keys its
// locks by the schema, and never waits for, or holds up, a process that
// uses the same lock on other tables. An empty schema, the default one,
// keeps key as it is.
func SchemaLockKey(key int64, schema string) int64 {
	if schema == "" {
		return key
	}

	buf := binary.BigEndian.AppendUint64(nil, uint64(key)) //nolint:gosec // The bits of the key are hashed, not its value.
	buf = append(buf, schema...)

	h := fnv.New64a()
	_, _ = h.Write(buf) // Writing to a hash never fails.

	return int64(h.Sum64()) //nolint:gosec // Any 64 bits make a lock key.
}
