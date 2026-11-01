//go:build integration

package proposals_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// trigger is the trigger of the tests: three wrong ratings within 14 days.
var trigger = proposals.Trigger{Enabled: true, Threshold: 3, Window: 14 * 24 * time.Hour}

// invID returns the ID of fixture investigation n.
func invID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("01920000-0000-7000-8000-00000000010%d", n))
}

// rating is one rating the tests insert.
type rating struct {
	inv    int
	kind   string
	rating string
	// ago is how long before now the rating was last changed.
	ago time.Duration
}

// wrongVerdict is a rating of wrong on the verdict of investigation inv, ago
// before now.
func wrongVerdict(inv int, ago time.Duration) rating {
	return rating{inv: inv, kind: "verdict", rating: "wrong", ago: ago}
}

func TestTriggerCheck(t *testing.T) {
	t.Parallel()

	const day = 24 * time.Hour

	tests := []struct {
		name      string
		trigger   proposals.Trigger
		ratings   []rating
		rated     int
		wantNew   bool
		wantWrong int32
	}{
		{name: "two wrong ratings are below the threshold", trigger: trigger, ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0)}, rated: 2},
		{name: "three wrong ratings reach it", trigger: trigger, ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, time.Hour), wrongVerdict(3, 2*day)}, rated: 3, wantNew: true, wantWrong: 3},
		{
			name: "four wrong ratings reach it with a count of four", trigger: trigger, rated: 4, wantNew: true, wantWrong: 4,
			ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0), wrongVerdict(3, 0), wrongVerdict(4, 0)},
		},
		{
			name: "ratings older than the window do not count", trigger: trigger, rated: 3,
			ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0), wrongVerdict(3, 15*day), wrongVerdict(4, 30*day)},
		},
		{
			name: "a rating on the edge of the window counts", trigger: trigger, rated: 3, wantNew: true, wantWrong: 3,
			ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0), wrongVerdict(3, 13*day+23*time.Hour)},
		},
		{
			name: "correct ratings do not count", trigger: trigger, rated: 3,
			ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0), {inv: 3, kind: "verdict", rating: "correct"}},
		},
		{
			name: "wrong ratings on a finding count", trigger: trigger, rated: 3, wantNew: true, wantWrong: 3,
			ratings: []rating{wrongVerdict(1, 0), {inv: 2, kind: "finding", rating: "wrong"}, {inv: 3, kind: "finding", rating: "wrong"}},
		},
		{
			name: "wrong ratings on a tool call or a fix do not count", trigger: trigger, rated: 3,
			ratings: []rating{wrongVerdict(1, 0), {inv: 2, kind: "tool_call", rating: "wrong"}, {inv: 3, kind: "fix", rating: "wrong"}, {inv: 4, kind: "tool_call", rating: "wrong"}},
		},
		{
			name: "another version of the strategy does not count", trigger: trigger, rated: 1,
			ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0), wrongVerdict(6, 0)},
		},
		{
			name: "another strategy does not count", trigger: trigger, rated: 1,
			ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0), wrongVerdict(7, 0)},
		},
		{
			name: "an investigation without a strategy is never counted", trigger: trigger, rated: 8,
			ratings: []rating{wrongVerdict(8, 0), wrongVerdict(8, 0), wrongVerdict(8, 0)},
		},
		{
			name: "a trigger that is off does nothing", trigger: proposals.Trigger{Threshold: 3, Window: 14 * day}, rated: 3,
			ratings: []rating{wrongVerdict(1, 0), wrongVerdict(2, 0), wrongVerdict(3, 0)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			storetest.LoadFixture(t, pool, "fixture/investigations.sql")

			for i, r := range tt.ratings {
				insertRating(t, pool, i, r)
			}

			got := check(t, pool, tt.trigger, tt.rated)
			require.Equal(t, tt.wantNew, got)

			rows := proposalRows(t, pool)

			if !tt.wantNew {
				require.Empty(t, rows)
				require.Zero(t, jobCount(t, pool))

				return
			}

			require.Len(t, rows, 1)
			require.Equal(t, "latency-alert", rows[0].Name)
			require.Equal(t, "v1", rows[0].BaseVersion)
			require.Equal(t, proposals.StatusDrafting, rows[0].Status)
			require.Equal(t, tt.wantWrong, rows[0].TriggerCount)

			job := onlyJob(t, pool)
			require.Equal(t, queue.KindProposeStrategy, job.Kind)
			require.Equal(t, new(proposals.ProposeStrategyKey(rows[0].ID)), job.UniqueKey)

			var payload proposals.ProposeStrategyPayload
			require.NoError(t, json.Unmarshal(job.Payload, &payload))
			require.Equal(t, rows[0].ID, payload.ProposalID)

			require.Equal(t, 1, auditCount(t, pool, proposals.ActionCreated))
		})
	}
}

