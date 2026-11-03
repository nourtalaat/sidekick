package githubrevert

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/source"
)

var _ action.Undoer = (*Revert)(nil)

// undoPrefix starts the title of the pull request that puts a revert back.
const undoPrefix = "Undo: "

// Undo implements action.Undoer. It undoes the pull request that the
// proposal inv.UndoOf made.
//
// A pull request that is not merged is closed and its branch is deleted. One
// that was merged is undone with a new draft pull request that puts back what
// the commit wrote, so the change the revert took out is back. It still
// needs review and merging. Before it opens one, it checks that each file on
// the base branch is still as the revert left it. If one is not, the error is
// an *action.ConflictError that carries the files as they are now, and force
// makes the pull request overwrite them.
//
// It is safe to repeat: it finds the branch and pull request of an earlier
// attempt, and closing a closed pull request changes nothing.
func (r *Revert) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	branch, err := githubgit.UndoBranch(inv)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the call to undo: %w", err)
	}

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the parameters of the proposal: %w", err)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	orig, err := githubgit.PullRequestFrom(ctx, r.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the pull request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no pull request was made from the branch %s", action.ErrPermanent, branch)
	}

	if githubgit.StateOf(orig) == gitchange.StateMerged {
		return r.putBack(ctx, inv, params, orig, snap, force)
	}

	res, err := githubgit.CloseOriginal(ctx, r.client, r.db, r.metrics, inv, params.Repo, orig, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("close the pull request to undo: %w", err)
	}

	return res, nil
}

// putBack opens a draft pull request that puts back what the commit wrote in
// the files the merged revert changed.
func (r *Revert) putBack(ctx context.Context, inv action.Invocation, params Params, orig *gh.PullRequest, snap action.Snapshot, force bool) (action.Result, error) {
	owner, name, _ := strings.Cut(params.Repo, "/")
	branch := inv.IdempotencyKey

	if err := githubgit.Record(ctx, r.db, inv.UndoOf, params.Repo, githubgit.StatusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	pr, branchExists, err := githubgit.Existing(ctx, r.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if !branchExists {
		res, done, err := r.pushPutBack(ctx, inv, snap, force, branch)
		if err != nil {
			return action.Result{}, fmt.Errorf("put the files back: %w", err)
		}

		if done {
			return res, nil
		}
	}

	if pr == nil {
		pr, err = githubgit.OpenDraft(ctx, r.client, owner, name, branch, r.undoRequest(inv, params, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the pull request that undoes the revert: %w", err)
		}
	} else if err := githubgit.RequireDraft(ctx, r.client, owner, name, branch, pr); err != nil {
		return action.Result{}, fmt.Errorf("check the pull request of an earlier attempt: %w", err)
	}

	if err := githubgit.Record(ctx, r.db, inv.ProposalID, params.Repo, githubgit.StatusOf(pr)); err != nil {
		return action.Result{}, fmt.Errorf("record the pull request that undoes the revert: %w", err)
	}

	r.metrics.PullRequest(githubgit.ResultReverted)

	title := fmt.Sprintf("%s#%d", params.Repo, pr.GetNumber())

	return action.Result{
		Summary:     "Opened the draft pull request " + title + ", which puts back what the revert took out. It still needs review and merging.",
		Links:       []action.Link{{Title: title, URL: pr.GetHTMLURL()}},
		PullRequest: &action.PullRequestRef{Number: int64(pr.GetNumber()), HeadSHA: pr.GetHead().GetSHA()},
	}, nil
}

// pushPutBack checks the files on the base branch against the snapshot and
// writes the commit that puts back what the commit wrote. done is true, with
// the result, when there is nothing to put back.
func (r *Revert) pushPutBack(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool, branch string) (res action.Result, done bool, err error) {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the snapshot: %w", err)
	}

	l, err := r.load(ctx, inv.Params, true)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read what the revert changed: %w", err)
	}

	plan, err := l.planPutBack(sides)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("plan what to put back: %w", err)
	}

	if len(plan.conflicts) > 0 && !force {
		live, merr := json.Marshal(plan.live)
		if merr != nil {
			return action.Result{}, false, fmt.Errorf("encode the files as they are now: %w", merr)
		}

		return action.Result{}, false, &action.ConflictError{Live: live}
	}

	if len(plan.entries) == 0 {
		return action.Result{Summary: "Nothing to put back: the files are as the commit left them."}, true, nil
	}

	if err := githubgit.CommitTo(ctx, r.client, l.owner, l.name, l.head, plan.entries, undoMessage(l.target, inv), branch); err != nil {
		return action.Result{}, false, fmt.Errorf("push the branch: %w", err)
	}

	return action.Result{}, false, nil
}

// putBackPlan is what putting back what the commit wrote takes.
type putBackPlan struct {
	// entries write the files that are not as the commit left them.
	entries []*gh.TreeEntry
	// conflicts are the files that are neither as the revert left them nor
	// as the commit left them.
	conflicts []string
	// live maps every file of the revert to the blob it has now, or nil.
	live map[string]*gitchange.BlobRef
}

// planPutBack decides what puts back what the commit wrote. A file that is
// as the commit left it already needs nothing. A file the revert put back
// is deleted again, and any other file gets the blob the commit wrote.
func (l *loaded) planPutBack(sides gitchange.SnapshotFiles) (putBackPlan, error) {
	plan := putBackPlan{live: make(map[string]*gitchange.BlobRef, len(sides.After))}

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		f, err := l.described(sides, path)
		if err != nil {
			return putBackPlan{}, fmt.Errorf("read the snapshot of %q: %w", path, err)
		}

		plan.live[path] = refOf(f.live)

		switch f.live {
		case f.commit:
			continue
		case f.parent:
		default:
			plan.conflicts = append(plan.conflicts, path)
		}

		plan.entries = append(plan.entries, restoreEntry(path, f.commit))
	}

	return plan, nil
}

// refOf returns the blob reference of a file, or nil for one that is not
// there.
func refOf(e entry) *gitchange.BlobRef {
	if !e.exists() {
		return nil
	}

	return &gitchange.BlobRef{Blob: e.blob}
}

// undoRequest is the draft pull request that puts back what the commit
// wrote.
func (r *Revert) undoRequest(inv action.Invocation, params Params, branch string) gh.CreatePullRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Puts back what the revert made for proposal `%s` took out. The files go back to what the commit wrote.\n\n", inv.UndoOf)
	b.WriteString(revertTrailer + " " + inv.ProposalID)

	title := undoPrefix + source.CutText(gitchange.OneLine(gitchange.PlainText(params.Title)), gitchange.MaxTitleChars-len(undoPrefix))

	return gh.CreatePullRequest{
		Title: new(title),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(githubgit.Description(b.String(), inv.ProposalID, inv.InvestigationID, r.publicURL)),
		Draft: new(true),
	}
}

// undoMessage returns the message of the commit that puts back what the
// commit wrote.
func undoMessage(t *target, inv action.Invocation) string {
	title := source.CutText("Undo the revert of "+t.what, githubgit.MaxCommitTitleBytes)

	return title + "\n\nSidekick proposal " + inv.ProposalID + ", which undoes " + inv.UndoOf + ".\n\n" + revertTrailer + " " + inv.ProposalID + "\n"
}
