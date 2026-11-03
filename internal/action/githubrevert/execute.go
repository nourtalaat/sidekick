package githubrevert

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The quotes around the subject in the first line of a revert commit, the
// way git revert writes it.
const (
	revertPrefix = `Revert "`
	revertSuffix = `"`
)

var errNoSnapshot = errors.New("the invocation has no snapshot to write")

// Execute implements action.Action. The branch is named by the idempotency
// key, sidekick/<proposal id>, so a repeat after a crash finds what the first
// attempt made: a pull request from the branch is returned as it is, and a
// branch without one gets its pull request. Otherwise it reads the commit and
// the files again, checks them against the snapshot, writes the revert as one
// commit on a new branch and opens a draft pull request. It never merges,
// requests reviewers, assigns or turns on auto-merge.
func (r *Revert) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	params, err := r.params(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if !githubgit.BranchOfProposal.MatchString(inv.IdempotencyKey) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, githubgit.ErrNoBranch)
	}

	branch := inv.IdempotencyKey
	owner, name, _ := strings.Cut(params.Repo, "/")

	pr, branchExists, err := githubgit.Existing(ctx, r.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if pr != nil {
		if err := githubgit.RequireDraft(ctx, r.client, owner, name, branch, pr); err != nil {
			r.metrics.PullRequest(githubgit.ResultError)

			return action.Result{}, fmt.Errorf("check the pull request of an earlier attempt: %w", err)
		}

		r.metrics.PullRequest(githubgit.ResultExisting)

		return r.result(owner, name, pr, githubgit.ResultExisting), nil
	}

	if !branchExists {
		res, done, err := r.push(ctx, inv, branch)
		if err != nil {
			r.metrics.PullRequest(githubgit.ResultError)

			return action.Result{}, fmt.Errorf("push the branch: %w", err)
		}

		if done {
			r.metrics.PullRequest(githubgit.ResultApplied)

			return res, nil
		}
	}

	pr, err = githubgit.OpenDraft(ctx, r.client, owner, name, branch, r.pullRequestRequest(params, branch, inv))
	if err != nil {
		r.metrics.PullRequest(githubgit.ResultError)

		return action.Result{}, fmt.Errorf("make the pull request from %s: %w", branch, err)
	}

	r.metrics.PullRequest(githubgit.ResultOpened)
	r.log.InfoContext(ctx, "revert pull request opened", "proposal_id", inv.ProposalID, "repo", params.Repo, "number", pr.GetNumber())

	return r.result(owner, name, pr, githubgit.ResultOpened), nil
}

// push checks the revert against the snapshot and writes it as one commit on
// the branch. done is true, with the result, when every file is back to what
// it was before the commit already, so there is nothing to push.
func (r *Revert) push(ctx context.Context, inv action.Invocation, branch string) (res action.Result, done bool, err error) {
	if inv.Snapshot == nil {
		return action.Result{}, false, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	l, err := r.load(ctx, inv.Params, false)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("check the revert before it is made: %w", err)
	}

	writes, err := l.applySnapshot(*inv.Snapshot)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("compare with the snapshot: %w", err)
	}

	if len(writes) == 0 {
		return action.Result{Summary: "The change is reverted on the base branch already, so no pull request was opened."}, true, nil
	}

	message := commitMessage(l.target, inv.ProposalID)

	if err := githubgit.CommitTo(ctx, r.client, l.owner, l.name, l.head, treeEntries(writes), message, branch); err != nil {
		return action.Result{}, false, fmt.Errorf("write the commit: %w", err)
	}

	return action.Result{}, false, nil
}

// pullRequestRequest is the draft pull request of a proposal. The draft is
// not a setting: nothing in the config can turn it off.
func (r *Revert) pullRequestRequest(params Params, branch string, inv action.Invocation) gh.CreatePullRequest {
	return gh.CreatePullRequest{
		Title: new(gitchange.OneLine(gitchange.PlainText(params.Title))),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(githubgit.Description(body(params, inv.ProposalID), inv.ProposalID, inv.InvestigationID, r.publicURL)),
		Draft: new(true),
	}
}

// body is the description of the pull request before the shared footer: the
// model's reason, what is reverted (which GitHub turns into a link) and the
// line that marks the pull request as a revert of this action.
func body(params Params, proposalID string) string {
	what := "commit " + params.Commit
	if params.PullRequest != 0 {
		what = fmt.Sprintf("pull request #%d", params.PullRequest)
	}

	return params.Body + "\n\nReverts " + what + ".\n\n" + revertTrailer + " " + proposalID
}

// commitMessage returns the message of the revert commit: Revert and the
// subject of what is reverted on the first line, what it reverts, and the
// line that marks the commit as a revert of this action.
func commitMessage(t *target, proposalID string) string {
	subject := source.CutText(gitchange.OneLine(gitchange.PlainText(t.subject)), githubgit.MaxCommitTitleBytes-len(revertPrefix)-len(revertSuffix))

	return revertPrefix + subject + revertSuffix + "\n\nThis reverts " + t.reverts + ".\n\n" + revertTrailer + " " + proposalID + "\n"
}

// treeEntries returns the tree entries that write the files: the parent's
// blob and mode, or a delete for a file the commit added.
func treeEntries(files []file) []*gh.TreeEntry {
	entries := make([]*gh.TreeEntry, 0, len(files))

	for _, f := range files {
		entries = append(entries, restoreEntry(f.path, f.parent))
	}

	return entries
}

// restoreEntry is the tree entry that puts a file back to e, or deletes it
// when e is not there.
func restoreEntry(path string, e entry) *gh.TreeEntry {
	if !e.exists() {
		return &gh.TreeEntry{Path: new(path)}
	}

	return &gh.TreeEntry{Path: new(path), Mode: new(e.mode), Type: new(typeBlob), SHA: new(e.blob)}
}

// result describes the pull request for people and for the proposal. It
// carries the number and head SHA for the job that reports the checks.
func (r *Revert) result(owner, name string, pr *gh.PullRequest, how string) action.Result {
	title := fmt.Sprintf("%s/%s#%d", owner, name, pr.GetNumber())

	draft := ""
	if pr.GetDraft() {
		draft = "draft "
	}

	var summary string

	switch state := githubgit.StateOf(pr); {
	case how == githubgit.ResultOpened:
		summary = "Opened " + draft + "pull request " + title
	case state == gitchange.StateMerged:
		summary = "Found pull request " + title + ", which has been merged since it was opened"
	case state == gitchange.StateClosed:
		summary = "Found pull request " + title + ", which has been closed since it was opened"
	default:
		summary = "Found the already opened " + draft + "pull request " + title
	}

	return action.Result{
		Summary:     summary,
		Links:       []action.Link{{Title: title, URL: pr.GetHTMLURL()}},
		PullRequest: &action.PullRequestRef{Number: int64(pr.GetNumber()), HeadSHA: pr.GetHead().GetSHA()},
	}
}
