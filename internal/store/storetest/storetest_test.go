//go:build integration

package storetest_test

import (
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/migrations"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// postgres15 is Postgres 15.0 in the form of the server_version_num setting.
const postgres15 = 150000

func TestEachTestGetsItsOwnSeededDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "first test adds a note", body: "added by the first test"},
		{name: "second test adds a different note", body: "added by the second test"},
		{name: "third test adds another note", body: "added by the third test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			_, err := pool.Exec(t.Context(), "INSERT INTO example_notes (id, body) VALUES ($1, $2)", ids.New(), tt.body)
			require.NoError(t, err)

			rows, err := pool.Query(t.Context(), "SELECT body FROM example_notes ORDER BY id")
			require.NoError(t, err)

			bodies, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)
			require.Equal(t, []string{"first note", "second note", tt.body}, bodies)
		})
	}
}

func TestNewAppliesEveryMigration(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)

	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)

	statuses, err := store.MigrationStatus(t.Context(), pool)
	require.NoError(t, err)
	require.Len(t, statuses, len(files))

	for _, status := range statuses {
		require.Equal(t, goose.StateApplied, status.State, "migration %s", status.Source.Path)
	}

	version, err := db.New(pool).ServerVersionNum(t.Context())
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, int32(postgres15))
}

func TestNewEmptyURLHasNoMigrations(t *testing.T) {
	t.Parallel()

	const maxConns = 1

	pool, err := store.Open(t.Context(), storetest.NewEmptyURL(t), maxConns)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var hasVersionTable bool
	err = pool.QueryRow(t.Context(), "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&hasVersionTable)
	require.NoError(t, err)
	require.False(t, hasVersionTable)
}
