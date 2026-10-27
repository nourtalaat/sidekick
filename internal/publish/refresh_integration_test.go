//go:build integration

package publish

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

// refresh is the stand-in for a Refresh among the writes of a test.
var refresh = Update{}

func TestRefreshAndUpdates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// writes run in order after Open; refresh stands for a Refresh.
		writes        []Update
		wantSeq       int64
		wantUpdateSeq int64
		wantPhase     output.Phase
	}{
		{
			name:          "a refresh of the queued state",
			writes:        []Update{refresh},
			wantSeq:       1,
			wantUpdateSeq: SeqQueued,
			wantPhase:     output.PhaseQueued,
		},
		{
			name:          "an update after a refresh is still newer",
			writes:        []Update{refresh, running(1)},
			wantSeq:       2,
			wantUpdateSeq: 1,
			wantPhase:     output.PhaseRunning,
		},
		{
			name:          "an update above the refreshed seq keeps its own seq",
			writes:        []Update{running(3), refresh, running(7)},
			wantSeq:       7,
			wantUpdateSeq: 7,
			wantPhase:     output.PhaseRunning,
		},
		{
			name:          "a stale update after a refresh is still dropped",
			writes:        []Update{running(3), refresh, running(2)},
			wantSeq:       4,
			wantUpdateSeq: 3,
			wantPhase:     output.PhaseRunning,
		},
		{
			name: "the final update wins over refreshes and later steps",
			writes: []Update{
				running(3), refresh, refresh,
				{InvestigationID: webhookInvestigation, Phase: output.PhaseFinal, Seq: 4},
				running(4), refresh,
			},
			wantSeq:       7,
			wantUpdateSeq: 4,
			wantPhase:     output.PhaseFinal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			h.open(t, webhookInvestigation, slackSink)

			for _, u := range tt.writes {
				if u.Seq == 0 {
					h.refresh(t)

					continue
				}

				h.enqueue(t, u)
			}

			state := h.state(t, webhookInvestigation)
			require.Equal(t, tt.wantSeq, state.Seq)
			require.Equal(t, &tt.wantUpdateSeq, state.UpdateSeq)
			require.Equal(t, string(tt.wantPhase), state.Phase)
			require.Len(t, h.jobs(t), 1, "a waiting job is kept, not doubled")
		})
	}
}

func TestRefreshWithoutAState(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.inTx(t, func(tx pgx.Tx) error { return Refresh(t.Context(), tx, slackInvestigation) })

	require.Empty(t, h.jobs(t))
}

func TestRefreshEditsTheMessage(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))

	h.refresh(t)
	require.NoError(t, h.run(t))

	require.Len(t, h.slack.callsTo("chat.postMessage"), 1)
	require.Len(t, h.slack.callsTo("chat.update"), 1)
	require.Equal(t, int64(1), h.outputRef(t, webhookInvestigation, slackSink).LastSeq)
}

// refresh calls Refresh for the webhook investigation in its own
// transaction.
func (h *harness) refresh(t *testing.T) {
	t.Helper()

	h.inTx(t, func(tx pgx.Tx) error { return Refresh(t.Context(), tx, webhookInvestigation) })
}

// running is a running update of the webhook investigation with seq.
func running(seq int64) Update {
	return Update{InvestigationID: webhookInvestigation, Phase: output.PhaseRunning, Seq: seq}
}
