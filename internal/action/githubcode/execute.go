package githubcode

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// Execute implements action.Action. The branch is named by the idempotency
// key, sidekick/<proposal id>, so a repeat after a crash finds what the
// first attempt made: a pull request from the branch is returned as it is,
// and a branch without one gets its pull request. Otherwise it checks the
// files again against the base as it is now and against the snapshot, writes
// them as one commit on a new branch and opens the pull request as a draft.
func (c *Code) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	if err := c.Validate(ctx, inv.Params); err != nil {
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

	pr, branchExists, err := githubgit.Existing(ctx, c.client, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if pr != nil {
		if err := c.requireDraft(ctx, owner, name, branch, pr); err != nil {
			c.metrics.PullRequest(githubgit.ResultError)

			return action.Result{}, fmt.Errorf("check the pull request of an earlier attempt: %w", err)
		}

		c.metrics.PullRequest(githubgit.ResultExisting)

		return c.result(owner, name, pr, githubgit.ResultExisting), nil
	}

	if !branchExists {
		pre, err := c.prepare(ctx, inv.Params, inv.Snapshot)
		if err != nil {
			c.metrics.PullRequest(githubgit.ResultError)

			return action.Result{}, fmt.Errorf("check the pull request before it is made: %w", err)
		}

		if len(pre.changes) == 0 {
			c.metrics.PullRequest(githubgit.ResultApplied)

			return action.Result{Summary: "The change is on the base branch already, so no pull request was opened."}, nil
		}

		message := githubgit.CommitMessage(pre.params.Title, inv.ProposalID)

		if err := githubgit.CommitTo(ctx, c.client, pre.owner, pre.name, pre.base, treeEntries(pre), message, branch); err != nil {
			c.metrics.PullRequest(githubgit.ResultError)

			return action.Result{}, fmt.Errorf("push the branch: %w", err)
		}
	}

	pr, err = c.openDraft(ctx, owner, name, branch, c.pullRequestRequest(params, branch, inv))
	if err != nil {
		c.metrics.PullRequest(githubgit.ResultError)

		return action.Result{}, fmt.Errorf("make the pull request from %s: %w", branch, err)
	}

	c.metrics.PullRequest(githubgit.ResultOpened)
	c.log.InfoContext(ctx, "pull request opened", "proposal_id", inv.ProposalID, "repo", params.Repo, "number", pr.GetNumber())

	return c.result(owner, name, pr, githubgit.ResultOpened), nil
}

// openDraft makes a pull request and checks that it is a draft, as
// requireDraft does.
func (c *Code) openDraft(ctx context.Context, owner, name, branch string, req gh.CreatePullRequest) (*gh.PullRequest, error) {
	pr, err := githubgit.OpenPullRequest(ctx, c.client, owner, name, req)
	if err != nil {
		return nil, fmt.Errorf("open the pull request: %w", err)
	}

	if err := c.requireDraft(ctx, owner, name, branch, pr); err != nil {
		return nil, fmt.Errorf("check that the pull request is a draft: %w", err)
	}

	return pr, nil
}

// requireDraft makes sure Sidekick never leaves a pull request that is not a
// draft, whether it just made it or found it from an earlier attempt, for
// example when GitHub does not make drafts for a private repository on its
// plan. A pull request that is open and not a draft is closed. Then the branch
// of a closed pull request that is not a draft is deleted if it is still
// there, and the error wraps action.ErrPermanent. A pull request that was
// merged is left alone: a person did that. A draft passes.
func (c *Code) requireDraft(ctx context.Context, owner, name, branch string, pr *gh.PullRequest) error {
	state := githubgit.StateOf(pr)
	if pr.GetDraft() || state == gitchange.StateMerged {
		return nil
	}

	title := fmt.Sprintf("%s/%s#%d", owner, name, pr.GetNumber())

	if state == gitchange.StateOpen {
		if _, err := githubgit.ClosePullRequest(ctx, c.client, owner, name, pr.GetNumber()); err != nil {
			return fmt.Errorf("close the pull request %s that is not a draft: %w", title, err)
		}
	}

	if err := githubgit.DeleteBranch(ctx, c.client, owner, name, branch); err != nil {
		return fmt.Errorf("delete the branch of the pull request %s that is not a draft: %w", title, err)
	}

	return fmt.Errorf("%w: GitHub did not open %s as a draft (draft pull requests may need another plan for private repositories), so Sidekick closed it and deleted its branch", action.ErrPermanent, title)
}

// pullRequestRequest is the request that makes the draft pull request of a
// proposal from the branch.
func (c *Code) pullRequestRequest(params Params, branch string, inv action.Invocation) gh.CreatePullRequest {
	return gh.CreatePullRequest{
		Title: new(gitchange.OneLine(gitchange.PlainText(params.Title))),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(githubgit.Description(params.Body, inv.ProposalID, inv.InvestigationID, c.publicURL)),
		Draft: new(true),
	}
}

// result describes the pull request for people and for the proposal, with its
// number and head SHA for whatever follows it.
func (c *Code) result(owner, name string, pr *gh.PullRequest, how string) action.Result {
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

// treeEntries returns the tree entries that write the changes: a file that
// exists keeps its mode, a new file is a regular file, and a deleted path has
// no content.
func treeEntries(l *loaded) []*gh.TreeEntry {
	entries := make([]*gh.TreeEntry, 0, len(l.changes))

	for _, ch := range l.changes {
		f := l.files[ch.Path]
		entry := &gh.TreeEntry{Path: new(ch.Path), Type: new(typeBlob), Mode: new(modeOf(f))}

		if !ch.Deleted() {
			entry.Content = ch.Content
		}

		entries = append(entries, entry)
	}

	return entries
}

// modeOf returns the mode a file is written with.
func modeOf(f readFile) string {
	if f.exists {
		return f.mode
	}

	return fileMode
}
