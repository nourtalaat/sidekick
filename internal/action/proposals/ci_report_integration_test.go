//go:build integration

package proposals

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/queue"
)

func TestSucceededPullRequestStartsACIReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// actionName is the action the proposal is for.
		actionName string
		wantReport bool
	}{
		{name: "an action that reads CI and opened a pull request", actionName: "ci-note", wantReport: true},
		{name: "an action that opened a pull request but cannot read CI", actionName: "pr-note"},
		{name: "an action that opened none", actionName: "monitor-note"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			input := strings.Replace(noteInput, `"monitor-note"`, `"`+tt.actionName+`"`, 1)
			id := h.propose(t, "call-1", input)

			_, err := h.svc.Approve(t.Context(), id, Decider{UserID: anaUser})
			require.NoError(t, err)

			ran, err := h.runJob(t)
			require.True(t, ran)
			require.NoError(t, err)
			require.Equal(t, string(action.StatusSucceeded), h.row(t, id).Status)

			require.Equal(t, tt.wantReport, h.ciReportState(t, id) == "pending")

			var keys []string

			rows, err := h.pool.Query(t.Context(), "SELECT unique_key FROM jobs WHERE kind = $1", queue.KindCIReport)
			require.NoError(t, err)

			keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)

			if tt.wantReport {
				require.Equal(t, []string{"ci_report:" + id.String()}, keys)
			} else {
				require.Empty(t, keys)
			}
		})
	}
}

// ciReportState returns the state of the CI report of the proposal, or ""
// when it has none.
func (h *harness) ciReportState(t *testing.T, id uuid.UUID) string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT state FROM ci_reports WHERE proposal_id = $1", id)
	require.NoError(t, err)

	states, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	if len(states) == 0 {
		return ""
	}

	return states[0]
}
