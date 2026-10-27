//go:build integration

package store

import (
	"context"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3/lock"
)

// MigrateFS lets the tests apply their own migrations.
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	return migrate(ctx, pool, fsys, lock.DefaultLockID)
}
