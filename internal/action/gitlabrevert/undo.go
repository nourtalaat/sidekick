package gitlabrevert

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

var _ action.Undoer = (*Revert)(nil)

// undoPrefix starts the title of the merge request that puts a revert back.
const undoPrefix = "Undo: "

// undoTitleFallback is the title of that merge request when the model's title is
// nothing but a draft prefix.
const undoTitleFallback = "Undo the revert"

// Undo implements action.Undoer. It undoes the merge request that the proposal
// inv.UndoOf made.
//
// A merge request that is not merged is closed and its branch is deleted. One
// that was merged is undone with a new draft merge request that puts back what
// the commit wrote, so the change the revert took out is back. It still needs
// review and merging. Before it opens one, it checks that each file on the base
// branch is still as the revert left it. If one is not, the error is an
// *action.ConflictError that carries the files as they are now, and force makes
// the merge request overwrite them.
//
// It is safe to repeat: it finds the branch and merge request of an earlier
// attempt, and closing a closed merge request changes nothing.
func (r *Revert) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	branch, err := gitlabgit.UndoBranch(inv)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the call to undo: %w", err)
	}

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the parameters of the proposal: %w", err)
	}

	if !gitlabgit.AllowsProject(r.limits.Projects, params.Project) {
		return action.Result{}, fmt.Errorf("project %q is not one of limits.projects (%s): %w", params.Project, strings.Join(r.limits.Projects, ", "), action.ErrLimit)
	}

	orig, err := gitlabgit.MergeRequestFrom(ctx, r.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the merge request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no merge request was made from the branch %s", action.ErrPermanent, branch)
	}

	if gitlabgit.StateOf(orig) == gitchange.StateMerged {
		return r.putBack(ctx, inv, params, orig, snap, force)
	}

	res, err := gitlabgit.CloseOriginal(ctx, r.client, r.db, r.metrics, inv, params.Project, orig, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("close the merge request to undo: %w", err)
	}

	return res, nil
}

