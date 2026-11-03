package githubpr

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
// that was merged is reverted with a new pull request, which still needs
// review and merging: the revert restores the earlier content of the files,
// so it is not run through the validator. Before it opens one, it checks that
// each file on the base branch is still as the merged pull request left it.
// If one is not, the error is an *action.ConflictError that carries the
// files as they are now, and force makes the revert overwrite them.
//
// It is safe to repeat: it finds the revert branch and pull request of an
// earlier attempt, and closing a closed pull request changes nothing.
func (p *PR) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	branch, err := githubgit.UndoBranch(inv)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the call to undo: %w", err)
	}

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the parameters of the proposal: %w", err)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	orig, err := githubgit.PullRequestFrom(ctx, p.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the pull request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no pull request was made from the branch %s", action.ErrPermanent, branch)
	}

	if githubgit.StateOf(orig) == gitchange.StateMerged {
		return p.revert(ctx, inv, params, orig, snap, force)
	}

	return p.closeOriginal(ctx, inv, params, orig, branch)
}

// closeOriginal closes the pull request, if it is open, deletes its branch and
// records it as closed.
func (p *PR) closeOriginal(ctx context.Context, inv action.Invocation, params Params, orig *gh.PullRequest, branch string) (action.Result, error) {
	owner, name, _ := strings.Cut(params.Repo, "/")
	title := fmt.Sprintf("%s#%d", params.Repo, orig.GetNumber())
	closed, summary := orig, "The pull request "+title+" was closed already; its branch is deleted."

	if githubgit.StateOf(orig) == gitchange.StateOpen {
		updated, err := githubgit.ClosePullRequest(ctx, p.client, owner, name, orig.GetNumber())
		if err != nil {
			return action.Result{}, fmt.Errorf("close the original: %w", err)
		}

		closed, summary = updated, "Closed the pull request "+title+" and deleted its branch."
	}

	if err := githubgit.DeleteBranch(ctx, p.client, owner, name, branch); err != nil {
		return action.Result{}, fmt.Errorf("remove the branch of the closed pull request: %w", err)
	}

	st := githubgit.StatusOf(closed)
	if st.State == gitchange.StateOpen {
		st.State, st.ClosedAt = gitchange.StateClosed, new(time.Now())
	}

	if err := p.record(ctx, inv.UndoOf, params.Repo, st); err != nil {
		return action.Result{}, fmt.Errorf("record the original as %s: %w", st.State, err)
	}

	p.metrics.PullRequest(githubgit.ResultClosed)

	return action.Result{Summary: summary, Links: []action.Link{{Title: title, URL: orig.GetHTMLURL()}}}, nil
}

