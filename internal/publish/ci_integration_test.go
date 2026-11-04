//go:build integration

package publish

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

// ciProposal is the proposal the CI report tests add a report for.
var ciProposal = uuid.MustParse("c1000000-0000-4000-8000-000000000001")

func TestCIReported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		accepts []output.Phase
		want    []output.Phase
	}{
		{
			name:    "a sink that takes CI reports gets the report once, before the state",
			accepts: []output.Phase{output.PhaseQueued, output.PhaseCI},
			want:    []output.Phase{output.PhaseCI, output.PhaseQueued},
		},
		{
			name:    "a sink that takes no CI reports gets only the state",
			accepts: []output.Phase{output.PhaseQueued},
			want:    []output.Phase{output.PhaseQueued},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, sink := setupRecording(t, tt.accepts)
			h.open(t, webhookInvestigation, slackSink)
			require.NoError(t, h.run(t))
			sink.reset()

			h.addCIReport(t, webhookInvestigation, ciProposal)
			h.inTx(t, func(tx pgx.Tx) error { return CIReported(t.Context(), tx, webhookInvestigation, ciProposal) })
			require.NoError(t, h.run(t))

			var phases []output.Phase

			for _, u := range sink.all() {
				phases = append(phases, u.Phase)

				if u.Phase == output.PhaseCI {
					require.Equal(t, &output.CIReport{State: output.CIFailed, Total: 4, Passed: 3, FailedCount: 1, Failed: []output.CICheck{{Name: "unit tests"}}}, u.CI)
				}
			}

			require.Equal(t, tt.want, phases)

			sink.reset()
			require.NoError(t, enqueueJob(t.Context(), h.pool, Payload{InvestigationID: webhookInvestigation, Sink: slackSink}))
			require.NoError(t, h.run(t))
			require.Empty(t, sink.all(), "nothing is sent twice")
		})
	}
}

func TestCIReportedWithoutAPublishState(t *testing.T) {
	t.Parallel()

	h, _ := setupRecording(t, []output.Phase{output.PhaseCI})
	h.addCIReport(t, slackInvestigation, ciProposal)
	h.inTx(t, func(tx pgx.Tx) error { return CIReported(t.Context(), tx, slackInvestigation, ciProposal) })

	require.Empty(t, h.jobs(t))
}

// addCIReport adds a succeeded proposal of the investigation with a failed
// CI report that has been made.
func (h *harness) addCIReport(t *testing.T, investigation, proposal uuid.UUID) {
	t.Helper()

	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

	_, err := h.pool.Exec(t.Context(), `
INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at)
VALUES ($1, $2, 'fix-code', '{}', 'approved', 'succeeded', $3, $4, $4)`,
		proposal, investigation, "sidekick/"+proposal.String(), now)
	require.NoError(t, err)

	_, err = h.pool.Exec(t.Context(), `
INSERT INTO ci_reports (proposal_id, state, detail, started_at, reported_at)
VALUES ($1, 'failed', $2, $3, $3)`,
		proposal, `{"state":"pending","total":4,"passed":3,"failedCount":1,"failed":[{"name":"unit tests"}]}`, now)
	require.NoError(t, err)
}
