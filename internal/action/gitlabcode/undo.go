package gitlabcode

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

// Undo implements action.Undoer. It undoes the merge request that the proposal
// inv.UndoOf made.
//
// A merge request that is not merged is closed and its branch is deleted. One
// that was merged is reverted with a new draft merge request, which still
// needs review and merging. Before it opens one, it checks that each file on
// the base branch is still as the merged merge request left it. If one is not,
// the error is an *action.ConflictError that carries the files as they are
// now, and force makes the revert overwrite them. A file that is now a
// symlink, a submodule or a directory is never overwritten. The revert touches
// only the files of the merged merge request, and a path that a refused glob
// matches now is refused, so limits.paths, limits.allowDelete and the size
// limits do not apply to it.
//
// It is safe to repeat: it finds the revert merge request, or the revert
// branch, of an earlier attempt, and closing a closed merge request changes
// nothing.
func (c *Code) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	branch, err := gitlabgit.UndoBranch(inv)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the call to undo: %w", err)
	}

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the parameters of the proposal: %w", err)
	}

	if !c.limits.allowsProject(params.Project) {
		return action.Result{}, refuse("project %q is not one of limits.projects (%s)", params.Project, strings.Join(c.limits.Projects, ", "))
	}

	orig, err := gitlabgit.MergeRequestFrom(ctx, c.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the merge request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no merge request was made from the branch %s", action.ErrPermanent, branch)
	}

	if gitlabgit.StateOf(orig) == gitchange.StateMerged {
		return c.revert(ctx, inv, params, orig, snap, force)
	}

	res, err := gitlabgit.CloseOriginal(ctx, c.client, c.db, c.metrics, inv, params.Project, orig, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("close the merge request to undo: %w", err)
	}

	return res, nil
}

// revert opens a draft merge request that restores the files of a merged merge
// request to their earlier content. A revert merge request found from an
// earlier attempt is checked to be a draft, like one that is made now.
func (c *Code) revert(ctx context.Context, inv action.Invocation, params Params, orig *gitlabgit.MergeRequest, snap action.Snapshot, force bool) (action.Result, error) {
	branch := inv.IdempotencyKey

	if err := gitlabgit.Record(ctx, c.db, inv.UndoOf, params.Project, gitlabgit.StatusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	mr, branchExists, err := gitlabgit.Existing(ctx, c.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	switch {
	case mr != nil:
		if err := gitlabgit.RequireDraft(ctx, c.client, params.Project, branch, mr); err != nil {
			return action.Result{}, fmt.Errorf("check the revert merge request of an earlier attempt: %w", err)
		}
	default:
		if !branchExists {
			res, done, err := c.pushRevert(ctx, inv, params, snap, force)
			if err != nil {
				return action.Result{}, fmt.Errorf("revert the files: %w", err)
			}

			if done {
				return res, nil
			}
		}

		mr, err = gitlabgit.OpenDraft(ctx, c.client, params.Project, branch, c.revertRequest(inv, params, orig, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the revert merge request: %w", err)
		}
	}

	if err := gitlabgit.Record(ctx, c.db, inv.ProposalID, params.Project, gitlabgit.StatusOf(mr)); err != nil {
		return action.Result{}, fmt.Errorf("record the revert merge request: %w", err)
	}

	c.metrics.MergeRequest(gitlabgit.ResultReverted)

	title := params.Project + "!" + strconv.Itoa(mr.IID)

	return action.Result{
		Summary:     "Opened the revert draft merge request " + title + ". It still needs review and merging. A file the merged change deleted comes back as a regular file, even if it was executable.",
		Links:       []action.Link{{Title: title, URL: mr.WebURL}},
		PullRequest: &action.PullRequestRef{Number: int64(mr.IID), HeadSHA: mr.SHA},
	}, nil
}

// pushRevert checks the files on the base branch against the snapshot and
// writes the commit that restores their earlier content on the revert branch.
// done is true, with the result, when there is nothing to restore.
func (c *Code) pushRevert(ctx context.Context, inv action.Invocation, params Params, snap action.Snapshot, force bool) (res action.Result, done bool, err error) {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the snapshot: %w", err)
	}

	if len(sides.After) == 0 {
		return action.Result{}, false, fmt.Errorf("%w: the snapshot lists no files", action.ErrPermanent)
	}

	head, err := gitlabgit.BaseOf(ctx, c.client, params.Project, params.BaseBranch)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the base branch: %w", err)
	}

	r := gitlabgit.NewReader(c.client, params.Project, head.Commit)

	plan, err := gitlabgit.PlanRevert(ctx, r, sides, c.revertGuard(r))
	if err != nil {
		return action.Result{}, false, fmt.Errorf("plan the revert: %w", refusalOf(err))
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

	actions, err := gitlabgit.RestoreActions(ctx, c.client, params.Project, plan.Restores)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the earlier content of the files: %w", refusalOf(err))
	}

	message := revertPrefix + source.CutText(gitlabgit.CommitTitle(params.Title), gitlabgit.MaxCommitTitleBytes-len(revertPrefix)) +
		"\n\nSidekick proposal " + inv.ProposalID + ", which undoes " + inv.UndoOf + "\n"

	_, err = gitlabgit.CommitTo(ctx, c.client, params.Project, gitlabgit.CommitRequest{
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

// revertGuard returns the check that runs on each path of a revert before it
// is read: a path that a refused glob matches now is refused, and so is one
// that is no longer a regular file.
func (c *Code) revertGuard(r *gitlabgit.Reader) func(ctx context.Context, path string) error {
	return func(ctx context.Context, path string) error {
		if err := gitchange.CheckNotRefused(path, c.limits.RefusedPaths); err != nil {
			return &refusal{err: err}
		}

		entry, err := entryOf(ctx, r, path)
		if err != nil {
			return fmt.Errorf("find the entry: %w", err)
		}

		if entry == nil {
			return nil
		}

		if err := checkRegular(path, entry); err != nil {
			return fmt.Errorf("the file is no longer one the revert may write: %w", err)
		}

		return nil
	}
}

// revertRequest is the draft merge request that reverts orig.
func (c *Code) revertRequest(inv action.Invocation, params Params, orig *gitlabgit.MergeRequest, branch string) gitlabgit.CreateRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Reverts the merge request `%s!%d`, which Sidekick opened for proposal `%s`.\n\n", params.Project, orig.IID, inv.UndoOf)
	b.WriteString("This change restores the earlier content of the files.\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`, which undoes `%s`.", inv.ProposalID, inv.UndoOf)

	if link := weblink.Investigation(c.publicURL, inv.InvestigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	return gitlabgit.CreateRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(revertPrefix+params.Title, true, fallbackTitle),
		Description:  b.String(),
	}
}