// putBack opens a draft merge request that puts back what the commit wrote in
// the files the merged revert changed.
func (r *Revert) putBack(ctx context.Context, inv action.Invocation, params Params, orig *gitlabgit.MergeRequest, snap action.Snapshot, force bool) (action.Result, error) {
	branch := inv.IdempotencyKey

	if err := gitlabgit.Record(ctx, r.db, inv.UndoOf, params.Project, gitlabgit.StatusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	mr, branchExists, err := gitlabgit.Existing(ctx, r.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	pushed := ""

	if mr == nil {
		if !branchExists {
			var done *action.Result

			pushed, done, err = r.pushPutBack(ctx, inv, params, snap, force)
			if err != nil {
				return action.Result{}, fmt.Errorf("put the files back: %w", err)
			}

			if done != nil {
				return *done, nil
			}
		}

		mr, err = gitlabgit.OpenDraft(ctx, r.client, params.Project, branch, r.undoRequest(inv, params, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the merge request that undoes the revert: %w", err)
		}
	} else if err := gitlabgit.RequireDraft(ctx, r.client, params.Project, branch, mr); err != nil {
		return action.Result{}, fmt.Errorf("check the merge request of an earlier attempt: %w", err)
	}

	if err := gitlabgit.Record(ctx, r.db, inv.ProposalID, params.Project, gitlabgit.StatusOf(mr)); err != nil {
		return action.Result{}, fmt.Errorf("record the merge request that undoes the revert: %w", err)
	}

	r.metrics.MergeRequest(gitlabgit.ResultReverted)

	return putBackResult(params.Project, mr, pushed), nil
}

// pushPutBack checks the files on the base branch against the snapshot and
// writes the commit that puts back what the commit wrote. done is set, with the
// result, when there is nothing to put back. It returns the SHA of the commit.
func (r *Revert) pushPutBack(ctx context.Context, inv action.Invocation, params Params, snap action.Snapshot, force bool) (pushed string, done *action.Result, err error) {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return "", nil, fmt.Errorf("read the snapshot: %w", err)
	}

	if len(sides.After) == 0 {
		return "", nil, fmt.Errorf("%w: the snapshot describes no file", action.ErrPermanent)
	}

	head, err := gitlabgit.BaseOf(ctx, r.client, params.Project, params.BaseBranch)
	if err != nil {
		return "", nil, fmt.Errorf("read the base branch: %w", err)
	}

	plan, err := gitlabgit.PlanRevert(ctx, gitlabgit.NewReader(r.client, params.Project, head.Commit), sides, r.guardPath)
	if err != nil {
		return "", nil, unreadable("plan what to put back", err)
	}

	if len(plan.Conflicts) > 0 && !force {
		live, merr := json.Marshal(plan.Live)
		if merr != nil {
			return "", nil, fmt.Errorf("encode the files as they are now: %w", merr)
		}

		return "", nil, &action.ConflictError{Live: live}
	}

	if len(plan.Restores) == 0 {
		return "", &action.Result{Summary: "Nothing to put back: the files are as the commit left them."}, nil
	}

	actions, err := gitlabgit.RestoreActions(ctx, r.client, params.Project, plan.Restores)
	if err != nil {
		return "", nil, unreadable("read what the commit wrote", err)
	}

	pushed, err = gitlabgit.CommitTo(ctx, r.client, params.Project, gitlabgit.CommitRequest{
		Branch:        inv.IdempotencyKey,
		StartSHA:      head.Commit,
		CommitMessage: undoMessage(params, inv),
		Actions:       actions,
	})
	if err != nil {
		return "", nil, fmt.Errorf("push the branch: %w", err)
	}

	return pushed, nil, nil
}

// guardPath refuses to put back a file on a path that is refused, built in or
// added by the operator, whatever the snapshot says.
func (r *Revert) guardPath(_ context.Context, path string) error {
	if err := gitchange.CheckNotRefused(path, r.limits.RefusedPaths); err != nil {
		return fmt.Errorf("the revert cannot be undone: %w", err)
	}

	return nil
}

// unreadable wraps err with what was being done. A file that cannot be read as
// text is a refusal, so the approver reads why.
func unreadable(doing string, err error) error {
	if isUnreadable(err) {
		return refuse("%s: %w; put it back by hand", doing, err)
	}

	return fmt.Errorf("%s: %w", doing, err)
}

// putBackResult describes the merge request that puts the files back, for people
// and for the proposal. It carries the number and head SHA for the job that
// reports the checks.
func putBackResult(project string, mr *gitlabgit.MergeRequest, pushed string) action.Result {
	title := fmt.Sprintf("%s!%d", project, mr.IID)

	head := mr.SHA
	if head == "" {
		head = pushed
	}

	return action.Result{
		Summary:     "Opened the draft merge request " + title + ", which puts back what the revert took out. It still needs review and merging.",
		Links:       []action.Link{{Title: title, URL: mr.WebURL}},
		PullRequest: &action.PullRequestRef{Number: int64(mr.IID), HeadSHA: head},
	}
}

// undoRequest is the draft merge request that puts back what the commit wrote.
func (r *Revert) undoRequest(inv action.Invocation, params Params, branch string) gitlabgit.CreateRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Puts back what the revert made for proposal `%s` took out. The files go back to what the commit wrote.\n\n", inv.UndoOf)
	b.WriteString(revertTrailer + " " + inv.ProposalID)

	return gitlabgit.CreateRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(undoPrefix+params.Title, true, undoTitleFallback),
		Description:  gitlabgit.Description(b.String(), inv.ProposalID, inv.InvestigationID, r.publicURL),
	}
}

// undoMessage returns the message of the commit that puts back what the commit
// wrote.
func undoMessage(params Params, inv action.Invocation) string {
	what := "commit " + params.Commit
	if params.MergeRequest != 0 {
		what = fmt.Sprintf("merge request !%d", params.MergeRequest)
	}

	return gitlabgit.CommitTitle("Undo the revert of "+what) + "\n\nSidekick proposal " + inv.ProposalID + ", which undoes " + inv.UndoOf + ".\n\n" + revertTrailer + " " + inv.ProposalID + "\n"
}
