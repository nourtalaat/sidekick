//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	actionproposals "github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/api"
)

// propMonitor is the succeeded monitor change that the undo tests insert.
const propMonitor = "0192d3a4-0000-7000-8000-0000000000f7"

// liveAfterEdit is the definition when someone changed the threshold after
// the action did.
const liveAfterEdit = `{"options":{"critical":95}}`

// apiMonitorAction is an action that can undo a change. Its Undo reports a
// conflict until a request forces it, when conflict is set.
type apiMonitorAction struct {
	mu       sync.Mutex
	conflict bool
	forced   []bool
}

func (*apiMonitorAction) Spec() action.Spec {
	return action.Spec{Name: "monitor", Description: "Changes a monitor.", ParamsSchema: json.RawMessage(`{"type": "object"}`)}
}

func (*apiMonitorAction) Validate(context.Context, json.RawMessage) error { return nil }

func (*apiMonitorAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

func (a *apiMonitorAction) Undo(_ context.Context, _ action.Invocation, _ action.Snapshot, force bool) (action.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.forced = append(a.forced, force)

	if a.conflict && !force {
		return action.Result{}, &action.ConflictError{Live: json.RawMessage(liveAfterEdit)}
	}

	return action.Result{Summary: "Restored the monitor.", Links: []action.Link{{Title: "Monitor 1", URL: "https://monitors.example.com/1"}}}, nil
}

// undoChange is a change as the API returns it.
type undoChange struct {
	Path   string  `json:"path"`
	Before *string `json:"before"`
	After  *string `json:"after"`
}

// undoProposalBody is the part of a proposal the undo tests read.
type undoProposalBody struct {
	ID       string       `json:"id"`
	Status   string       `json:"status"`
	Target   string       `json:"target"`
	Changes  []undoChange `json:"changes"`
	Undoable bool         `json:"undoable"`
	UndoOf   string       `json:"undoOf"`
	Result   *struct {
		Summary string `json:"summary"`
	} `json:"result"`
}

// undoProblemBody is the problem details of a refused undo.
type undoProblemBody struct {
	Status  int          `json:"status"`
	Code    string       `json:"code"`
	Detail  string       `json:"detail"`
	Changes []undoChange `json:"changes"`
}

// setupUndo returns a server whose payments route proposes monitor and
// monitor-note, with the succeeded, undoable monitor change propMonitor.
func setupUndo(t *testing.T) (server, *apiMonitorAction) {
	t.Helper()

	monitor := &apiMonitorAction{}
	policy := action.StaticPolicy{"payments": {Mode: action.Approved, Actions: []string{"monitor", "monitor-note"}}}

	s := setupWith(t, func(o *api.Options) {
		svc, err := actionproposals.New(actionproposals.Options{
			Pool: o.Pool, Policy: policy, Redactor: o.Redactor,
			Actions: map[string]action.Action{"monitor": monitor, "monitor-note": apiNoteAction{}},
		})
		require.NoError(t, err)

		o.Actions = svc
	}, "fixture/proposals.sql")

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, decided_by, decided_at, executed_at, result, snapshot, undoable)
		VALUES ($1, '0192d3a4-0000-7000-8000-0000000000e2', 'monitor', '{"threshold": 90}', 'Too noisy.', 'approved', 'succeeded',
		        'sidekick/' || $1::text, now() - interval '2 hours', now() + interval '22 hours',
		        '01920000-0000-7000-8000-000000000070', now() - interval '110 minutes', now() - interval '100 minutes',
		        '{"summary": "Changed the monitor."}',
		        '{"target": "fake:monitor/1", "before": {"options": {"critical": 80}}, "after": {"options": {"critical": 90}}}', true)`, propMonitor)
	require.NoError(t, err)

	return s, monitor
}

func readUndoProposal(t *testing.T, body []byte) undoProposalBody {
	t.Helper()

	var p undoProposalBody
	require.NoError(t, json.Unmarshal(body, &p))

	return p
}

func TestProposalShowsItsChangesAndWhetherItCanBeUndone(t *testing.T) {
	t.Parallel()

	s, _ := setupUndo(t)

	rec := s.doWithToken(t, http.MethodGet, actionProposalsRoute+"?status=succeeded&limit=2", approverToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list struct {
		Proposals []undoProposalBody `json:"proposals"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Proposals, 2)

	byID := map[string]undoProposalBody{}
	for _, p := range list.Proposals {
		byID[p.ID] = p
	}

	note := byID[propSucceeded]
	require.Equal(t, propSucceeded, note.ID)
	require.False(t, note.Undoable, "a note can not be undone")
	require.Empty(t, note.Changes)

	monitor := byID[propMonitor]
	require.Equal(t, propMonitor, monitor.ID)
	require.True(t, monitor.Undoable)
	require.Equal(t, "fake:monitor/1", monitor.Target)
	require.Equal(t, []undoChange{{Path: "options.critical", Before: new("80"), After: new("90")}}, monitor.Changes)
}

