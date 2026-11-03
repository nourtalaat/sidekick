//go:build integration

package proposals

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestPropose(t *testing.T) {
	t.Parallel()

	h := setup(t)
	before := h.publishSeq(t, checkoutInvestigation)

	id := h.propose(t, "call-1", noteInput)

	row := h.row(t, id)
	require.Equal(t, "proposed", row.Status)
	require.Equal(t, "monitor-note", row.Action)
	require.JSONEq(t, `{"board":"ops","title":"Slow checkout"}`, string(row.Params))
	require.Equal(t, "p99 doubled after the deploy.", row.Reason)
	require.Equal(t, "approved", row.Mode)
	require.Equal(t, "sidekick/"+id.String(), row.IdempotencyKey)
	require.Equal(t, row.ProposedAt.Add(action.DefaultProposalTTL), row.ExpiresAt)

	require.Equal(t, []string{AuditProposed}, h.auditActions(t, id))
	require.Greater(t, h.publishSeq(t, checkoutInvestigation), before, "the outputs show the proposal")

	proposals, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
	require.NoError(t, err)
	require.Len(t, proposals, 1)
	require.Equal(t, action.StatusProposed, proposals[0].Status)
	require.Equal(t, "payments", proposals[0].Route)
	require.Contains(t, proposals[0].ParamsText, `"title": "Slow checkout"`)
}

func TestProposeStoresTheSnapshot(t *testing.T) {
	t.Parallel()

	h := setup(t)

	id := h.propose(t, "call-1", monitorInput)

	row := h.row(t, id)
	require.True(t, row.Undoable)
	require.Nil(t, row.UndoOf)

	var snap action.Snapshot

	require.NoError(t, json.Unmarshal(row.Snapshot, &snap))
	require.Equal(t, "fake:monitor/1", snap.Target)
	require.JSONEq(t, startMonitor, string(snap.Before))

	proposals, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
	require.NoError(t, err)
	require.Len(t, proposals, 1)
	require.Equal(t, []action.Change{{Path: "options.critical", Before: new("80"), After: new("90")}}, proposals[0].Changes)
	require.False(t, proposals[0].Undoable, "only a succeeded proposal can be undone")

	var details struct {
		New struct {
			Snapshot action.Snapshot `json:"snapshot"`
		} `json:"new"`
	}

	var raw string
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT details::text FROM audit_log WHERE action = $1 AND target_id = $2", AuditProposed, id.String()).Scan(&raw))
	require.NoError(t, json.Unmarshal([]byte(raw), &details))
	require.Equal(t, "fake:monitor/1", details.New.Snapshot.Target, "the audit entry keeps the before and after")

	plain := h.row(t, h.propose(t, "call-2", noteInput))
	require.Nil(t, plain.Snapshot)
	require.False(t, plain.Undoable)
}

func TestProposeStoresTheFixKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        string
		want         *string
		wantProposal action.FixKind
	}{
		{name: "an action that fixes through an API", input: fixAPIInput, want: new("api"), wantProposal: action.FixAPI},
		{name: "an action that fixes through a pull request", input: fixPullRequestInput, want: new("pull_request"), wantProposal: action.FixPullRequest},
		{name: "a monitor change that fixes nothing", input: monitorInput},
		{name: "an action that is not a monitor", input: noteInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			row := h.row(t, h.propose(t, "call-1", tt.input))
			require.Equal(t, tt.want, row.FixKind)

			proposals, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
			require.NoError(t, err)
			require.Len(t, proposals, 1)
			require.Equal(t, tt.wantProposal, proposals[0].FixKind)
		})
	}
}

func TestProposeRefusesWhatTheActionRefuses(t *testing.T) {
	t.Parallel()

	h := setup(t)

	res, err := h.svc.Propose(t.Context(), ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: "call-1", Route: "payments", Mode: action.Approved,
		Input: json.RawMessage(`{"action": "monitor", "params": {"threshold": 90, "message": "x"}, "reason": "x"}`),
	})
	require.NoError(t, err)
	require.Contains(t, res.Refusal, `the field "message" is not one of limits.fields`)
	require.Zero(t, countRows(t, h, "SELECT count(*) FROM action_proposals"))
}

func TestProposeRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		route       string
		mode        action.Mode
		input       string
		change      func(h *harness)
		wantRefusal string
	}{
		{name: "an action outside the limits", input: `{"action": "monitor-note", "params": {"board": "finance", "title": "t"}, "reason": "x"}`, wantRefusal: "outside configured limits"},
		{name: "a read-only investigation", mode: action.ReadOnly, input: noteInput, wantRefusal: "read-only"},
		{
			name: "a route changed to read-only since the investigation started", input: noteInput,
			change:      func(h *harness) { h.policy.set("payments", action.RoutePolicy{Mode: action.ReadOnly}) },
			wantRefusal: `route "payments" is read-only`,
		},
		{
			name: "an action taken off the route since the investigation started", input: noteInput,
			change: func(h *harness) {
				h.policy.set("payments", action.RoutePolicy{Mode: action.Approved, Actions: []string{"other"}})
			},
			wantRefusal: `route "payments" does not list action "monitor-note"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			if tt.change != nil {
				tt.change(h)
			}

			mode := cmpMode(tt.mode)

			res, err := h.svc.Propose(t.Context(), ProposeRequest{
				InvestigationID: checkoutInvestigation, ToolCallID: "call-1", Route: "payments", Mode: mode, Input: json.RawMessage(tt.input),
			})
			require.NoError(t, err)
			require.Contains(t, res.Refusal, tt.wantRefusal)

			require.Zero(t, countRows(t, h, "SELECT count(*) FROM action_proposals"), "a refused call stores nothing")
			require.Empty(t, h.jobs(t))
		})
	}
}

func TestProposeIsRepeatable(t *testing.T) {
	t.Parallel()

	h := setup(t)
	svc := h.service(t, Options{MaxPerInvestigation: 1})
	h.svc = svc

	first := h.propose(t, "call-1", noteInput)
	again := h.propose(t, "call-1", noteInput)

	require.Equal(t, first, again, "a call repeated after a crash finds its proposal, even at the cap")

	require.EqualValues(t, 1, countRows(t, h, "SELECT count(*) FROM action_proposals"))
	require.Equal(t, []string{AuditProposed}, h.auditActions(t, first), "and writes its audit entry once")

	res, err := svc.Propose(t.Context(), ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: "call-2", Route: "payments", Mode: action.Approved, Input: json.RawMessage(noteInput),
	})
	require.NoError(t, err)
	require.Contains(t, res.Refusal, "the most it may make")
}

func TestProposeLetsTheProposalExpireAfterTheTTL(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.svc = h.service(t, Options{TTL: 2 * time.Hour})

	row := h.row(t, h.propose(t, "call-1", noteInput))
	require.Equal(t, 2*time.Hour, row.ExpiresAt.Sub(row.ProposedAt))
}

func TestStoredParamsCannotChange(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.propose(t, "call-1", noteInput)

	for _, column := range []string{
		"params = '{}'", "action = 'other'", "reason = 'x'", "idempotency_key = 'x'", "expires_at = now() + interval '1 year'",
		"snapshot = '{}'", "undoable = true", "undo_of = id", "fix_kind = 'api'",
	} {
		_, err := h.pool.Exec(t.Context(), "UPDATE action_proposals SET "+column+" WHERE id = $1", id)
		require.ErrorContains(t, err, "what an action proposal asks for cannot be changed", column)
	}

	_, err := h.pool.Exec(t.Context(), "UPDATE action_proposals SET error = 'x' WHERE id = $1", id)
	require.NoError(t, err, "the other columns change")
}

func TestFixKindCannotChange(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.propose(t, "call-1", fixAPIInput)

	for _, column := range []string{"fix_kind = NULL", "fix_kind = 'pull_request'"} {
		_, err := h.pool.Exec(t.Context(), "UPDATE action_proposals SET "+column+" WHERE id = $1", id)
		require.ErrorContains(t, err, "what an action proposal asks for cannot be changed", column)
	}

	_, err := h.pool.Exec(t.Context(), "INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at, fix_kind) "+
		"VALUES (gen_random_uuid(), $1, 'x', '{}', 'approved', 'proposed', 'key-x', now(), now(), 'mute')", checkoutInvestigation)
	require.ErrorContains(t, err, "action_proposals_fix_kind_check")
}

// cmpMode returns m, or approved for the zero value.
func cmpMode(m action.Mode) action.Mode {
	if m == "" {
		return action.Approved
	}

	return m
}

// countRows returns the number the query counts.
func countRows(t *testing.T, h *harness, query string) int64 {
	t.Helper()

	var n int64
	require.NoError(t, h.pool.QueryRow(t.Context(), query).Scan(&n))

	return n
}