// revert opens a pull request that restores the files of a merged pull
// request to their earlier content.
func (p *PR) revert(ctx context.Context, inv action.Invocation, params Params, orig *gh.PullRequest, snap action.Snapshot, force bool) (action.Result, error) {
	owner, name, _ := strings.Cut(params.Repo, "/")
	branch := inv.IdempotencyKey

	if err := p.record(ctx, inv.UndoOf, params.Repo, githubgit.StatusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	pr, branchExists, err := githubgit.Existing(ctx, p.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if !branchExists {
		res, done, err := p.pushRevert(ctx, inv, params, snap, force)
		if err != nil {
			return action.Result{}, fmt.Errorf("revert the files: %w", err)
		}

		if done {
			return res, nil
		}
	}

	if pr == nil {
		pr, err = githubgit.OpenPullRequest(ctx, p.client, owner, name, p.revertRequest(inv, params, orig, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the revert pull request: %w", err)
		}
	}

	if err := p.record(ctx, inv.ProposalID, params.Repo, githubgit.StatusOf(pr)); err != nil {
		return action.Result{}, fmt.Errorf("record the revert pull request: %w", err)
	}

	p.metrics.PullRequest(githubgit.ResultReverted)

	title := fmt.Sprintf("%s#%d", params.Repo, pr.GetNumber())

	return action.Result{
		Summary: "Opened the revert pull request " + title + ". It still needs review and merging. Its files were not validated, because it restores their earlier content.",
		Links:   []action.Link{{Title: title, URL: pr.GetHTMLURL()}},
	}, nil
}

// pushRevert checks the files on the base branch against the snapshot and
// writes the commit that restores their earlier content on the revert
// branch. done is true, with the result, when there is nothing to restore.
func (p *PR) pushRevert(ctx context.Context, inv action.Invocation, params Params, snap action.Snapshot, force bool) (res action.Result, done bool, err error) {
	owner, name, _ := strings.Cut(params.Repo, "/")

	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the snapshot: %w", err)
	}

	head, err := githubgit.BaseOf(ctx, p.client, owner, name, params.BaseBranch)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the base branch: %w", err)
	}

	plan, err := p.planRevert(ctx, newReader(p.client, owner, name, head.Commit), sides)
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

	if err := githubgit.CommitTo(ctx, p.client, owner, name, head, plan.entries, message, inv.IdempotencyKey); err != nil {
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

// planRevert reads the files of the change on the base branch and decides
// what restores them. A file that is at its earlier content already needs
// nothing. A file that was added is deleted. Any other file gets the blob it
// had before, which is in the repository already, so no content is read.
func (p *PR) planRevert(ctx context.Context, r *reader, sides gitchange.SnapshotFiles) (revertPlan, error) {
	plan := revertPlan{live: make(map[string]*gitchange.BlobRef, len(sides.After))}

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		f, err := r.file(ctx, path)
		if err != nil {
			return revertPlan{}, fmt.Errorf("read the file at the base: %w", err)
		}

		if f.exists {
			plan.live[path] = &gitchange.BlobRef{Blob: f.sha}
		} else {
			plan.live[path] = nil
		}

		before := gitchange.BlobOf(sides.Before[path])
		if f.sha == before {
			continue
		}

		if f.sha != sides.After[path].Blob {
			plan.conflicts = append(plan.conflicts, path)
		}

		plan.entries = append(plan.entries, revertEntry(path, before))
	}

	return plan, nil
}

// revertEntry is the tree entry that puts a file back to a blob, or deletes
// it when it had none.
func revertEntry(path, blob string) *gh.TreeEntry {
	if blob == "" {
		return &gh.TreeEntry{Path: new(path)}
	}

	return &gh.TreeEntry{Path: new(path), Mode: new(fileMode), Type: new(blobType), SHA: new(blob)}
}

// revertRequest is the pull request that reverts orig.
func (p *PR) revertRequest(inv action.Invocation, params Params, orig *gh.PullRequest, branch string) gh.CreatePullRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Reverts the pull request %s#%d, which Sidekick opened for proposal `%s`.\n\n", params.Repo, orig.GetNumber(), inv.UndoOf)
	b.WriteString("The files were not validated: this change restores their earlier content.\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`, which undoes `%s`.", inv.ProposalID, inv.UndoOf)

	if link := weblink.Investigation(p.publicURL, inv.InvestigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	title := revertPrefix + source.CutText(gitchange.OneLine(gitchange.PlainText(params.Title)), gitchange.MaxTitleChars-len(revertPrefix))

	return gh.CreatePullRequest{
		Title: new(title),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(b.String()),
		Draft: new(p.limits.draft()),
	}
}

// record keeps the state of a pull request for the proposal with the ID, for
// the count of merged pull requests. Where there is no database, it does
// nothing.
func (p *PR) record(ctx context.Context, proposalID, repo string, st *gitchange.Status) error {
	if p.db == nil {
		return nil
	}

	if err := gitchange.RecordState(ctx, p.db, proposalID, repo, st); err != nil {
		return fmt.Errorf("record the pull request state: %w", err)
	}

	return nil
}