func TestUndoProposal(t *testing.T) {
	t.Parallel()

	s, monitor := setupUndo(t)
	path := actionProposalsRoute + "/" + propMonitor + "/undo"

	rec := s.do(t, http.MethodPost, path, apSession, apCSRF, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	undo := readUndoProposal(t, rec.Body.Bytes())
	require.Equal(t, "succeeded", undo.Status)
	require.Equal(t, propMonitor, undo.UndoOf)
	require.Equal(t, "Restored the monitor.", undo.Result.Summary)
	require.False(t, undo.Undoable)
	require.Equal(t, []undoChange{{Path: "options.critical", Before: new("90"), After: new("80")}}, undo.Changes)
	require.Equal(t, []bool{false}, monitor.forced)

	var status string
	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT status FROM action_proposals WHERE id = $1", propMonitor).Scan(&status))
	require.Equal(t, "undone", status)

	require.Equal(t, []string{
		"action.undo_requested user 01920000-0000-7000-8000-000000000070",
		"action.succeeded system ",
	}, actionProposalAudit(t, s.pool, undo.ID))
	require.Equal(t, "action.undone user 01920000-0000-7000-8000-000000000070", actionProposalAudit(t, s.pool, propMonitor)[0])

	rec = s.do(t, http.MethodPost, path, apSession, apCSRF, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	var refused undoProblemBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
	require.Equal(t, "not_undoable", refused.Code)
	require.Equal(t, "This proposal cannot be undone: it was undone already.", refused.Detail)
}

func TestUndoProposalShowsTheConflictThenForces(t *testing.T) {
	t.Parallel()

	s, monitor := setupUndo(t)
	monitor.conflict = true
	path := actionProposalsRoute + "/" + propMonitor + "/undo"

	rec := s.do(t, http.MethodPost, path, apSession, apCSRF, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	var conflict undoProblemBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conflict))
	require.Equal(t, "conflict", conflict.Code)
	require.Equal(t, []undoChange{{Path: "options.critical", Before: new("95"), After: new("80")}}, conflict.Changes)

	var status string
	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT status FROM action_proposals WHERE id = $1", propMonitor).Scan(&status))
	require.Equal(t, "succeeded", status, "nothing was restored")

	rec = s.do(t, http.MethodPost, path, apSession, apCSRF, strings.NewReader(`{"force": true}`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "succeeded", readUndoProposal(t, rec.Body.Bytes()).Status)
	require.Equal(t, []bool{false, true}, monitor.forced)
}

func TestUndoProposalRefusals(t *testing.T) {
	t.Parallel()

	s, _ := setupUndo(t)

	tests := []struct {
		name       string
		id         string
		token      string
		wantStatus int
		wantDetail string
		wantCode   string
	}{
		{
			name: "an API token", id: propMonitor, token: approverToken, wantStatus: http.StatusForbidden,
			wantDetail: "An action is undone by a person who is signed in, not by an API token.",
		},
		{name: "a proposal that does not exist", id: propNotThere, wantStatus: http.StatusNotFound, wantDetail: "No action proposal has this ID."},
		{
			name: "a proposal that did not succeed", id: propFirst, wantStatus: http.StatusConflict, wantCode: "not_undoable",
			wantDetail: "This proposal cannot be undone: it is proposed, and only a succeeded proposal can be undone.",
		},
		{
			name: "an action that cannot undo", id: propSucceeded, wantStatus: http.StatusConflict, wantCode: "not_undoable",
			wantDetail: "This proposal cannot be undone: its action cannot undo it.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := actionProposalsRoute + "/" + tt.id + "/undo"

			var rec *httptest.ResponseRecorder
			if tt.token != "" {
				rec = s.doWithToken(t, http.MethodPost, path, tt.token, nil)
			} else {
				rec = s.do(t, http.MethodPost, path, apSession, apCSRF, nil)
			}

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			var got undoProblemBody

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tt.wantDetail, got.Detail)
			require.Equal(t, tt.wantCode, got.Code)
		})
	}

	viewer := s.doWithToken(t, http.MethodPost, actionProposalsRoute+"/"+propMonitor+"/undo", "skt_viewer", nil)
	require.Equal(t, http.StatusForbidden, viewer.Code)
	require.JSONEq(t, problem(http.StatusForbidden, "This needs the approver role.", actionProposalsRoute+"/"+propMonitor+"/undo"), viewer.Body.String())
}
