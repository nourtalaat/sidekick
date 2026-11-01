package action

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdempotencyKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, "sidekick/0192d3a4-0000-7000-8000-0000000000a1", IdempotencyKey("0192d3a4-0000-7000-8000-0000000000a1"))
}

func TestProposalStatusFinal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status ProposalStatus
		want   bool
	}{
		{status: StatusProposed},
		{status: StatusApproved},
		{status: StatusExecuting},
		{status: StatusRejected, want: true},
		{status: StatusExpired, want: true},
		{status: StatusSucceeded, want: true},
		{status: StatusFailed, want: true},
		{status: StatusUndone, want: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.status.Final())
		})
	}
}

func TestStaticPolicy(t *testing.T) {
	t.Parallel()

	policy := StaticPolicy{
		"payments": {Mode: Approved, Actions: []string{"monitor-note"}},
		"quiet":    {Mode: ReadOnly, Actions: []string{"monitor-note"}},
		"unset":    {Actions: []string{"monitor-note"}},
	}

	tests := []struct {
		name      string
		route     string
		action    string
		wantFound bool
		wantAllow bool
	}{
		{name: "listed on an approved route", route: "payments", action: "monitor-note", wantFound: true, wantAllow: true},
		{name: "not listed", route: "payments", action: "other", wantFound: true},
		{name: "a read-only route", route: "quiet", action: "monitor-note", wantFound: true},
		{name: "a route with no mode", route: "unset", action: "monitor-note", wantFound: true},
		{name: "a route that does not exist", route: "gone", action: "monitor-note"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, found := policy.Route(tt.route)
			require.Equal(t, tt.wantFound, found)
			require.Equal(t, tt.wantAllow, r.Allows(tt.action))
		})
	}
}