func TestTriggerFiresOncePerVersion(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	for i, inv := range []int{1, 2, 3, 4, 5} {
		insertRating(t, pool, i, wrongVerdict(inv, 0))
	}

	var fired []bool

	for _, inv := range []int{3, 4, 5} {
		fired = append(fired, check(t, pool, trigger, inv))
	}

	require.Equal(t, []bool{true, false, false}, fired, "only the first check at or over the threshold adds a proposal")
	require.Len(t, proposalRows(t, pool), 1)
	require.Equal(t, 1, jobCount(t, pool))

	// Even when the proposal's job has ended and the proposal failed, a later
	// rating adds none for the same version.
	_, err := pool.Exec(t.Context(), "UPDATE jobs SET status = 'done'; UPDATE strategy_proposals SET status = 'draft_failed'")
	require.NoError(t, err)

	insertRating(t, pool, 9, wrongVerdict(1, 0))
	require.False(t, check(t, pool, trigger, 1))
	require.Len(t, proposalRows(t, pool), 1)
	require.Equal(t, 1, jobCount(t, pool))
}

func TestTriggerGivesANewVersionItsOwnProposal(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	for i, inv := range []int{1, 2, 3} {
		insertRating(t, pool, i, wrongVerdict(inv, 0))
	}

	require.True(t, check(t, pool, trigger, 1))

	// Investigations 4 and 5 ran v2 of the strategy, and 6 already did.
	_, err := pool.Exec(t.Context(), "UPDATE investigations SET strategy_version = 'v2' WHERE id = ANY ($1)", []uuid.UUID{invID(4), invID(5)})
	require.NoError(t, err)

	for i, inv := range []int{4, 5, 6} {
		insertRating(t, pool, 10+i, wrongVerdict(inv, 0))
	}

	require.True(t, check(t, pool, trigger, 6))

	rows := proposalRows(t, pool)
	require.Len(t, rows, 2)
	require.ElementsMatch(t, []string{"v1", "v2"}, []string{rows[0].BaseVersion, rows[1].BaseVersion})
}

// insertRating inserts the rating r by a Slack user that is n.
func insertRating(t *testing.T, pool *pgxpool.Pool, n int, r rating) {
	t.Helper()

	_, err := pool.Exec(t.Context(), `INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'slack', now() - $7::interval, now() - $7::interval)`,
		uuid.New(), invID(r.inv), r.kind, fmt.Sprintf("ref-%d", n), r.rating, fmt.Sprintf("U%02d", n), fmt.Sprintf("%d seconds", int(r.ago.Seconds())))
	require.NoError(t, err)
}

// check runs the trigger for investigation inv in a transaction it commits,
// and reports whether it added a proposal.
func check(t *testing.T, pool *pgxpool.Pool, tr proposals.Trigger, inv int) bool {
	t.Helper()

	var added bool

	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		row, err := db.New(tx).LockInvestigation(t.Context(), invID(inv))
		if err != nil {
			return fmt.Errorf("lock the investigation: %w", err)
		}

		added, err = tr.Check(t.Context(), tx, row)
		if err != nil {
			return fmt.Errorf("check: %w", err)
		}

		return nil
	})
	require.NoError(t, err)

	return added
}

// proposalRows returns every proposal.
func proposalRows(t *testing.T, pool *pgxpool.Pool) []db.StrategyProposal {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT id, name, base_version, trigger_count, status FROM strategy_proposals ORDER BY created_at")
	require.NoError(t, err)

	defer rows.Close()

	var out []db.StrategyProposal

	for rows.Next() {
		var p db.StrategyProposal

		require.NoError(t, rows.Scan(&p.ID, &p.Name, &p.BaseVersion, &p.TriggerCount, &p.Status))

		out = append(out, p)
	}

	require.NoError(t, rows.Err())

	return out
}

// jobCount returns how many jobs there are.
func jobCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs").Scan(&n))

	return n
}

// onlyJob returns the one job there is.
func onlyJob(t *testing.T, pool *pgxpool.Pool) db.Job {
	t.Helper()

	var j db.Job

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT kind, payload, unique_key FROM jobs").Scan(&j.Kind, &j.Payload, &j.UniqueKey))

	return j
}

// auditCount returns how many audit entries have the action.
func auditCount(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE action = $1", action).Scan(&n))

	return n
}
