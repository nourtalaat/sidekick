package gitlabgit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// UndoBranch checks the invocation of an undo and returns the branch of the
// proposal that is undone.
func UndoBranch(inv action.Invocation) (string, error) {
	if inv.UndoOf == "" {
		return "", ErrNoUndoOf
	}

	branch := action.IdempotencyKey(inv.UndoOf)
	if !BranchOfProposal.MatchString(branch) || !BranchOfProposal.MatchString(inv.IdempotencyKey) {
		return "", fmt.Errorf("%w: %w", action.ErrPermanent, ErrNoBranch)
	}

	return branch, nil
}

// CloseOriginal is the undo of a merge request that was not merged. It closes
// the merge request orig of project, if it is open, deletes its branch and
// records it as closed for the proposal inv.UndoOf. Closing a closed merge
// request changes nothing, so it is safe to repeat. dbtx may be nil, as for
// Record; m counts the undo as closed.
func CloseOriginal(ctx context.Context, client *gitlabapi.Client, dbtx db.DBTX, m *Metrics, inv action.Invocation, project string, orig *MergeRequest, branch string) (action.Result, error) {
	title := project + "!" + strconv.Itoa(orig.IID)
	closed, summary := orig, "The merge request "+title+" was closed already; its branch is deleted."

	if StateOf(orig) == gitchange.StateOpen {
		updated, err := CloseMergeRequest(ctx, client, project, orig.IID)
		if err != nil {
			return action.Result{}, fmt.Errorf("close the original: %w", err)
		}

		closed, summary = updated, "Closed the merge request "+title+" and deleted its branch."
	}

	if err := DeleteBranch(ctx, client, project, branch); err != nil {
		return action.Result{}, fmt.Errorf("remove the branch of the closed merge request: %w", err)
	}

	st := StatusOf(closed)
	if st.State == gitchange.StateOpen {
		st.State, st.ClosedAt = gitchange.StateClosed, new(time.Now())
	}

	if st.Number == 0 {
		st.Number, st.URL = orig.IID, orig.WebURL
	}

	if err := Record(ctx, dbtx, inv.UndoOf, project, st); err != nil {
		return action.Result{}, fmt.Errorf("record the original as %s: %w", st.State, err)
	}

	m.MergeRequest(ResultClosed)

	return action.Result{Summary: summary, Links: []action.Link{{Title: title, URL: orig.WebURL}}}, nil
}

// Record keeps the state of a merge request for the proposal with the ID, for
// the count of merged merge requests. Where there is no database, dbtx is nil
// and it does nothing.
func Record(ctx context.Context, dbtx db.DBTX, proposalID, project string, st *gitchange.Status) error {
	if dbtx == nil {
		return nil
	}

	if err := gitchange.RecordState(ctx, dbtx, proposalID, project, st); err != nil {
		return fmt.Errorf("record the merge request state: %w", err)
	}

	return nil
}
