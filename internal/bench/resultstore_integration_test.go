//go:build integration

package bench

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestResultStoreSave(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		thresholds []ThresholdResult
		wantStatus string
	}{
		{name: "every threshold met", thresholds: testThresholds[:1], wantStatus: statusPassed},
		{name: "a failed threshold", thresholds: testThresholds, wantStatus: statusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dbURL := storetest.NewURL(t)

			s, err := OpenResultStore(t.Context(), dbURL)
			require.NoError(t, err)
			t.Cleanup(s.Close)

			info := testInfo
			info.Label = "nightly"

			rep, err := scoredResults(t).Report(info, tt.thresholds)
			require.NoError(t, err)

			id, err := s.Save(t.Context(), rep)
			require.NoError(t, err)

			pool, err := store.Open(t.Context(), dbURL, testMaxConns)
			require.NoError(t, err)
			t.Cleanup(pool.Close)

			var (
				label, status, triggeredBy, gitSHA string
				repeats                            int
				summary, connectors                []byte
			)

			require.NoError(t, pool.QueryRow(t.Context(),
				"SELECT label, status, triggered_by, git_sha, repeats, summary, connectors FROM bench_runs WHERE id = $1", id).
				Scan(&label, &status, &triggeredBy, &gitSHA, &repeats, &summary, &connectors))
			require.Equal(t, "nightly", label)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, "cli", triggeredBy)
			require.Equal(t, "abc1234", gitSHA)
			require.Equal(t, 3, repeats)
			require.Contains(t, string(connectors), "claude-opus-5-5")

			var stored storedSummary
			require.NoError(t, json.Unmarshal(summary, &stored))
			require.Equal(t, rep.Metrics.Runs, stored.Metrics.Runs)
			require.Equal(t, JudgePromptVersion, stored.JudgePromptVersion)
			require.Len(t, stored.Thresholds, len(tt.thresholds))

			rows, err := pool.Query(t.Context(), `SELECT scenario_id, repeat, verdict, verdict_correct, root_cause_score, judge_reason, evidence_score
				FROM bench_results WHERE run_id = $1 ORDER BY scenario_id, repeat`, id)
			require.NoError(t, err)

			type result struct {
				scenario       string
				repeat         int
				verdict        *string
				correct        bool
				rootCauseScore *float32
				judgeReason    *string
				evidenceScore  *float32
			}

			var got []result

			for rows.Next() {
				var r result
				require.NoError(t, rows.Scan(&r.scenario, &r.repeat, &r.verdict, &r.correct, &r.rootCauseScore, &r.judgeReason, &r.evidenceScore))
				got = append(got, r)
			}

			require.NoError(t, rows.Err())
			require.Len(t, got, 5, "one row per run")

			// The rows sort as judge-error, no-rubric, then pool runs 1 to 3.
			require.Equal(t, "no grades: no grades", *got[0].judgeReason)
			require.Nil(t, got[1].rootCauseScore, "no rubric, no grade")
			require.Nil(t, got[1].evidenceScore, "no evidence checks")
			require.Equal(t, "pool", got[2].scenario)
			require.Equal(t, 1, got[2].repeat)
			require.InDelta(t, 1.0, *got[2].rootCauseScore, shareDelta)
			require.InDelta(t, 1.0, *got[2].evidenceScore, shareDelta)
			require.Contains(t, *got[3].judgeReason, "commit: not met: ")
			require.Equal(t, "inconclusive", *got[4].verdict)
			require.False(t, got[4].correct)
		})
	}
}

func TestOpenResultStoreNeedsCurrentSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a database without the newest migration"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dbURL := storetest.NewURL(t)

			pool, err := store.Open(t.Context(), dbURL, testMaxConns)
			require.NoError(t, err)
			t.Cleanup(pool.Close)

			_, err = pool.Exec(t.Context(), "DELETE FROM goose_db_version WHERE version_id = (SELECT max(version_id) FROM goose_db_version)")
			require.NoError(t, err)

			_, err = OpenResultStore(t.Context(), dbURL)
			require.ErrorIs(t, err, ErrSchemaNotCurrent)
		})
	}
}
