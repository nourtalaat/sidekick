//go:build integration

package store_test

import (
	"context"
	"io/fs"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/migrations"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	// replicas is how many migrators run against one database at once.
	replicas = 2
	// replicaMaxConns is the pool size of each simulated replica.
	replicaMaxConns = 2
)

// TestMigrateAtTheSameTime starts several migrators on one empty database at
// once, as replicas do when they start together, and checks that each
// migration was applied exactly once.
func TestMigrateAtTheSameTime(t *testing.T) {
	t.Parallel()

	fixtureMigrations := os.DirFS("fixture/migrations")

	tests := []struct {
		name    string
		fsys    fs.FS
		migrate func(ctx context.Context, pool *pgxpool.Pool) error
	}{
		{
			name:    "the real migrations",
			fsys:    migrations.FS,
			migrate: store.Migrate,
		},
		{
			name: "migrations that fail if they run twice",
			fsys: fixtureMigrations,
			migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
				return store.MigrateFS(ctx, pool, fixtureMigrations)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			url := storetest.NewEmptyURL(t)
			pools := make([]*pgxpool.Pool, replicas)

			for i := range pools {
				pool, err := store.Open(t.Context(), url, replicaMaxConns)
				require.NoError(t, err)
				t.Cleanup(pool.Close)

				pools[i] = pool
			}

			var group errgroup.Group

			for _, pool := range pools {
				group.Go(func() error {
					return tt.migrate(t.Context(), pool)
				})
			}

			require.NoError(t, group.Wait())
			require.Equal(t, migrationVersions(t, tt.fsys), appliedVersions(t, pools[0]))
		})
	}
}

func TestMigrationStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		url       func(t testing.TB) string
		wantState goose.State
	}{
		{name: "new database", url: storetest.NewEmptyURL, wantState: goose.StatePending},
		{name: "migrated database", url: storetest.NewURL, wantState: goose.StateApplied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := store.Open(t.Context(), tt.url(t), replicaMaxConns)
			require.NoError(t, err)
			t.Cleanup(pool.Close)

			statuses, err := store.MigrationStatus(t.Context(), pool)
			require.NoError(t, err)
			require.Len(t, statuses, len(migrationVersions(t, migrations.FS)))

			for _, status := range statuses {
				require.Equal(t, tt.wantState, status.State, "migration %s", status.Source.Path)
			}
		})
	}
}

func TestSchemaVersion(t *testing.T) {
	t.Parallel()

	versions := migrationVersions(t, migrations.FS)
	latest := versions[len(versions)-1]

	tests := []struct {
		name        string
		url         func(t testing.TB) string
		wantCurrent int64
	}{
		{name: "new database", url: storetest.NewEmptyURL, wantCurrent: 0},
		{name: "migrated database", url: storetest.NewURL, wantCurrent: latest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := store.Open(t.Context(), tt.url(t), replicaMaxConns)
			require.NoError(t, err)
			t.Cleanup(pool.Close)

			current, target, err := store.SchemaVersion(t.Context(), pool)
			require.NoError(t, err)
			require.Equal(t, tt.wantCurrent, current)
			require.Equal(t, latest, target)
		})
	}
}

func TestOpenSetsThePoolSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxConns int32
	}{
		{name: "one connection", maxConns: 1},
		{name: "several connections", maxConns: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := store.Open(t.Context(), storetest.NewURL(t), tt.maxConns)
			require.NoError(t, err)
			t.Cleanup(pool.Close)

			require.Equal(t, tt.maxConns, pool.Config().MaxConns)
		})
	}
}

// migrationVersions returns the version of each migration file in fsys.
func migrationVersions(t *testing.T, fsys fs.FS) []int64 {
	t.Helper()

	files, err := fs.Glob(fsys, "*.sql")
	require.NoError(t, err)

	versions := make([]int64, 0, len(files))

	for _, file := range files {
		version, err := goose.NumericComponent(file)
		require.NoError(t, err)

		versions = append(versions, version)
	}

	return versions
}

// appliedVersions returns every version that goose recorded as applied, in
// the order it applied them. A migration that ran twice shows up twice.
func appliedVersions(t *testing.T, pool *pgxpool.Pool) []int64 {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT version_id FROM goose_db_version WHERE version_id > 0 AND is_applied ORDER BY id")
	require.NoError(t, err)

	versions, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err)

	return versions
}
