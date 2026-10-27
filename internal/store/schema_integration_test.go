//go:build integration

package store_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// schemaMaxConns is the pool size of the schema tests.
const schemaMaxConns = 2

func TestOpenSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema string
	}{
		{name: "a bench schema", schema: "sidekick_bench_0192d3a4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			url := storetest.NewURL(t)

			admin, err := store.Open(t.Context(), url, schemaMaxConns)
			require.NoError(t, err)
			t.Cleanup(admin.Close)

			require.NoError(t, store.CreateSchema(t.Context(), admin, tt.schema))

			pool, err := store.OpenSchema(t.Context(), url, tt.schema, schemaMaxConns)
			require.NoError(t, err)

			t.Cleanup(pool.Close)

			require.NoError(t, store.MigrateSchema(t.Context(), pool, tt.schema))

			_, err = pool.Exec(t.Context(), "INSERT INTO jobs (kind, payload) VALUES ('publish', '{}')")
			require.NoError(t, err)

			jobsInSchema := "SELECT count(*) FROM " + tt.schema + ".jobs" //nolint:gosec // A schema name from the test table.
			require.Equal(t, 1, count(t, admin, jobsInSchema), "the job is in the schema")
			require.Equal(t, 0, count(t, admin, "SELECT count(*) FROM public.jobs"), "the default schema has no job")
			require.Equal(t, 1, count(t, admin, "SELECT count(*) FROM pg_tables WHERE schemaname = $1 AND tablename = 'goose_db_version'", tt.schema),
				"the migrations are recorded in the schema")

			pool.Close()
			require.NoError(t, store.DropSchema(t.Context(), admin, tt.schema))
			require.Equal(t, 0, count(t, admin, "SELECT count(*) FROM pg_namespace WHERE nspname = $1", tt.schema))
		})
	}
}

// count runs a query that returns one number.
func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), sql, args...).Scan(&n))

	return n
}
