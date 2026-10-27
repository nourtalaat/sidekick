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
	if err := migrate(ctx, pool, migrations.FS, lock.DefaultLockID); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	return nil
}

// MigrateSchema is Migrate for a pool from OpenSchema. Its migration lock
// is keyed by schema, so migrating it never waits for, or holds up, a
// replica that migrates another schema of the same database.
func MigrateSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	if err := migrate(ctx, pool, migrations.FS, SchemaLockKey(lock.DefaultLockID, schema)); err != nil {
		return fmt.Errorf("migrate schema %s: %w", schema, err)
	}

	return nil
}

// MigrationStatus reports each migration and whether the database has it.
func MigrationStatus(ctx context.Context, pool *pgxpool.Pool) (statuses []*goose.MigrationStatus, err error) {
	provider, err := newProvider(pool, migrations.FS, lock.DefaultLockID)
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

// SchemaVersion returns the version of the newest migration the database
// has and of the newest one this binary embeds. The database's schema is
// current when current is at least target. It does not take the migration
// lock, so it neither waits for nor holds up a replica that is migrating.
func SchemaVersion(ctx context.Context, pool *pgxpool.Pool) (current, target int64, err error) {
	provider, err := newProvider(pool, migrations.FS, lock.DefaultLockID)
	if err != nil {
		return 0, 0, fmt.Errorf("read schema version: %w", err)
	}

	defer func() {
		err = closeProvider(provider, err)
	}()

	current, target, err = provider.GetVersions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read schema version: %w", err)
	}

	return current, target, nil
}

// migrate applies the migrations in fsys under the migration lock lockID.
// Tests call it with their own migrations.
func migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, lockID int64) (err error) {
	provider, err := newProvider(pool, fsys, lockID)
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

func newProvider(pool *pgxpool.Pool, fsys fs.FS, lockID int64) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(lockID), lock.WithLockTimeout(lockRetrySeconds, lockRetries))
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
