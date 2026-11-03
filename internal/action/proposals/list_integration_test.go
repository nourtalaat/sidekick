//go:build integration

package proposals

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// listed is a proposal that TestListByAction stores: its action and status,
// and how long ago it was proposed.
type listed struct {
	action string
	status action.ProposalStatus
	ago    time.Duration
}

func TestListByAction(t *testing.T) {
	t.Parallel()

	h := setup(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Newest first: the ids run from the newest to the oldest.
	stored := []listed{
		{"monitor-note", action.StatusProposed, 1 * time.Hour},
		{"monitor", action.StatusFailed, 2 * time.Hour},
		{"monitor-note", action.StatusSucceeded, 3 * time.Hour},
		{"monitor-note", action.StatusFailed, 4 * time.Hour},
		{"monitor", action.StatusSucceeded, 5 * time.Hour},
		{"monitor-note", action.StatusFailed, 6 * time.Hour},
	}
	ids := make([]uuid.UUID, len(stored))

	for i, p := range stored {
		ids[i] = uuid.New()

		_, err := h.pool.Exec(t.Context(), `
			INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at)
			VALUES ($1, $2, $3, '{}', 'approved', $4, $5, $6, $7)`,
			ids[i], checkoutInvestigation, p.action, string(p.status), "sidekick/"+ids[i].String(), now.Add(-p.ago), now.Add(time.Hour))
		require.NoError(t, err)
	}

	tests := []struct {
		name string
		opts ListOptions
		want []int
	}{
		{name: "no action lists every action", opts: ListOptions{Statuses: []action.ProposalStatus{action.StatusFailed}, PageSize: 10}, want: []int{1, 3, 5}},
		{name: "one action and one status", opts: ListOptions{Statuses: []action.ProposalStatus{action.StatusFailed}, Action: "monitor-note", PageSize: 10}, want: []int{3, 5}},
		{name: "one action and several statuses", opts: ListOptions{Statuses: []action.ProposalStatus{action.StatusFailed, action.StatusSucceeded, action.StatusProposed}, Action: "monitor-note", PageSize: 10}, want: []int{0, 2, 3, 5}},
		{name: "an action nothing has", opts: ListOptions{Statuses: []action.ProposalStatus{action.StatusFailed}, Action: "slack-react", PageSize: 10}, want: []int{}},
		{name: "a page of an action across statuses", opts: ListOptions{Statuses: []action.ProposalStatus{action.StatusFailed, action.StatusSucceeded}, Action: "monitor-note", PageSize: 2}, want: []int{2, 3}},
		{
			name: "the page after a cursor keeps the action",
			opts: ListOptions{Statuses: []action.ProposalStatus{action.StatusFailed, action.StatusSucceeded}, Action: "monitor-note", After: Cursor{ProposedAt: now.Add(-3 * time.Hour), ID: ids[2]}, PageSize: 2},
			want: []int{3, 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := List(t.Context(), h.pool, tt.opts)
			require.NoError(t, err)

			gotIDs := make([]string, len(got))
			for i, v := range got {
				gotIDs[i] = v.ID
			}

			wantIDs := make([]string, len(tt.want))
			for i, n := range tt.want {
				wantIDs[i] = ids[n].String()
			}

			require.Equal(t, wantIDs, gotIDs)
		})
	}
}
