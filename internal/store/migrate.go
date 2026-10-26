package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/nourtalaat/sidekick/internal/store/migrations"
)

// A replica that finds another one migrating tries to take the migration
// lock again every lockRetrySeconds, for up to lockRetries times (five
// minutes), before it gives up.
const (
	lockRetrySeconds = 1
	lockRetries      = 300
)

// Migrate applies every migration that the database does not have yet. It
// holds a Postgres advisory lock (goose's session locker) while it runs, so
// when several replicas start at once, one migrates and the others wait for
// it and then find nothing left to do.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if err := migrate(ctx, pool, migrations.FS); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	return nil
}

// MigrationStatus reports each migration and whether the database has it.
func MigrationStatus(ctx context.Context, pool *pgxpool.Pool) (statuses []*goose.MigrationStatus, err error) {
	provider, err := newProvider(pool, migrations.FS)
	if err != nil {
		return nil, fmt.Errorf("read migration status: %w", err)
	}

	defer func() {
		err = closeProvider(provider, err)
	}()

	statuses, err = provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read migration status: %w", err)
	}

	return statuses, nil
}

// migrate applies the migrations in fsys. Tests call it with their own
// migrations.
func migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (err error) {
	provider, err := newProvider(pool, fsys)
	if err != nil {
		return fmt.Errorf("set up goose: %w", err)
	}

	defer func() {
		err = closeProvider(provider, err)
	}()

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}

func newProvider(pool *pgxpool.Pool, fsys fs.FS) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(lockRetrySeconds, lockRetries))
	if err != nil {
		return nil, fmt.Errorf("create migration lock: %w", err)
	}

	// Closing this *sql.DB leaves the pool open.
	sqlDB := stdlib.OpenDBFromPool(pool)

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("load migrations: %w", err), sqlDB.Close())
	}

	return provider, nil
}

// closeProvider closes provider and adds any error from that to err.
func closeProvider(provider *goose.Provider, err error) error {
	if closeErr := provider.Close(); closeErr != nil {
		return errors.Join(err, fmt.Errorf("close migration provider: %w", closeErr))
	}

	return err
}
