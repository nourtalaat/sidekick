//go:build integration

package bench

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// sampleReport is a true positive that cites the list_commits call.
const sampleReport = `{"verdict": "true_positive", "confidence": "high",
	"reason": "Latency rose right after commit abc123 shrank the DB pool.",
	"summary": "The DB pool ran out after abc123.", "cause": "Commit abc123 lowered the pool size to 5.",
	"impact": "Checkout p99 is above 2s.",
	"findings": [{"claim": "abc123 lowered the pool size to 5 at 10:05.", "evidenceToolCallIds": ["c1", "c2"]}],
	"fixes": [{"title": "Restore the pool size", "detail": "Set pool_size back to 20 or roll back abc123."}]}`

// testUsage is the token usage of every scripted turn.
var testUsage = model.Usage{InputTokens: 1000, OutputTokens: 200}

// sampleTurns are the turns of one run of the sample scenario: three
// source calls, the same tool again, and the verdict.
func sampleTurns() []modeltest.Turn {
	return []modeltest.Turn{
		modeltest.ToolUse(testUsage,
			modeltest.Call("c1", "github__list_commits", `{"repo": "acme/checkout", "since": "2026-05-04T06:00:00Z"}`),
			modeltest.Call("c2", "github__get_commit", `{"repo": "acme/checkout", "ref": "abc123"}`),
			modeltest.Call("c3", "github__list_workflow_runs", `{"repo": "acme/checkout"}`),
		),
		modeltest.ToolUse(testUsage, modeltest.Call("c4", "github__list_workflow_runs", `{"repo": "acme/checkout"}`)),
		modeltest.ToolUse(testUsage, modeltest.Call("v1", report.SubmitVerdictTool, sampleReport)),
	}
}

func TestRunSample(t *testing.T) {
	t.Parallel()

	const repeat = 3

	tests := []struct {
		name     string
		database func(t *testing.T) *Database
	}{
		{name: "in the default schema, as with a temporary Postgres", database: inDefaultSchema},
		{name: "in a schema of its own, as with --db", database: inOwnSchema},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var turns []modeltest.Turn
			for range repeat {
				turns = append(turns, sampleTurns()...)
			}

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, turns...)
			conn.ExpectAllUsed()

			database := tt.database(t)

			results, err := newRunner(t, conn, repeat, loadSample(t)).Run(t.Context(), database)
			require.NoError(t, err)
			require.Equal(t, repeat, results.Repeat)
			require.Len(t, results.Scenarios, 1)

			sc := results.Scenarios[0]
			require.Equal(t, "checkout-pool-size", sc.ID)
			require.Equal(t, report.TruePositive, sc.Expected.Verdict)
			require.Len(t, sc.Runs, repeat)

			first := sc.Runs[0]
			require.Len(t, first.ToolResults, 4)
			require.Equal(t, []string{"github__list_commits", "github__get_commit", "github__list_workflow_runs", "github__list_workflow_runs"},
				toolNames(first.ToolResults))
			require.Contains(t, first.ToolResults[0].Content, "Lower DB pool size to 5")
			require.Contains(t, first.ToolResults[1].Content, "pool_size 20 -> 5")
			require.Contains(t, first.ToolResults[2].Content, "run 991", "the first of the responses comes first")
			require.Equal(t, "no_data", first.ToolResults[3].Status, "the last of the responses comes next")

			for i, run := range sc.Runs {
				require.Equal(t, i+1, run.Run)
				require.Equal(t, investigator.StatusFinal, run.Status)
				require.Equal(t, report.TruePositive, run.Report.Verdict)
				require.Equal(t, report.StopSubmitted, run.Report.StopReason)
				require.Equal(t, 4, run.ToolCalls)
				require.Equal(t, 3*testUsage.InputTokens, run.InputTokens)
				require.Positive(t, run.CostMicros, "the shipped price table prices the model")
				require.Equal(t, first.ToolResults, run.ToolResults, "run %d gives the same tool results as the first", run.Run)
				require.Equal(t, report.CheckUnavailable, checkOf(t, run.Report.Checked, "k8s").Status, "the unavailable source is reported unavailable")
				require.Equal(t, report.CheckOK, checkOf(t, run.Report.Checked, "github").Status)
			}

			var investigations int
			require.NoError(t, database.Pool.QueryRow(t.Context(), "SELECT count(*) FROM investigations WHERE route = $1", RouteName).Scan(&investigations))
			require.Equal(t, repeat, investigations, "each run is an investigation of its own")
		})
	}
}

func TestRunProviderUnreachable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "overloaded", err: &model.Error{Kind: model.ErrOverloaded, Err: errors.New("overloaded")}},
		{name: "timeout", err: &model.Error{Kind: model.ErrTimeout, Err: context.DeadlineExceeded}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, modeltest.Fail(tt.err))
			conn.ExpectAllUsed()

			database := inDefaultSchema(t)

			results, err := newRunner(t, conn, 2, loadSample(t)).Run(t.Context(), database)
			require.ErrorIs(t, err, ErrProviderUnreachable)
			require.ErrorIs(t, err, ErrInfrastructure)
			require.Len(t, results.Scenarios, 1)
			require.Empty(t, results.Scenarios[0].Runs, "the run stops at the first failure")

			var status string
			require.NoError(t, database.Pool.QueryRow(t.Context(), "SELECT status FROM investigations").Scan(&status))
			require.Equal(t, investigator.StatusFailed, status)

			var dead int
			require.NoError(t, database.Pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE status = 'dead'").Scan(&dead))
			require.Equal(t, 1, dead, "the investigate job is given up, not retried")
		})
	}
}

func TestOpenSchemaWithoutCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a role without CREATE on the database"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := OpenSchema(t.Context(), withoutCreate(t), nil)
			require.ErrorIs(t, err, ErrCannotCreateSchema)
		})
	}
}

func TestOpenSchemaDropsItOnClose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "the schema and its tables"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dbURL := storetest.NewURL(t)

			d, err := OpenSchema(t.Context(), dbURL, nil)
			require.NoError(t, err)
			require.Regexp(t, `^sidekick_bench_[0-9a-f]{32}$`, d.Schema)

			admin, err := store.Open(t.Context(), dbURL, testMaxConns)
			require.NoError(t, err)
			t.Cleanup(admin.Close)

			require.Equal(t, 1, schemas(t, admin, d.Schema), "the schema exists while the run goes on")
			var jobs int
			require.NoError(t, d.Pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs").Scan(&jobs), "the schema's tables answer")

			require.NoError(t, d.Close(t.Context()))
			require.Equal(t, 0, schemas(t, admin, d.Schema), "Close drops the schema")
		})
	}
}

// schemas counts the schemas called name in the database of pool.
func schemas(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_namespace WHERE nspname = $1", name).Scan(&n))

	return n
}

// checkOf returns the checked record of the source called name.
func checkOf(t *testing.T, checked []report.Check, name string) report.Check {
	t.Helper()

	for _, c := range checked {
		if c.Source == name {
			return c
		}
	}

	require.Failf(t, "source not checked", "%s is not in %v", name, checked)

	return report.Check{}
}

// toolNames returns the tool of each result.
func toolNames(results []ToolResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Tool)
	}

	return out
}
