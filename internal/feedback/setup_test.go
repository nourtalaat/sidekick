//go:build integration

package feedback

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The investigations in fixture/investigations.sql.
var (
	finalInvestigation   = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	runningInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c2")
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a pool on a database of the test's own, seeded with
// fixture/investigations.sql.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	return pool
}

// rate rates a verdict in a transaction of its own.
func rate(t *testing.T, pool *pgxpool.Pool, r VerdictRating) (Result, error) {
	t.Helper()

	var res Result

	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		var err error
		res, err = RateVerdict(t.Context(), tx, r)

		return err
	})

	return res, err
}

// investigation returns the investigation with id.
func investigation(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) db.Investigation {
	t.Helper()

	inv, err := db.New(pool).GetInvestigation(t.Context(), id)
	require.NoError(t, err)

	return inv
}
