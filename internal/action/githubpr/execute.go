package githubpr

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// File mode of every file the action writes: a regular, non-executable file.
const (
	fileMode = "100644"
	blobType = "blob"
)

// Execute implements action.Action. The branch is named by the idempotency
// key, sidekick/<proposal id>, so a repeat after a crash finds what the
// first attempt made: a pull request from the branch is returned as it is,
// and a branch without one gets its pull request. Otherwise it checks the
// files again, writes them as one commit on a new branch and opens the pull
// request.
func (p *PR) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	if err := p.Validate(ctx, inv.Params); err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if !githubgit.BranchOfProposal.MatchString(inv.IdempotencyKey) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, githubgit.ErrNoBranch)
	}

	branch := inv.IdempotencyKey

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("decode the parameters: %w", err)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	pr, branchExists, err := githubgit.Existing(ctx, p.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if pr != nil {
		p.metrics.PullRequest(githubgit.ResultExisting)

		return p.result(owner, name, pr, githubgit.ResultExisting), nil
	}

	if !branchExists {
		pre, err := p.prepare(ctx, inv.Params, inv.Snapshot)
		if err != nil {
			p.metrics.PullRequest(githubgit.ResultError)

			return action.Result{}, fmt.Errorf("check the pull request before it is made: %w", err)
		}

		if len(pre.changes) == 0 {
			p.metrics.PullRequest(githubgit.ResultApplied)

			return action.Result{Summary: "The change is on the base branch already, so no pull request was opened."}, nil
		}

		message := githubgit.CommitMessage(pre.params.Title, inv.ProposalID)

		if err := githubgit.CommitTo(ctx, p.client, pre.owner, pre.name, pre.base, treeEntries(pre.changes), message, branch); err != nil {
			p.metrics.PullRequest(githubgit.ResultError)

			return action.Result{}, fmt.Errorf("push the branch: %w", err)
		}
	}

	pr, err = githubgit.OpenPullRequest(ctx, p.client, owner, name, p.pullRequestRequest(params, branch, inv))
	if err != nil {
		p.metrics.PullRequest(githubgit.ResultError)

		return action.Result{}, fmt.Errorf("make the pull request from %s: %w", branch, err)
	}

	p.metrics.PullRequest(githubgit.ResultOpened)
	p.log.InfoContext(ctx, "pull request opened", "proposal_id", inv.ProposalID, "repo", params.Repo, "number", pr.GetNumber())

	return p.result(owner, name, pr, githubgit.ResultOpened), nil
}

// pullRequestRequest is the request that makes the pull request of a
// proposal from the branch.
func (p *PR) pullRequestRequest(params Params, branch string, inv action.Invocation) gh.CreatePullRequest {
	return gh.CreatePullRequest{
		Title: new(gitchange.OneLine(gitchange.PlainText(params.Title))),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(githubgit.Description(params.Body, inv.ProposalID, inv.InvestigationID, p.publicURL)),
		Draft: new(p.limits.draft()),
	}
}

// result describes the pull request for people and for the proposal.
func (p *PR) result(owner, name string, pr *gh.PullRequest, how string) action.Result {
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

	return action.Result{Summary: summary, Links: []action.Link{{Title: title, URL: pr.GetHTMLURL()}}}
}

// treeEntries returns the tree entries that write the changes.
func treeEntries(changes []gitchange.PlannedFile) []*gh.TreeEntry {
	entries := make([]*gh.TreeEntry, 0, len(changes))

	for _, c := range changes {
		entry := &gh.TreeEntry{Path: new(c.Path)}

		if !c.Deleted() {
			entry.Mode, entry.Type, entry.Content = new(fileMode), new(blobType), c.Content
		}

		entries = append(entries, entry)
	}

	return entries
}
