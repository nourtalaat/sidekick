//go:build integration

package rules_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// actorID is the Slack user who makes the changes in the tests.
const actorID = "U01ADMIN"

var actor = rules.SlackUser(actorID)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a database of the test's own, seeded with
// fixture/rules.sql, and a Store on it with fileConfig as the file.
func setup(t *testing.T) (*pgxpool.Pool, *rules.Store) {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/rules.sql")

	return pool, rules.NewStore(pool, fileConfig())
}

// auditRow is one audit log row as the tests compare it.
type auditRow struct {
	Actor   string
	Action  string
	Target  string
	Details map[string]any
}

// auditRows returns the audit log, oldest first.
func auditRows(t *testing.T, pool *pgxpool.Pool) []auditRow {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT actor_id, action, target_kind || ':' || target_id, details FROM audit_log ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []auditRow

	for rows.Next() {
		var (
			r       auditRow
			details []byte
		)

		require.NoError(t, rows.Scan(&r.Actor, &r.Action, &r.Target, &details))
		require.NoError(t, json.Unmarshal(details, &r.Details))
		out = append(out, r)
	}

	require.NoError(t, rows.Err())

	return out
}
