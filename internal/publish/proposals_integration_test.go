//go:build integration

package publish

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// TestUpdatesShowTheProposals checks that every update has the action
// proposals the Proposals option returns for its investigation, in every
// phase, and that a refresh shows a change to them.
func TestUpdatesShowTheProposals(t *testing.T) {
	t.Parallel()

	h, sink := setupRecording(t, []output.Phase{output.PhaseQueued, output.PhaseFinal})

	proposals := []action.Proposal{{ID: "p1", Action: "monitor-note", Status: action.StatusProposed, ParamsText: "{}"}}

	var asked []uuid.UUID

	h.handler.proposals = func(_ context.Context, _ db.DBTX, id uuid.UUID) ([]action.Proposal, error) {
		asked = append(asked, id)

		return proposals, nil
	}

	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))

	proposals = []action.Proposal{{ID: "p1", Action: "monitor-note", Status: action.StatusApproved, ParamsText: "{}"}}

	h.inTx(t, func(tx pgx.Tx) error { return Refresh(t.Context(), tx, webhookInvestigation) })
	require.NoError(t, h.run(t))

	h.enqueue(t, Update{
		InvestigationID: webhookInvestigation,
		Phase:           output.PhaseFinal,
		Seq:             1,
		Report:          &report.Report{Verdict: report.TruePositive, Confidence: report.High, Reason: "Deploy 4f2c1a broke checkout."},
	})
	require.NoError(t, h.run(t))

	updates := sink.all()
	require.Len(t, updates, 3)
	require.Equal(t, action.StatusProposed, updates[0].Proposals[0].Status)
	require.Equal(t, action.StatusApproved, updates[1].Proposals[0].Status, "a refresh shows the change")
	require.Equal(t, output.PhaseFinal, updates[2].Phase)
	require.Equal(t, action.StatusApproved, updates[2].Proposals[0].Status)
	require.Equal(t, []uuid.UUID{webhookInvestigation, webhookInvestigation, webhookInvestigation}, asked)
}
