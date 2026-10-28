//go:build integration

package memory

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a pool on a database of the test's own, seeded with the
// fixture files given.
func setup(t *testing.T, fixtures ...string) *pgxpool.Pool {
	t.Helper()

	pool := storetest.New(t)
	for _, f := range fixtures {
		storetest.LoadFixture(t, pool, f)
	}

	return pool
}

// setupVector is setup on a database of a Postgres with pgvector, with the
// vector migrations applied for vectors of the given length.
func setupVector(t *testing.T, dimensions int, fixtures ...string) *pgxpool.Pool {
	t.Helper()

	pool := storetest.NewVector(t, dimensions)
	for _, f := range fixtures {
		storetest.LoadFixture(t, pool, f)
	}

	return pool
}
