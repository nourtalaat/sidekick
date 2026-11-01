//go:build integration

package proposals

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/queue"
)

func TestApprove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		by          Decider
		wantUser    *uuid.UUID
		wantSlack   *string
		wantName    string
		wantAuditBy string
	}{
		{name: "a web user", by: Decider{UserID: anaUser}, wantUser: &anaUser, wantName: "Ana", wantAuditBy: "user"},
		{name: "an API token", by: Decider{UserID: ciToken, Token: true}, wantUser: &ciToken, wantName: "ci-approver", wantAuditBy: "token"},
		{name: "a Slack user", by: Decider{SlackUserID: "U01ABCDEF"}, wantSlack: new("U01ABCDEF"), wantAuditBy: "user"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			id := h.propose(t, "call-1", noteInput)
			seq := h.publishSeq(t, checkoutInvestigation)

			got, err := h.svc.Approve(t.Context(), id, tt.by)
			require.NoError(t, err)
			require.Equal(t, action.StatusApproved, got.Status)
			require.Equal(t, tt.wantName, got.DecidedByName)

			row := h.row(t, id)
			require.Equal(t, "approved", row.Status)
			require.Equal(t, tt.wantUser, row.DecidedBy)
			require.Equal(t, tt.wantSlack, row.DecidedBySlackUser)
			require.NotNil(t, row.DecidedAt)

			require.Equal(t, []string{AuditProposed, AuditApproved}, h.auditActions(t, id))
			require.Greater(t, h.publishSeq(t, checkoutInvestigation), seq, "the outputs show the decision")

			jobs := h.jobs(t)
			require.Len(t, jobs, 1)
			require.Equal(t, "action:"+id.String(), *jobs[0].UniqueKey)
			require.JSONEq(t, `{"proposal_id": "`+id.String()+`"}`, string(jobs[0].Payload))
			require.EqualValues(t, executeMaxAttempts, jobs[0].MaxAttempts)
		})
	}
}

func TestReject(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.propose(t, "call-1", noteInput)

	got, err := h.svc.Reject(t.Context(), id, Decider{UserID: anaUser}, "wrong board")
	require.NoError(t, err)
	require.Equal(t, action.StatusRejected, got.Status)
	require.Equal(t, "Ana", got.DecidedByName)

	require.Equal(t, []string{AuditProposed, AuditRejected}, h.auditActions(t, id))
	require.Empty(t, h.jobs(t), "a rejected proposal is never run")

	var details string
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT details::text FROM audit_log WHERE action = $1 AND target_id = $2", AuditRejected, id.String()).Scan(&details))
	require.JSONEq(t, `{"new": {"status": "rejected", "action": "monitor-note", "note": "wrong board"}}`, details)
}

