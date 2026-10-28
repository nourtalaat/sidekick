//go:build integration

package backfill

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a pool on a database of its own seeded with
// fixture/investigations.sql.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	return pool
}

// listed is what the list columns of an investigation hold.
type listed struct {
	ID      string
	Title   *string
	Service *string
	Model   *string
}

// columns returns the list columns of every investigation, in ID order.
func columns(t *testing.T, pool *pgxpool.Pool) []listed {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT right(id::text, 2), alert_title, service, model FROM investigations ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []listed

	for rows.Next() {
		var l listed

		require.NoError(t, rows.Scan(&l.ID, &l.Title, &l.Service, &l.Model))

		out = append(out, l)
	}

	require.NoError(t, rows.Err())

	return out
}

func ptr(s string) *string {
	return &s
}
