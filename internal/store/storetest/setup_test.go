//go:build integration

package storetest_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started. Every package with integration tests has one.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a pool on a fresh, migrated database seeded from fixture/.
// In a feature package, setup also builds the services and handlers under
// test on top of the pool.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/notes.sql")

	return pool
}
