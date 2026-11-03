package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// revertPrefix starts the title of a revert merge request.
const revertPrefix = "Revert: "

// Undo implements action.Undoer. It undoes the merge request that the
// proposal inv.UndoOf made.
//
// A merge request that is not merged is closed and its branch is deleted. One
// that was merged is reverted with a new merge request, which still needs
// review and merging: the revert restores the earlier content of the files
// through the Commits API, as GitHub's does through the Git data API and not
// with GitLab's revert call, so the two hosts behave alike. It is not run
// through the validator, because it restores what was there. Before it opens
// one, it checks that each file on the base branch is still as the merged
// merge request left it. If one is not, the error is an *action.ConflictError
// that carries the files as they are now, and force makes the revert
// overwrite them.
//
// It is safe to repeat: it finds the revert merge request, or the revert
// branch, of an earlier attempt, and closing a closed merge request changes
// nothing.
func (m *MR) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	branch, err := gitlabgit.UndoBranch(inv)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the call to undo: %w", err)
	}

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the parameters of the proposal: %w", err)
	}

	if !m.limits.allowsProject(params.Project) {
		return action.Result{}, fmt.Errorf("project %q is not one of limits.projects (%s): %w", params.Project, strings.Join(m.limits.Projects, ", "), action.ErrLimit)
	}

	orig, err := gitlabgit.MergeRequestFrom(ctx, m.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the merge request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no merge request was made from the branch %s", action.ErrPermanent, branch)
	}

	if gitlabgit.StateOf(orig) == gitchange.StateMerged {
		return m.revert(ctx, inv, params, orig, snap, force)
	}

	res, err := gitlabgit.CloseOriginal(ctx, m.client, m.db, m.metrics.Metrics, inv, params.Project, orig, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("close the merge request to undo: %w", err)
	}

	return res, nil
}

// revert opens a merge request that restores the files of a merged merge
// request to their earlier content.
func (m *MR) revert(ctx context.Context, inv action.Invocation, params Params, orig *gitlabgit.MergeRequest, snap action.Snapshot, force bool) (action.Result, error) {
	branch := inv.IdempotencyKey

	if err := gitlabgit.Record(ctx, m.db, inv.UndoOf, params.Project, gitlabgit.StatusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	mr, branchExists, err := gitlabgit.Existing(ctx, m.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if mr == nil {
		if !branchExists {
			res, done, err := m.pushRevert(ctx, inv, params, snap, force)
			if err != nil {
				return action.Result{}, fmt.Errorf("revert the files: %w", err)
			}

			if done {
				return res, nil
			}
		}

		mr, err = m.openRevert(ctx, params.Project, branch, m.revertRequest(inv, params, orig, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the revert merge request: %w", err)
		}
	} else if err := m.requireDraft(ctx, params.Project, branch, mr); err != nil {
		return action.Result{}, fmt.Errorf("check the revert merge request of an earlier attempt: %w", err)
	}

	if err := gitlabgit.Record(ctx, m.db, inv.ProposalID, params.Project, gitlabgit.StatusOf(mr)); err != nil {
		return action.Result{}, fmt.Errorf("record the revert merge request: %w", err)
	}

	m.metrics.MergeRequest(gitlabgit.ResultReverted)

	title := params.Project + "!" + strconv.Itoa(mr.IID)

	return action.Result{
		Summary: "Opened the revert merge request " + title + ". It still needs review and merging. Its files were not validated, because it restores their earlier content.",
		Links:   []action.Link{{Title: title, URL: mr.WebURL}},
	}, nil
}

// pushRevert checks the files on the base branch against the snapshot and
// writes the commit that restores their earlier content on the revert
// branch. done is true, with the result, when there is nothing to restore.
func (m *MR) pushRevert(ctx context.Context, inv action.Invocation, params Params, snap action.Snapshot, force bool) (res action.Result, done bool, err error) {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the snapshot: %w", err)
	}

	head, err := gitlabgit.BaseOf(ctx, m.client, params.Project, params.BaseBranch)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the base branch: %w", err)
	}

	plan, err := gitlabgit.PlanRevert(ctx, gitlabgit.NewReader(m.client, params.Project, head.Commit), sides, nil)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("plan the revert: %w", err)
	}

	if len(plan.Conflicts) > 0 && !force {
		live, merr := json.Marshal(plan.Live)
		if merr != nil {
			return action.Result{}, false, fmt.Errorf("encode the files as they are now: %w", merr)
		}

		return action.Result{}, false, &action.ConflictError{Live: live}
	}

	if len(plan.Restores) == 0 {
		return action.Result{Summary: "Nothing to revert: the files are as they were before the merge request."}, true, nil
	}

	actions, err := gitlabgit.RestoreActions(ctx, m.client, params.Project, plan.Restores)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the earlier content of the files: %w", err)
	}

	message := revertPrefix + source.CutText(gitlabgit.CommitTitle(params.Title), gitlabgit.MaxCommitTitleBytes-len(revertPrefix)) +
		"\n\nSidekick proposal " + inv.ProposalID + ", which undoes " + inv.UndoOf + "\n"

	_, err = gitlabgit.CommitTo(ctx, m.client, params.Project, gitlabgit.CommitRequest{
		Branch:        inv.IdempotencyKey,
		StartSHA:      head.Commit,
		CommitMessage: message,
		Actions:       actions,
	})
	if err != nil {
		return action.Result{}, false, fmt.Errorf("push the revert branch: %w", err)
	}

	return action.Result{}, false, nil
}

// revertRequest is the merge request that reverts orig. Its text is the
// action's own, apart from the title of the original, so only that is cleaned.
func (m *MR) revertRequest(inv action.Invocation, params Params, orig *gitlabgit.MergeRequest, branch string) gitlabgit.CreateRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Reverts the merge request `%s!%d`, which Sidekick opened for proposal `%s`.\n\n", params.Project, orig.IID, inv.UndoOf)
	b.WriteString("The files were not validated: this change restores their earlier content.\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`, which undoes `%s`.", inv.ProposalID, inv.UndoOf)

	if link := weblink.Investigation(m.publicURL, inv.InvestigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	return gitlabgit.CreateRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(revertPrefix+params.Title, m.limits.draft(), fallbackTitle),
		Description:  b.String(),
	}
}
