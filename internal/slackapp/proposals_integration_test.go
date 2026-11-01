//go:build integration

package slackapp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// noteAction is an action the proposal tests never run.
type noteAction struct{}

func (noteAction) Spec() action.Spec {
	return action.Spec{Name: "monitor-note", Description: "Writes a note.", ParamsSchema: json.RawMessage(`{"type": "object"}`)}
}

func (noteAction) Validate(context.Context, json.RawMessage) error { return nil }

func (noteAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

// proposalService returns a proposal service for the default route, which
// proposes monitor-note in approved mode. now, when set, is its clock.
func proposalService(t *testing.T, b *buttonsHarness, policy action.Policy, now func() time.Time) *proposals.Service {
	t.Helper()

	svc, err := proposals.New(proposals.Options{
		Pool: b.pool, Actions: map[string]action.Action{"monitor-note": noteAction{}}, Policy: policy, Now: now,
	})
	require.NoError(t, err)

	return svc
}

var approvedDefault = action.StaticPolicy{"default": {Mode: action.Approved, Actions: []string{"monitor-note"}}}

// propose makes a proposal on the final investigation and returns its ID.
func (b *buttonsHarness) propose(t *testing.T) uuid.UUID {
	t.Helper()

	res, err := b.buttons.actions.Propose(t.Context(), proposals.ProposeRequest{
		InvestigationID: finalInvestigation, ToolCallID: "call-1", Route: "default", Mode: action.Approved,
		Input: json.RawMessage(`{"action": "monitor-note", "params": {"title": "Slow checkout"}, "reason": "p99 doubled."}`),
	})
	require.NoError(t, err)
	require.Empty(t, res.Refusal)

	return res.ID
}

// proposalRow returns the proposal with id.
func (b *buttonsHarness) proposalRow(t *testing.T, id uuid.UUID) db.ActionProposal {
	t.Helper()

	row, err := db.New(b.pool).GetActionProposal(t.Context(), id)
	require.NoError(t, err)

	return row
}

func TestProposalButtons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// role is the role every user has.
		role roles.Role
		// prepare changes the setting after the proposal is made, and may
		// return the service the click goes to.
		prepare    func(t *testing.T, b *buttonsHarness, id uuid.UUID)
		action     string
		user       string
		wantNotes  []string
		wantStatus string
		wantJobs   int
	}{
		{
			name: "an approver approves", role: roles.Approver, action: slackout.ActionApproveProposal, user: "U01APPR",
			wantNotes: []string{noteApproved}, wantStatus: "approved", wantJobs: 1,
		},
		{
			name: "an admin approves", role: roles.Admin, action: slackout.ActionApproveProposal, user: "U01ADMIN",
			wantNotes: []string{noteApproved}, wantStatus: "approved", wantJobs: 1,
		},
		{
			name: "an approver rejects", role: roles.Approver, action: slackout.ActionRejectProposal, user: "U01APPR",
			wantNotes: []string{noteRejected}, wantStatus: "rejected",
		},
		{
			name: "a responder cannot approve", role: roles.Responder, action: slackout.ActionApproveProposal, user: "U01ANA",
			wantNotes: []string{noteNotApprover}, wantStatus: "proposed",
		},
		{
			name: "a responder cannot reject", role: roles.Responder, action: slackout.ActionRejectProposal, user: "U01ANA",
			wantNotes: []string{noteNotApprover}, wantStatus: "proposed",
		},
		{
			name: "an expired proposal cannot be approved", role: roles.Approver, action: slackout.ActionApproveProposal, user: "U01APPR",
			prepare: func(t *testing.T, b *buttonsHarness, _ uuid.UUID) {
				t.Helper()

				b.buttons.actions = proposalService(t, b, approvedDefault, func() time.Time { return time.Now().Add(48 * time.Hour) })
			},
			wantNotes: []string{noteExpired}, wantStatus: "proposed",
		},
		{
			name: "a route that is read-only now", role: roles.Approver, action: slackout.ActionApproveProposal, user: "U01APPR",
			prepare: func(t *testing.T, b *buttonsHarness, _ uuid.UUID) {
				t.Helper()

				b.buttons.actions = proposalService(t, b, action.StaticPolicy{"default": {Mode: action.ReadOnly}}, nil)
			},
			wantNotes: []string{`The config does not allow this any more: route "default" is read-only. Reject it instead.`}, wantStatus: "proposed",
		},
		{
			name: "a proposal decided a moment ago by someone else", role: roles.Approver, action: slackout.ActionApproveProposal, user: "U01APPR",
			prepare: func(t *testing.T, b *buttonsHarness, id uuid.UUID) {
				t.Helper()

				_, err := b.buttons.actions.Reject(t.Context(), id, proposals.Decider{SlackUserID: "U01ANA"}, "")
				require.NoError(t, err)
			},
			wantNotes: []string{"This proposal is rejected already; <@U01ANA> decided it."}, wantStatus: "rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := setupButtons(t, tt.role)
			b.buttons.actions = proposalService(t, b, approvedDefault, nil)

			id := b.propose(t)

			if tt.prepare != nil {
				tt.prepare(t, b, id)
			}

			require.Equal(t, http.StatusOK, b.click(t, clickBy{user: tt.user, action: tt.action, id: id}, false))
			require.Equal(t, tt.wantNotes, b.notes(t))
			require.Equal(t, tt.wantStatus, b.proposalRow(t, id).Status)

			var jobs int
			require.NoError(t, b.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1", queue.KindExecuteAction).Scan(&jobs))
			require.Equal(t, tt.wantJobs, jobs)
		})
	}
}

func TestProposalButtonsRecordWhoDecided(t *testing.T) {
	t.Parallel()

	b := setupButtons(t, roles.Approver)
	b.buttons.actions = proposalService(t, b, approvedDefault, nil)

	id := b.propose(t)
	seq := b.publishSeq(t, finalInvestigation)

	require.Equal(t, http.StatusOK, b.click(t, clickBy{user: "U01APPR", action: slackout.ActionApproveProposal, id: id}, false))

	row := b.proposalRow(t, id)
	require.Equal(t, new("U01APPR"), row.DecidedBySlackUser)
	require.Nil(t, row.DecidedBy)
	require.Greater(t, b.publishSeq(t, finalInvestigation), seq, "the message is shown again without the buttons")

	var actor string
	require.NoError(t, b.pool.QueryRow(t.Context(), "SELECT actor_kind || ' ' || actor_id FROM audit_log WHERE action = 'action.approved' AND target_id = $1", id.String()).Scan(&actor))
	require.Equal(t, "user U01APPR", actor)
}

func TestAProposalThatDoesNotExist(t *testing.T) {
	t.Parallel()

	b := setupButtons(t, roles.Approver)
	b.buttons.actions = proposalService(t, b, approvedDefault, nil)

	require.Equal(t, http.StatusOK, b.click(t, clickBy{user: "U01APPR", action: slackout.ActionApproveProposal, id: uuid.New()}, false))
	require.Equal(t, []string{noteNoProposal}, b.notes(t))
}
