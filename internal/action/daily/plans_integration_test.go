//go:build integration

package daily

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on CountActionUses over 50,000
// proposals, 200 of them of the counted action, and checks that it uses
// the indexes made for it. Run with -v to see the
// plan.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "../proposals/fixture/busy.sql")

	// Give the counted action 200 proposals next to the fixture's 50,000:
	// 20 approved and waiting, 180 done a minute ago.
	_, err := pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, decided_at, executed_at)
		SELECT md5('ops' || n)::uuid, md5('investigation' || (1 + n))::uuid, 'ops-issues', '{}', '', 'approved',
		       CASE WHEN n <= 20 THEN 'approved' ELSE 'succeeded' END, 'sidekick/' || md5('ops' || n),
		       now() - interval '1 hour', now() + interval '1 hour', now() - make_interval(secs => 600 - n),
		       CASE WHEN n <= 20 THEN NULL ELSE now() - interval '1 minute' END
		FROM generate_series(1, 200) AS n;
		ANALYZE action_proposals;`, pgx.QueryExecModeSimpleProtocol)
	require.NoError(t, err)

	var self uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('ops' || 10)::uuid").Scan(&self))

	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)

	defer func() { _ = tx.Rollback(t.Context()) }()

	explain := storetest.NewExplainer(t, tx)
	now := time.Now()

	_, err = db.New(explain).CountActionUses(t.Context(), db.CountActionUsesParams{
		Action: "ops-issues", ID: self, DayStart: now.Add(-12 * time.Hour), DayEnd: now.Add(12 * time.Hour),
	})
	require.ErrorIs(t, err, storetest.ErrExplained)
	require.Contains(t, explain.Plan, "action_proposals_daily")
	require.Contains(t, explain.Plan, "action_proposals_status_proposed")
	require.NotContains(t, explain.Plan, "Seq Scan on action_proposals")
}
