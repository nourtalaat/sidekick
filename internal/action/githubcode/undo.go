package githubcode

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// revertPrefix starts the title of a revert pull request.
const revertPrefix = "Revert: "

// Undo implements action.Undoer. It undoes the pull request that the
// proposal inv.UndoOf made.
//
// A pull request that is not merged is closed and its branch is deleted. One
// that was merged is reverted with a new draft pull request, which still
// needs review and merging. Before it opens one, it checks that each file on
// the base branch is still as the merged pull request left it. If one is not,
// the error is an *action.ConflictError that carries the files as they are
// now, and force makes the revert overwrite them. A file that is now a
// symlink, a submodule or a directory is never overwritten. The revert
// touches only the files of the merged pull request, and a path that a
// refused glob matches now is refused, so limits.paths, limits.allowDelete and
// the size limits do not apply to it.
//
// It is safe to repeat: it finds the revert branch and pull request of an
// earlier attempt, and closing a closed pull request changes nothing.
func (c *Code) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	branch, err := githubgit.UndoBranch(inv)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the call to undo: %w", err)
	}

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the parameters of the proposal: %w", err)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	orig, err := githubgit.PullRequestFrom(ctx, c.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the pull request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no pull request was made from the branch %s", action.ErrPermanent, branch)
	}

	if githubgit.StateOf(orig) == gitchange.StateMerged {
		return c.revert(ctx, inv, params, orig, snap, force)
	}

	return c.closeOriginal(ctx, inv, params, orig, branch)
}

// closeOriginal closes the pull request, if it is open, deletes its branch and
// records it as closed.
func (c *Code) closeOriginal(ctx context.Context, inv action.Invocation, params Params, orig *gh.PullRequest, branch string) (action.Result, error) {
	owner, name, _ := strings.Cut(params.Repo, "/")
	title := fmt.Sprintf("%s#%d", params.Repo, orig.GetNumber())
	closed, summary := orig, "The pull request "+title+" was closed already; its branch is deleted."

	if githubgit.StateOf(orig) == gitchange.StateOpen {
		updated, err := githubgit.ClosePullRequest(ctx, c.client, owner, name, orig.GetNumber())
		if err != nil {
			return action.Result{}, fmt.Errorf("close the original: %w", err)
		}

		closed, summary = updated, "Closed the pull request "+title+" and deleted its branch."
	}

	if err := githubgit.DeleteBranch(ctx, c.client, owner, name, branch); err != nil {
		return action.Result{}, fmt.Errorf("remove the branch of the closed pull request: %w", err)
	}

	st := githubgit.StatusOf(closed)
	if st.State == gitchange.StateOpen {
		st.State, st.ClosedAt = gitchange.StateClosed, new(time.Now())
	}

	if err := c.record(ctx, inv.UndoOf, params.Repo, st); err != nil {
		return action.Result{}, fmt.Errorf("record the original as %s: %w", st.State, err)
	}

	c.metrics.PullRequest(githubgit.ResultClosed)

	return action.Result{Summary: summary, Links: []action.Link{{Title: title, URL: orig.GetHTMLURL()}}}, nil
}

// revert opens a draft pull request that restores the files of a merged pull
// request to their earlier content.
func (c *Code) revert(ctx context.Context, inv action.Invocation, params Params, orig *gh.PullRequest, snap action.Snapshot, force bool) (action.Result, error) {
	owner, name, _ := strings.Cut(params.Repo, "/")
	branch := inv.IdempotencyKey

	if err := c.record(ctx, inv.UndoOf, params.Repo, githubgit.StatusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	pr, branchExists, err := githubgit.Existing(ctx, c.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if !branchExists {
		res, done, err := c.pushRevert(ctx, inv, params, snap, force)
		if err != nil {
			return action.Result{}, fmt.Errorf("revert the files: %w", err)
		}

		if done {
			return res, nil
		}
	}

	if pr == nil {
		pr, err = c.openDraft(ctx, owner, name, branch, c.revertRequest(inv, params, orig, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the revert pull request: %w", err)
		}
	} else if err := c.requireDraft(ctx, owner, name, branch, pr); err != nil {
		return action.Result{}, fmt.Errorf("check the revert pull request of an earlier attempt: %w", err)
	}

	if err := c.record(ctx, inv.ProposalID, params.Repo, githubgit.StatusOf(pr)); err != nil {
		return action.Result{}, fmt.Errorf("record the revert pull request: %w", err)
	}

	c.metrics.PullRequest(githubgit.ResultReverted)

	title := fmt.Sprintf("%s#%d", params.Repo, pr.GetNumber())

	return action.Result{
		Summary:     "Opened the revert draft pull request " + title + ". It still needs review and merging.",
		Links:       []action.Link{{Title: title, URL: pr.GetHTMLURL()}},
		PullRequest: &action.PullRequestRef{Number: int64(pr.GetNumber()), HeadSHA: pr.GetHead().GetSHA()},
	}, nil
}

// pushRevert checks the files on the base branch against the snapshot and
// writes the commit that restores their earlier content on the revert
// branch. done is true, with the result, when there is nothing to restore.
func (c *Code) pushRevert(ctx context.Context, inv action.Invocation, params Params, snap action.Snapshot, force bool) (res action.Result, done bool, err error) {
	owner, name, _ := strings.Cut(params.Repo, "/")

	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the snapshot: %w", err)
	}

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		if err := gitchange.CheckNotRefused(path, c.limits.RefusedPaths); err != nil {
			return action.Result{}, false, fmt.Errorf("check the path of the revert: %w", err)
		}
	}

	head, err := githubgit.BaseOf(ctx, c.client, owner, name, params.BaseBranch)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the base branch: %w", err)
	}

	plan, err := c.planRevert(ctx, newReader(c.client, owner, name, head.Tree), snap.Version, sides)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("plan the revert: %w", err)
	}

	if len(plan.conflicts) > 0 && !force {
		live, merr := json.Marshal(plan.live)
		if merr != nil {
			return action.Result{}, false, fmt.Errorf("encode the files as they are now: %w", merr)
		}

		return action.Result{}, false, &action.ConflictError{Live: live}
	}

	if len(plan.entries) == 0 {
		return action.Result{Summary: "Nothing to revert: the files are as they were before the pull request."}, true, nil
	}

	message := revertPrefix + source.CutText(gitchange.OneLine(gitchange.PlainText(params.Title)), githubgit.MaxCommitTitleBytes-len(revertPrefix)) +
		"\n\nSidekick proposal " + inv.ProposalID + ", which undoes " + inv.UndoOf + "\n"

	if err := githubgit.CommitTo(ctx, c.client, owner, name, head, plan.entries, message, inv.IdempotencyKey); err != nil {
		return action.Result{}, false, fmt.Errorf("push the revert branch: %w", err)
	}

	return action.Result{}, false, nil
}

