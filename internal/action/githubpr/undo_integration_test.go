//go:build integration

package githubpr

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// undoProposal inserts the succeeded proposal that undoes orig, as the
// proposals service stores it, and returns its ID.
func (s *statusBed) undoProposal(t *testing.T, orig uuid.UUID) uuid.UUID {
	t.Helper()

	id := uuid.New()

	_, err := s.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key, proposed_at, expires_at, executed_at, undo_of)
		VALUES ($1, $2, 'monitor-pr', jsonb_build_object('force', false, 'undoOf', $3::text), '', 'approved', 'succeeded', $4, now(), now(), now(), $5)`,
		id, checkoutInvestigation, orig.String(), action.IdempotencyKey(id.String()), orig)
	require.NoError(t, err)

	return id
}

// undoInv returns the invocation of the undo with the ID undoID of the
// proposal orig.
func undoInv(orig, undoID uuid.UUID) action.Invocation {
	inv := invocation(undoParams())
	inv.ProposalID = undoID.String()
	inv.IdempotencyKey = action.IdempotencyKey(undoID.String())
	inv.UndoOf = orig.String()

	return inv
}

func TestUndoRecordsTheStateOfAClosedPullRequest(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)
	orig := s.proposal(t, "monitor-pr", testRepo, s.now.Add(-time.Hour))
	undoID := s.undoProposal(t, orig)

	branch := action.IdempotencyKey(orig.String())
	s.pull(orig, 12, "open", "")
	s.github.Handle(http.MethodPatch, repoPath("pulls/12"), reply(`{"number":12,"state":"closed","html_url":"https://github.com/acme/monitors/pull/12","closed_at":"2026-11-03T11:00:00Z"}`))
	s.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+branch), fakehttp.Response{Status: http.StatusNoContent})

	_, err := s.pr.Undo(t.Context(), undoInv(orig, undoID), undoSnapshot(), false)
	require.NoError(t, err)

	st := s.state(t, orig)
	require.NotNil(t, st)
	require.Equal(t, StateClosed, st.State)
	require.Equal(t, int64(12), st.Number)
	require.NotNil(t, st.ClosedAt)
	require.Equal(t, time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC), st.ClosedAt.UTC())
	require.Nil(t, s.state(t, undoID), "an undo that closed a pull request has no pull request of its own")
}

func TestUndoRecordsTheStatesOfAMergedPullRequestAndItsRevert(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)
	orig := s.proposal(t, "monitor-pr", testRepo, s.now.Add(-time.Hour))
	undoID := s.undoProposal(t, orig)
	closedUndo := s.undoProposal(t, s.proposal(t, "monitor-pr", testRepo, s.now.Add(-time.Hour)))

	s.pull(orig, 12, "closed", "2026-11-03T10:00:00Z")
	s.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+action.IdempotencyKey(undoID.String())), notFound)
	s.expectBase()
	s.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesAfter, blobSHA(testRulesAfter))))
	s.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/new.yaml"), reply(fileBodyWithSHA("prometheus/rules/new.yaml", "x: 1\n", blobSHA("x: 1\n"))))
	s.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/old.yaml"), notFound)
	s.expectPush()
	s.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(30, true)))

	_, err := s.pr.Undo(t.Context(), undoInv(orig, undoID), undoSnapshot(), false)
	require.NoError(t, err)

	o := s.state(t, orig)
	require.NotNil(t, o)
	require.Equal(t, StateMerged, o.State)
	require.NotNil(t, o.MergedAt)

	r := s.state(t, undoID)
	require.NotNil(t, r)
	require.Equal(t, StateOpen, r.State)
	require.Equal(t, int64(30), r.Number)

	rows, err := db.New(s.pool).ListPullRequestsToCheck(t.Context(), db.ListPullRequestsToCheckParams{
		Actions: []string{"monitor-pr"}, Since: time.Now().Add(-trackedFor), Before: time.Now().Add(time.Hour), Batch: maxChecksPerRun,
	})
	require.NoError(t, err)

	got := map[uuid.UUID]string{}
	for _, row := range rows {
		got[row.ID] = row.Repo
	}

	require.Equal(t, testRepo, got[undoID], "the revert pull request is checked, with the repository of the proposal it undoes")
	require.NotContains(t, got, closedUndo, "an undo with no pull request of its own is not checked")
}

func TestUndoRecordsNothingWithoutADatabase(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{"acme:" + testBranch: openPull(12)})
	b.github.Handle(http.MethodPatch, repoPath("pulls/12"), reply(`{"number":12,"state":"closed","html_url":"https://github.com/acme/monitors/pull/12"}`))
	b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})

	_, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
}