func TestDecideAProposalThatCannotBeDecided(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T, h *harness, id uuid.UUID)
		decide  func(h *harness, id uuid.UUID) error
		wantIs  error
		wantAs  action.ProposalStatus
		wantMsg string
	}{
		{
			name:    "approving after it expired but before the expiry job ran",
			prepare: func(_ *testing.T, h *harness, _ uuid.UUID) { h.clock.advance(action.DefaultProposalTTL) },
			decide: func(h *harness, id uuid.UUID) error {
				_, err := h.svc.Approve(context.Background(), id, Decider{UserID: anaUser})
				return err
			},
			wantIs: ErrExpired,
		},
		{
			name:    "rejecting after it expired",
			prepare: func(_ *testing.T, h *harness, _ uuid.UUID) { h.clock.advance(action.DefaultProposalTTL) },
			decide: func(h *harness, id uuid.UUID) error {
				_, err := h.svc.Reject(context.Background(), id, Decider{UserID: anaUser}, "")
				return err
			},
			wantIs: ErrExpired,
		},
		{
			name: "approving twice",
			prepare: func(t *testing.T, h *harness, id uuid.UUID) {
				_, err := h.svc.Approve(t.Context(), id, Decider{UserID: anaUser})
				require.NoError(t, err)
			},
			decide: func(h *harness, id uuid.UUID) error {
				_, err := h.svc.Approve(context.Background(), id, Decider{SlackUserID: "U01ABCDEF"})
				return err
			},
			wantAs: action.StatusApproved,
		},
		{
			name: "approving after a rejection",
			prepare: func(t *testing.T, h *harness, id uuid.UUID) {
				_, err := h.svc.Reject(t.Context(), id, Decider{UserID: anaUser}, "")
				require.NoError(t, err)
			},
			decide: func(h *harness, id uuid.UUID) error {
				_, err := h.svc.Approve(context.Background(), id, Decider{UserID: ciToken, Token: true})
				return err
			},
			wantAs: action.StatusRejected,
		},
		{
			name: "approving after the route became read-only",
			prepare: func(_ *testing.T, h *harness, _ uuid.UUID) {
				h.policy.set("payments", action.RoutePolicy{Mode: action.ReadOnly})
			},
			decide: func(h *harness, id uuid.UUID) error {
				_, err := h.svc.Approve(context.Background(), id, Decider{UserID: anaUser})
				return err
			},
			wantIs: ErrNotAllowed, wantMsg: `route "payments" is read-only`,
		},
		{
			name: "approving after the action left the route",
			prepare: func(_ *testing.T, h *harness, _ uuid.UUID) {
				h.policy.set("payments", action.RoutePolicy{Mode: action.Approved, Actions: []string{"other"}})
			},
			decide: func(h *harness, id uuid.UUID) error {
				_, err := h.svc.Approve(context.Background(), id, Decider{UserID: anaUser})
				return err
			},
			wantIs: ErrNotAllowed, wantMsg: `does not list action "monitor-note"`,
		},
		{
			name:    "a proposal that does not exist",
			prepare: func(*testing.T, *harness, uuid.UUID) {},
			decide: func(h *harness, _ uuid.UUID) error {
				_, err := h.svc.Approve(context.Background(), uuid.New(), Decider{UserID: anaUser})
				return err
			},
			wantIs: ErrNotFound,
		},
		{
			name:    "a decision from nobody",
			prepare: func(*testing.T, *harness, uuid.UUID) {},
			decide: func(h *harness, id uuid.UUID) error {
				_, err := h.svc.Approve(context.Background(), id, Decider{})
				return err
			},
			wantIs: errNoDecider,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			id := h.propose(t, "call-1", noteInput)
			tt.prepare(t, h, id)

			err := tt.decide(h, id)
			require.Error(t, err)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}

			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
			}

			if tt.wantAs != "" {
				var decided *DecidedError
				require.True(t, errors.As(err, &decided))
				require.Equal(t, tt.wantAs, decided.Proposal.Status)
				require.NotEmpty(t, decided.Proposal.DecidedByName+decided.Proposal.DecidedBySlackUser, "it says who decided")
			}

			if row := h.row(t, id); row.Status == "proposed" || row.Status == "expired" {
				require.Empty(t, h.jobs(t), "nothing runs")
			}
		})
	}
}

// TestTwoApproversAtOnce lets many people approve and reject the same
// proposal at the same time: exactly one wins, the others learn who did,
// and one job is queued.
func TestTwoApproversAtOnce(t *testing.T) {
	t.Parallel()

	const deciders = 12

	h := setup(t)
	id := h.propose(t, "call-1", noteInput)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		lost    []*DecidedError
	)

	for i := range deciders {
		wg.Go(func() {
			by := Decider{SlackUserID: "U0" + string(rune('A'+i))}

			var err error
			if i%3 == 0 {
				_, err = h.svc.Reject(t.Context(), id, by, "")
			} else {
				_, err = h.svc.Approve(t.Context(), id, by)
			}

			mu.Lock()
			defer mu.Unlock()

			var decided *DecidedError

			switch {
			case err == nil:
				winners++
			case errors.As(err, &decided):
				lost = append(lost, decided)
			default:
				t.Errorf("a decider failed: %v", err)
			}
		})
	}

	wg.Wait()

	require.Equal(t, 1, winners)
	require.Len(t, lost, deciders-1)

	row := h.row(t, id)
	require.NotNil(t, row.DecidedBySlackUser)

	for _, l := range lost {
		require.Equal(t, action.ProposalStatus(row.Status), l.Proposal.Status)
		require.Equal(t, *row.DecidedBySlackUser, l.Proposal.DecidedBySlackUser, "the others are told who won")
	}

	audited := h.auditActions(t, id)
	require.Len(t, audited, 2, "one proposed and one decision")

	if row.Status == "approved" {
		require.Len(t, h.jobs(t), 1)
	} else {
		require.Empty(t, h.jobs(t))
	}
}

func TestApproveQueuesOneJob(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.propose(t, "call-1", noteInput)

	tx, err := h.pool.Begin(t.Context())
	require.NoError(t, err)

	require.NoError(t, enqueueExecute(t.Context(), tx, id))
	require.NoError(t, enqueueExecute(t.Context(), tx, id), "a job that waits already is enough")
	require.NoError(t, tx.Commit(t.Context()))

	jobs := h.jobs(t)
	require.Len(t, jobs, 1)
	require.Equal(t, queue.StatusPending, jobs[0].Status)
}
