//go:build integration

package gitlabmr

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
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
		VALUES ($1, $2, 'monitor-mr', jsonb_build_object('force', false, 'undoOf', $3::text), '', 'approved', 'succeeded', $4, now(), now(), now(), $5)`,
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

func TestUndoRecordsTheStateOfAClosedMergeRequest(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)
	orig := s.proposal(t, "monitor-mr", testProject, s.now.Add(-time.Hour))
	undoID := s.undoProposal(t, orig)

	branch := action.IdempotencyKey(orig.String())
	s.pull(orig, 12, "opened", "")
	s.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(`{"iid":12,"state":"closed","web_url":"`+testWebURL+`12","closed_at":"2026-11-03T11:00:00Z"}`))
	s.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+branch), fakehttp.Response{Status: http.StatusNoContent})

	_, err := s.mr.Undo(t.Context(), undoInv(orig, undoID), undoSnapshot(), false)
	require.NoError(t, err)

	st := s.state(t, orig)
	require.NotNil(t, st)
	require.Equal(t, gitchange.StateClosed, st.State)
	require.Equal(t, int64(12), st.Number)
	require.NotNil(t, st.ClosedAt)
	require.Equal(t, time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC), st.ClosedAt.UTC())
	require.Nil(t, s.state(t, undoID), "an undo that closed a merge request has no merge request of its own")
}

func TestUndoRecordsTheStatesOfAMergedMergeRequestAndItsRevert(t *testing.T) {
	t.Parallel()

	s := newStatusBed(t)
	orig := s.proposal(t, "monitor-mr", testProject, s.now.Add(-time.Hour))
	undoID := s.undoProposal(t, orig)
	closedUndo := s.undoProposal(t, s.proposal(t, "monitor-mr", testProject, s.now.Add(-time.Hour)))

	s.pull(orig, 12, "merged", "2026-11-03T10:00:00Z")
	s.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+action.IdempotencyKey(undoID.String())), notFound)
	s.expectBase()
	s.expectMergedFiles()
	s.expectBeforeBlobs()
	s.expectCommit()
	s.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(30, "opened", true, action.IdempotencyKey(undoID.String()))))

	_, err := s.mr.Undo(t.Context(), undoInv(orig, undoID), undoSnapshot(), false)
	require.NoError(t, err)

	o := s.state(t, orig)
	require.NotNil(t, o)
	require.Equal(t, gitchange.StateMerged, o.State)
	require.NotNil(t, o.MergedAt)

	r := s.state(t, undoID)
	require.NotNil(t, r)
	require.Equal(t, gitchange.StateOpen, r.State)
	require.Equal(t, int64(30), r.Number)

	rows, err := db.New(s.pool).ListPullRequestsToCheck(t.Context(), db.ListPullRequestsToCheckParams{
		Actions: []string{"monitor-mr"}, Since: time.Now().Add(-gitchange.TrackedFor), Before: time.Now().Add(time.Hour), Batch: gitchange.MaxChecksPerRun,
	})
	require.NoError(t, err)

	got := map[uuid.UUID]string{}

	for _, row := range rows {
		var target struct {
			Project string `json:"project"`
		}

		require.NoError(t, json.Unmarshal(row.Params, &target))

		got[row.ID] = target.Project
	}

	require.Equal(t, testProject, got[undoID], "the revert merge request is checked, with the project of the proposal it undoes")
	require.NotContains(t, got, closedUndo, "an undo with no merge request of its own is not checked")
}

func TestUndoRecordsNothingWithoutADatabase(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: openList(12)})
	b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(`{"iid":12,"state":"closed","web_url":"`+testWebURL+`12"}`))
	b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})

	_, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
}