// revertPlan is what restoring the files takes.
type revertPlan struct {
	// entries restore the files that are not at their earlier content.
	entries []*gh.TreeEntry
	// conflicts are the files that are neither as the pull request left them
	// nor as they were before it.
	conflicts []string
	// live maps every file of the change to the blob it has now, or nil.
	live map[string]*gitchange.BlobRef
}

// planRevert reads the entries of the files of the change on the base branch,
// without their content, and decides what restores them. A file that is at its
// earlier content already needs nothing. A file that was added is deleted. Any
// other file gets the blob it had before, which is in the repository already,
// with the mode it has now; a file that the pull request deleted gets the mode
// it had in the tree of the snapshot's base commit. An entry that is not a
// regular file ends the plan with an error that wraps action.ErrPermanent.
func (c *Code) planRevert(ctx context.Context, r *reader, version string, sides gitchange.SnapshotFiles) (revertPlan, error) {
	plan := revertPlan{live: make(map[string]*gitchange.BlobRef, len(sides.After))}

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		entry, err := r.entry(ctx, path)
		if err != nil {
			return revertPlan{}, fmt.Errorf("read the entry at the base: %w", err)
		}

		liveSHA, liveMode := "", ""

		if entry != nil {
			if err := checkRegular(path, entry); err != nil {
				return revertPlan{}, fmt.Errorf("the file is no longer one the revert may write: %w", err)
			}

			liveSHA, liveMode = entry.GetSHA(), entry.GetMode()
			plan.live[path] = &gitchange.BlobRef{Blob: liveSHA}
		} else {
			plan.live[path] = nil
		}

		before := gitchange.BlobOf(sides.Before[path])
		if liveSHA == before {
			continue
		}

		if liveSHA != sides.After[path].Blob {
			plan.conflicts = append(plan.conflicts, path)
		}

		if before == "" {
			plan.entries = append(plan.entries, &gh.TreeEntry{Path: new(path), Mode: new(liveMode), Type: new(typeBlob)})

			continue
		}

		mode := liveMode
		if mode == "" {
			mode, err = earlierMode(ctx, r, version, path, before)
			if err != nil {
				return revertPlan{}, fmt.Errorf("read the earlier mode of %s: %w", path, err)
			}
		}

		plan.entries = append(plan.entries, &gh.TreeEntry{Path: new(path), Mode: new(mode), Type: new(typeBlob), SHA: new(before)})
	}

	return plan, nil
}

// revertRequest is the draft pull request that reverts orig.
func (c *Code) revertRequest(inv action.Invocation, params Params, orig *gh.PullRequest, branch string) gh.CreatePullRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Reverts the pull request %s#%d, which Sidekick opened for proposal `%s`.\n\n", params.Repo, orig.GetNumber(), inv.UndoOf)
	b.WriteString("This change restores the earlier content of the files.\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`, which undoes `%s`.", inv.ProposalID, inv.UndoOf)

	if link := weblink.Investigation(c.publicURL, inv.InvestigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	title := revertPrefix + source.CutText(gitchange.OneLine(gitchange.PlainText(params.Title)), gitchange.MaxTitleChars-len(revertPrefix))

	return gh.CreatePullRequest{
		Title: new(title),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(b.String()),
		Draft: new(true),
	}
}

// record keeps the state of a pull request for the proposal with the ID, for
// the count of merged pull requests. Where there is no database, it does
// nothing.
func (c *Code) record(ctx context.Context, proposalID, repo string, st *gitchange.Status) error {
	if c.db == nil {
		return nil
	}

	if err := gitchange.RecordState(ctx, c.db, proposalID, repo, st); err != nil {
		return fmt.Errorf("record the pull request state: %w", err)
	}

	return nil
}

// earlierMode returns the mode path had in the tree of the commit the pull
// request was made on, when it had the blob. Where the commit or the entry is
// not there, or is not that blob, it is a regular file.
func earlierMode(ctx context.Context, r *reader, commit, path, blob string) (string, error) {
	if commit == "" {
		return fileMode, nil
	}

	cm, _, err := r.client.Git.GetCommit(ctx, r.owner, r.repo, commit)
	if err != nil {
		return "", githubgit.Classify("read the base commit "+commit, err)
	}

	entry, err := newReader(r.client, r.owner, r.repo, cm.GetTree().GetSHA()).entry(ctx, path)
	if err != nil {
		return "", fmt.Errorf("find the path at the base commit: %w", err)
	}

	if entry == nil || entry.GetSHA() != blob || !isRegular(entry) {
		return fileMode, nil
	}

	return entry.GetMode(), nil
}
