//go:build integration

package strategystore_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

// samID is the user who saved the strategy in fixture/strategies.sql.
const samID = "01920000-0000-7000-8000-000000000070"

// sam is the user who saves in the tests.
var sam = strategystore.Actor{Kind: audit.ActorUser, ID: samID}

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a database of the test's own, seeded with
// fixture/strategies.sql, and a Store on it.
func setup(t *testing.T) (*pgxpool.Pool, *strategystore.Store) {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/strategies.sql")

	return pool, strategystore.NewStore(pool)
}

// body returns the YAML of a minimal strategy.
func body(name, description string) string {
	return "name: " + name + "\ndescription: " + description + "\n"
}

// auditRow is one audit log row as the tests compare it.
type auditRow struct {
	Actor   string
	Action  string
	Target  string
	Details string
}

// auditRows returns the audit log, oldest first.
func auditRows(t *testing.T, pool *pgxpool.Pool) []auditRow {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT actor_id, action, target_kind || ':' || target_id, details::text FROM audit_log ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []auditRow

	for rows.Next() {
		var r auditRow

		require.NoError(t, rows.Scan(&r.Actor, &r.Action, &r.Target, &r.Details))

		out = append(out, r)
	}

	require.NoError(t, rows.Err())

	return out
}
