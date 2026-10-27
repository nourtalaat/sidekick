// Package store connects to the Sidekick Postgres database and migrates it.
// The queries live in internal/store/queries and run through the code that
// sqlc generates in internal/store/db.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// minServerVersionNum is the oldest Postgres that Sidekick supports, 15.0,
// in the form of the server_version_num setting.
const minServerVersionNum = 150000

// serverVersionNumDivisor turns a server_version_num such as 160004 into
// its major version, 16.
const serverVersionNumDivisor = 10000

// ErrUnsupportedServer means the database runs a Postgres older than 15.
var ErrUnsupportedServer = errors.New("unsupported Postgres version, 15 or newer is required")

// Open connects to the database at url with a pool of at most maxConns
// connections, and checks that the server runs Postgres 15 or newer. The
// caller closes the pool.
func Open(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	return open(ctx, url, maxConns, nil)
}

// open is Open with configure, when not nil, changing the pool's config
// before the pool connects.
func open(ctx context.Context, url string, maxConns int32, configure func(*pgxpool.Config)) (*pgxpool.Pool, error) {
	if maxConns < 1 {
		return nil, fmt.Errorf("open database: max connections is %d, want at least 1", maxConns)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}

	cfg.MaxConns = maxConns

	if configure != nil {
		configure(cfg)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := checkServerVersion(ctx, pool); err != nil {
		pool.Close()

		return nil, fmt.Errorf("open database: %w", err)
	}

	return pool, nil
}

func checkServerVersion(ctx context.Context, pool *pgxpool.Pool) error {
	version, err := db.New(pool).ServerVersionNum(ctx)
	if err != nil {
		return fmt.Errorf("read Postgres version: %w", err)
	}

	if version < minServerVersionNum {
		return fmt.Errorf("server runs Postgres %d: %w", version/serverVersionNumDivisor, ErrUnsupportedServer)
	}

	return nil
}
