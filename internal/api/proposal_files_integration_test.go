//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	actionproposals "github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/api"
)

// The pull request proposals that the file tests insert: a succeeded one, and
// the undo of it.
const (
	propPullRequest = "0192d3a4-0000-7000-8000-0000000000f8"
	propPullUndo    = "0192d3a4-0000-7000-8000-0000000000f9"
)

// pullRequestSnapshot is the snapshot of a pull request that changes one
// file, adds one and deletes one.
const pullRequestSnapshot = `{
	"target": "github:acme/monitors",
	"before": {"rules/a.yaml": {"blob": "a1"}, "rules/b.yaml": null, "rules/c.yaml": {"blob": "c1"}},
	"after": {
		"rules/a.yaml": {"blob": "a2", "status": "changed", "linesAdded": 2, "linesRemoved": 1},
		"rules/b.yaml": {"blob": "b2", "status": "added", "linesAdded": 3, "linesRemoved": 0},
		"rules/c.yaml": {"status": "deleted", "linesRemoved": 5}
	},
	"version": "base1"
}`

// pullRequestParams are the parameters that go with pullRequestSnapshot.
const pullRequestParams = `{
	"repo": "acme/monitors", "baseBranch": "main", "title": "Tune rules", "body": "",
	"files": [
		{"path": "rules/a.yaml", "content": "one\ntwo\nthree\n"},
		{"path": "rules/b.yaml", "content": "x\ny\nz\n"},
		{"path": "rules/c.yaml", "delete": true}
	]
}`

// proposalFileBody is a file of a proposal as the API returns it.
type proposalFileBody struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	LinesAdded   int    `json:"linesAdded"`
	LinesRemoved int    `json:"linesRemoved"`
}

// proposalFilesBody is the part of a proposal the file tests read.
type proposalFilesBody struct {
	ID    string             `json:"id"`
	Files []proposalFileBody `json:"files"`
}

// setupFiles returns a server whose payments route proposes the actions, with
// the succeeded pull request proposal and the undo of it.
func setupFiles(t *testing.T, actions map[string]action.Action) server {
	t.Helper()

	names := make([]string, 0, len(actions))
	for name := range actions {
		names = append(names, name)
	}

	policy := action.StaticPolicy{"payments": {Mode: action.Approved, Actions: names}}

	s := setupWith(t, func(o *api.Options) {
		svc, err := actionproposals.New(actionproposals.Options{Pool: o.Pool, Actions: actions, Policy: policy, Redactor: o.Redactor})
		require.NoError(t, err)

		o.Actions = svc
	}, "fixture/proposals.sql")

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, decided_by, decided_at, executed_at, result, snapshot, undoable)
		VALUES ($1::uuid, '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-pr', $2::jsonb, 'Too noisy.', 'approved', 'succeeded',
		        'sidekick/' || $1::uuid::text, now() - interval '2 hours', now() + interval '22 hours',
		        '01920000-0000-7000-8000-000000000070', now() - interval '110 minutes', now() - interval '100 minutes',
		        '{"summary": "Opened a pull request."}', $3::jsonb, true)`, propPullRequest, pullRequestParams, pullRequestSnapshot)
	require.NoError(t, err)

	_, err = s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, decided_by, decided_at, executed_at, result, snapshot, undo_of)
		VALUES ($1::uuid, '0192d3a4-0000-7000-8000-0000000000e2', 'monitor-pr', '{"undoOf": "0192d3a4-0000-7000-8000-0000000000f8"}', '', 'approved', 'succeeded',
		        'sidekick/' || $1::uuid::text, now() - interval '1 hour', now() + interval '23 hours',
		        '01920000-0000-7000-8000-000000000070', now() - interval '50 minutes', now() - interval '40 minutes',
		        '{"summary": "Closed the pull request."}',
		        '{"target": "github:acme/monitors", "before": {"rules/a.yaml": {"blob": "a2", "status": "changed"}}, "after": {"rules/a.yaml": {"blob": "a1"}}}', $2::uuid)`,
		propPullUndo, propPullRequest)
	require.NoError(t, err)

	return s
}

// readFilesProposals returns the proposals of the statuses, by ID.
func readFilesProposals(t *testing.T, s server) map[string]proposalFilesBody {
	t.Helper()

	rec := s.doWithToken(t, http.MethodGet, actionProposalsRoute+"?status=succeeded&limit=10", approverToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list struct {
		Proposals []proposalFilesBody `json:"proposals"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))

	out := make(map[string]proposalFilesBody, len(list.Proposals))
	for _, p := range list.Proposals {
		out[p.ID] = p
	}

	return out
}

func TestProposalListsTheFilesOfAPullRequest(t *testing.T) {
	t.Parallel()

	s := setupFiles(t, map[string]action.Action{"monitor-pr": apiNoteAction{}, "monitor-note": apiNoteAction{}})
	got := readFilesProposals(t, s)

	require.Equal(t, []proposalFileBody{
		{Path: "rules/a.yaml", Status: "changed", LinesAdded: 2, LinesRemoved: 1},
		{Path: "rules/b.yaml", Status: "added", LinesAdded: 3},
		{Path: "rules/c.yaml", Status: "deleted", LinesRemoved: 5},
	}, got[propPullRequest].Files, "the files come from the snapshot, sorted by path")

	require.Empty(t, got[propPullUndo].Files, "an undo has the swapped snapshot, which is not a list of files")
	require.Empty(t, got[propSucceeded].Files, "a proposal that changes no files has none")
}
