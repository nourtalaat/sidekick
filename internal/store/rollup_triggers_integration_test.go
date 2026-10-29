//go:build integration

package store_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	rollupGroup = "0192d3a4-0000-7000-8000-0000000000b1"
	// The fixture's investigation was created on 2026-10-26 at 14:00:01 UTC.
	rollupStatsBucket   = "stats_hourly 2026-10-26 14:00"
	rollupQualityBucket = "quality_daily 2026-10-26 00:00"
	watcherID           = "0192d3a4-0000-7000-8000-0000000000d1"
)

const (
	updateInvestigation = "UPDATE investigations SET %s WHERE id = '" + notifyInvestigation + "'"
	insertWatcher       = `INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s,
		passes_to_recover, check_timeout_s, status, started_at, deadline)
		VALUES ('` + watcherID + `', '` + rollupGroup + `', '` + notifyInvestigation + `',
		'health', 'model', 'alert', '{}', 30, 2, 10, 'running', now(), now() + interval '1 hour')`
	insertFeedbackTemplate = `INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
		VALUES ('0192d3a4-0000-7000-8000-0000000000e1', '` + notifyInvestigation + `', '%s', '%s', 'correct', 'U1', 'slack', now(), now())`
)

func TestRollupTriggersMarkBuckets(t *testing.T) {
	t.Parallel()

	insertVerdictFeedback := fmt.Sprintf(insertFeedbackTemplate, "verdict", "run:1/verdict")

	tests := []struct {
		name string
		// setup runs first, and its marks are cleared before statements run.
		setup []string
		// statements run in one transaction, which commits unless rollback is set.
		statements []string
		rollback   bool
		want       []string
	}{
		{
			name: "a new investigation counts in the volume",
			statements: []string{`INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
				VALUES ('0192d3a4-0000-7000-8000-0000000000c2', '` + rollupGroup + `', 'default', 'read-only', 'queued', '{}', '{}', '2026-10-27 09:30:00+00')`},
			want: []string{"stats_hourly 2026-10-27 09:00"},
		},
		{
			name:       "a status change",
			statements: []string{fmt.Sprintf(updateInvestigation, "status = 'final'")},
			want:       []string{rollupStatsBucket},
		},
		{
			name:       "the end of a run",
			statements: []string{fmt.Sprintf(updateInvestigation, "finished_at = now(), status = 'final'")},
			want:       []string{rollupStatsBucket},
		},
		{
			name:       "the time to the first finding",
			statements: []string{fmt.Sprintf(updateInvestigation, "first_finding_at = now()")},
			want:       []string{rollupStatsBucket},
		},
		{
			name:       "the start of a run",
			statements: []string{fmt.Sprintf(updateInvestigation, "started_at = now()")},
			want:       []string{rollupStatsBucket},
		},
		{
			name:       "the fix mark",
			statements: []string{fmt.Sprintf(updateInvestigation, "fix_applied_at = now()")},
			want:       []string{rollupStatsBucket},
		},
		{
			name:       "a verdict",
			statements: []string{fmt.Sprintf(updateInvestigation, "verdict = 'false_positive'")},
			want:       []string{rollupQualityBucket, rollupStatsBucket},
		},
		{
			name:       "the model of the first model turn",
			statements: []string{fmt.Sprintf(updateInvestigation, "model = 'claude'")},
			want:       []string{rollupQualityBucket, rollupStatsBucket},
		},
		{
			name:       "a service filled in by the backfill",
			statements: []string{fmt.Sprintf(updateInvestigation, "service = 'checkout'")},
			want:       []string{rollupQualityBucket, rollupStatsBucket},
		},
		{
			name:       "the strategy that triage chose",
			statements: []string{fmt.Sprintf(updateInvestigation, "strategy = 'default'")},
			want:       []string{rollupQualityBucket},
		},
		{
			name:       "a confirmed verdict",
			statements: []string{fmt.Sprintf(updateInvestigation, "confirmed_verdict = 'true_positive', confirmed_at = now()")},
			want:       []string{rollupQualityBucket},
		},
		{
			name:       "a follow-up run",
			statements: []string{fmt.Sprintf(updateInvestigation, "run = run + 1")},
			want:       []string{rollupQualityBucket},
		},
		{
			name: "counters only, as every step advances them",
			statements: []string{
				fmt.Sprintf(updateInvestigation, "next_seq = next_seq + 1, cost_micros = cost_micros + 5, input_tokens = input_tokens + 3, tool_calls = tool_calls + 1"),
			},
		},
		{
			name:       "a rating of the verdict",
			statements: []string{insertVerdictFeedback},
			want:       []string{rollupQualityBucket},
		},
		{
			name:       "a rating of a finding",
			statements: []string{fmt.Sprintf(insertFeedbackTemplate, "finding", "run:1/finding:f1")},
		},
		{
			name:       "a rating of the verdict that changes",
			setup:      []string{insertVerdictFeedback},
			statements: []string{"UPDATE feedback SET rating = 'wrong', updated_at = now()"},
			want:       []string{rollupQualityBucket},
		},
		{
			name:       "a reason added to a rating",
			setup:      []string{insertVerdictFeedback},
			statements: []string{"UPDATE feedback SET reason = 'because', updated_at = now()"},
		},
		{
			name:       "a started watcher",
			statements: []string{insertWatcher},
			want:       []string{rollupStatsBucket},
		},
		{
			name:       "a check of a watcher",
			setup:      []string{insertWatcher},
			statements: []string{"UPDATE watchers SET checks = checks + 1, passes_in_a_row = 1, last_check_at = now()"},
		},
		{
			name:       "a recovered watcher",
			setup:      []string{insertWatcher},
			statements: []string{"UPDATE watchers SET status = 'recovered', recovered_at = now(), ended_at = now()"},
			want:       []string{rollupStatsBucket},
		},
		{
			name:       "a deleted investigation keeps the history",
			setup:      []string{insertWatcher},
			statements: []string{"DELETE FROM watchers", "DELETE FROM investigations"},
		},
		{
			name:       "two changes of one bucket",
			statements: []string{fmt.Sprintf(updateInvestigation, "status = 'final'"), fmt.Sprintf(updateInvestigation, "verdict = 'inconclusive'")},
			want:       []string{rollupQualityBucket, rollupStatsBucket},
		},
		{
			name:       "a rolled back change",
			statements: []string{fmt.Sprintf(updateInvestigation, "status = 'final'")},
			rollback:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			storetest.LoadFixture(t, pool, "fixture/notify.sql")

			for _, statement := range tt.setup {
				_, err := pool.Exec(t.Context(), statement)
				require.NoError(t, err)
			}

			_, err := pool.Exec(t.Context(), "DELETE FROM rollup_dirty")
			require.NoError(t, err)

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			for _, statement := range tt.statements {
				_, err := tx.Exec(t.Context(), statement)
				require.NoError(t, err)
			}

			if tt.rollback {
				require.NoError(t, tx.Rollback(t.Context()))
			} else {
				require.NoError(t, tx.Commit(t.Context()))
			}

			rows, err := pool.Query(t.Context(), `SELECT table_name || ' ' || to_char(bucket AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI')
				FROM rollup_dirty ORDER BY 1`)
			require.NoError(t, err)

			got, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)

			if len(tt.want) == 0 {
				require.Empty(t, got)

				return
			}

			require.Equal(t, tt.want, got)
		})
	}
}

// TestRollupHistogram checks the bucket of a time and the counts of a list
// of buckets, which the dashboard reads back as an approximate median.
func TestRollupHistogram(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)

	var buckets []int32

	err := pool.QueryRow(t.Context(), `SELECT array_agg(rollup_hist_bucket(ms) ORDER BY ms)
		FROM unnest(ARRAY[0, 500, 501, 799, 801, 60000, 3600000, 15000000, 16000000, 99999999999]::bigint[]) AS ms`).Scan(&buckets)
	require.NoError(t, err)
	require.Equal(t, []int32{0, 0, 1, 1, 2, 11, 19, 22, 23, 23}, buckets)

	var counts []int32

	err = pool.QueryRow(t.Context(), "SELECT rollup_hist(ARRAY[0, 0, 5, 23])").Scan(&counts)
	require.NoError(t, err)
	require.Len(t, counts, 24)
	require.Equal(t, int32(2), counts[0])
	require.Equal(t, int32(1), counts[5])
	require.Equal(t, int32(1), counts[23])

	err = pool.QueryRow(t.Context(), "SELECT rollup_hist(NULL::int[])").Scan(&counts)
	require.NoError(t, err)
	require.Equal(t, make([]int32, 24), counts)
}
