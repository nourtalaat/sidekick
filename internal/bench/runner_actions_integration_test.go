//go:build integration

package bench

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
)

// restartScenario is the approved-mode scenario with one mock action.
const restartScenario = "fixture/restart-scenario.yaml"

// restartReport is a true positive that cites the list_commits call.
const restartReport = `{"verdict": "true_positive", "confidence": "high",
	"reason": "Latency rose right after commit abc123 shrank the DB pool.",
	"summary": "The DB pool ran out after abc123.", "cause": "Commit abc123 lowered the pool size to 5.",
	"impact": "Checkout p99 is above 2s.",
	"findings": [{"claim": "abc123 lowered the pool size to 5 at 10:05.", "evidenceToolCallIds": ["c1"]}],
	"fixes": [{"title": "Restart checkout-api", "detail": "A restart clears the exhausted pool."}]}`

// Parameters the model proposes: the first breaks the action's schema (the
// service is not allowed), the second fits it.
const (
	badProposal  = `{"action": "restart", "params": {"service": "billing"}, "reason": "Restart it."}`
	goodProposal = `{"action": "restart", "params": {"service": "checkout-api", "graceSeconds": 5}, "reason": "The pool is stuck, a restart clears it."}`
)

// restartTurns are the turns of one run of the restart scenario: a source
// call, a proposal that breaks the schema, a proposal that fits it, and
// the verdict.
func restartTurns() []modeltest.Turn {
	return []modeltest.Turn{
		modeltest.ToolUse(testUsage, modeltest.Call("c1", "github__list_commits", `{"repo": "acme/checkout"}`)),
		modeltest.ToolUse(testUsage, modeltest.Call("p1", "propose_action", badProposal)),
		modeltest.ToolUse(testUsage, modeltest.Call("p2", "propose_action", goodProposal)),
		modeltest.ToolUse(testUsage, modeltest.Call("v1", report.SubmitVerdictTool, restartReport)),
	}
}

func TestRunApprovedScenario(t *testing.T) {
	t.Parallel()

	const repeat = 2

	tests := []struct {
		name string
	}{
		{name: "the proposals are recorded, refused ones are not, and nothing runs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var turns []modeltest.Turn
			for range repeat {
				turns = append(turns, restartTurns()...)
			}

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, turns...)
			conn.ExpectAllUsed()

			sc, err := LoadScenarios(restartScenario, LoadOptions{BaseDir: repoRoot})
			require.NoError(t, err)
			require.Len(t, sc, 1)

			database := inDefaultSchema(t)

			results, err := newRunner(t, conn, repeat, sc[0]).Run(t.Context(), database)
			require.NoError(t, err)
			require.Len(t, results.Scenarios, 1)
			require.Len(t, results.Scenarios[0].Runs, repeat)

			for _, run := range results.Scenarios[0].Runs {
				require.Equal(t, investigator.StatusFinal, run.Status)
				require.Equal(t, report.StopSubmitted, run.Report.StopReason)
				require.Len(t, run.Proposals, 1, "only the proposal that fits the schema is recorded")

				got := run.Proposals[0]
				require.Equal(t, "restart", got.Action)
				require.JSONEq(t, `{"service": "checkout-api", "graceSeconds": 5}`, string(got.Params))
				require.Equal(t, "The pool is stuck, a restart clears it.", got.Reason)
			}

			var proposed, stored int
			require.NoError(t, database.Pool.QueryRow(t.Context(), "SELECT count(*), count(*) FILTER (WHERE status = 'proposed') FROM action_proposals").Scan(&stored, &proposed))
			require.Equal(t, repeat, stored)
			require.Equal(t, repeat, proposed, "nobody decides a proposal")

			var executions int
			require.NoError(t, database.Pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1", queue.KindExecuteAction).Scan(&executions))
			require.Zero(t, executions, "nothing is queued to run a proposal")

			requests := conn.Requests()
			require.Len(t, requests, repeat*4)

			propose := toolSpec(t, requests[0].Tools, "propose_action")
			require.Contains(t, propose.Description, "restart: Restarts a service. Allowed services: checkout-api, ledger.")
			require.Contains(t, propose.Description, `"graceSeconds"`, "the description gives the schema of the action")

			refused := toolResult(t, requests[2], "p1")
			require.True(t, refused.IsError)
			require.Contains(t, refused.Content, "Nothing was proposed.")

			accepted := toolResult(t, requests[3], "p2")
			require.False(t, accepted.IsError)
			require.Contains(t, accepted.Content, "Nothing has run")
		})
	}
}

func TestRunReadOnlyScenarioHasNoProposals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "the sample scenario"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, sampleTurns()...)
			conn.ExpectAllUsed()

			results, err := newRunner(t, conn, 1, loadSample(t)).Run(t.Context(), inDefaultSchema(t))
			require.NoError(t, err)
			require.Nil(t, results.Scenarios[0].Runs[0].Proposals)

			for _, req := range conn.Requests() {
				for _, spec := range req.Tools {
					require.NotEqual(t, "propose_action", spec.Name, "a read-only run is not offered the tool")
				}
			}
		})
	}
}

// toolSpec returns the tool called name of a request.
func toolSpec(t *testing.T, specs []model.ToolSpec, name string) model.ToolSpec {
	t.Helper()

	for _, spec := range specs {
		if spec.Name == name {
			return spec
		}
	}

	require.Failf(t, "tool not offered", "%s is not in the request's tools", name)

	return model.ToolSpec{}
}

// toolResult returns the result the request carries for the tool call with
// callID.
func toolResult(t *testing.T, req model.Request, callID string) model.ToolResult {
	t.Helper()

	for _, msg := range req.Messages {
		for _, part := range msg.Parts {
			if res, ok := part.(model.ToolResult); ok && res.CallID == callID {
				return res
			}
		}
	}

	raw, err := json.Marshal(req.Messages)
	require.NoError(t, err)
	require.Failf(t, "no tool result", "call %s has no result in %s", callID, raw)

	return model.ToolResult{}
}
