package gitlabcode

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// fallbackTitle is the title of a merge request whose given title is nothing
// but a draft prefix.
const fallbackTitle = "Update code"

// Execute implements action.Action. The branch is named by the idempotency
// key, sidekick/<proposal id>, so a repeat after a crash finds what the first
// attempt made: a merge request from the branch is returned as it is, after
// the check that it is a draft, and a branch without one gets its merge
// request. Otherwise it checks the files again against the base as it is now
// and against the snapshot, writes them as one commit on a new branch and
// opens the merge request as a draft.
func (c *Code) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	if err := c.Validate(ctx, inv.Params); err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if !gitlabgit.BranchOfProposal.MatchString(inv.IdempotencyKey) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, gitlabgit.ErrNoBranch)
	}

	branch := inv.IdempotencyKey

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("decode the parameters: %w", err)
	}

	mr, branchExists, err := gitlabgit.Existing(ctx, c.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if mr != nil {
		if err := gitlabgit.RequireDraft(ctx, c.client, params.Project, branch, mr); err != nil {
			c.metrics.MergeRequest(gitlabgit.ResultError)

			return action.Result{}, fmt.Errorf("check the merge request of an earlier attempt: %w", err)
		}

		c.metrics.MergeRequest(gitlabgit.ResultExisting)

		return gitlabgit.Result(params.Project, mr, gitlabgit.ResultExisting, ""), nil
	}

	var pushed string

	if !branchExists {
		pre, err := c.prepare(ctx, inv.Params, inv.Snapshot)
		if err != nil {
			c.metrics.MergeRequest(gitlabgit.ResultError)

			return action.Result{}, fmt.Errorf("check the merge request before it is made: %w", err)
		}

		if len(pre.changes) == 0 {
			c.metrics.MergeRequest(gitlabgit.ResultApplied)

			return action.Result{Summary: "The change is on the base branch already, so no merge request was opened."}, nil
		}

		pushed, err = c.push(ctx, pre, branch, inv)
		if err != nil {
			c.metrics.MergeRequest(gitlabgit.ResultError)

			return action.Result{}, fmt.Errorf("push the branch: %w", err)
		}
	}

	mr, err = gitlabgit.OpenDraft(ctx, c.client, params.Project, branch, c.createRequest(params, branch, inv))
	if err != nil {
		c.metrics.MergeRequest(gitlabgit.ResultError)

		return action.Result{}, fmt.Errorf("make the merge request from %s: %w", branch, err)
	}

	c.metrics.MergeRequest(gitlabgit.ResultOpened)
	c.log.InfoContext(ctx, "merge request opened", "proposal_id", inv.ProposalID, "project", params.Project, "iid", mr.IID)

	return gitlabgit.Result(params.Project, mr, gitlabgit.ResultOpened, pushed), nil
}

// push writes the changes as one commit that makes the branch from the commit
// the files were read at, so a base branch that moved meanwhile does not change
// what the commit is built on. It returns the SHA of the commit, or an empty
// one when the branch exists already, made by another attempt a moment ago.
func (c *Code) push(ctx context.Context, pre *loaded, branch string, inv action.Invocation) (string, error) {
	sha, err := gitlabgit.CommitTo(ctx, c.client, pre.params.Project, gitlabgit.CommitRequest{
		Branch:        branch,
		StartSHA:      pre.base.Commit,
		CommitMessage: gitlabgit.CommitMessage(pre.params.Title, inv.ProposalID),
		Actions:       commitActions(pre),
	})
	if err != nil {
		return "", fmt.Errorf("make the commit: %w", err)
	}

	return sha, nil
}

// createRequest is the request that makes the draft merge request of a
// proposal from the branch.
func (c *Code) createRequest(params Params, branch string, inv action.Invocation) gitlabgit.CreateRequest {
	return gitlabgit.CreateRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(params.Title, true, fallbackTitle),
		Description:  gitlabgit.Description(params.Body, inv.ProposalID, inv.InvestigationID, c.publicURL),
	}
}

// commitActions returns the actions that write the changes: a delete for a
// removed file, an update for a file that exists, which keeps its mode, and a
// create for a new file.
func commitActions(l *loaded) []gitlabgit.CommitAction {
	actions := make([]gitlabgit.CommitAction, 0, len(l.changes))

	for _, ch := range l.changes {
		switch {
		case ch.Deleted():
			actions = append(actions, gitlabgit.CommitAction{Action: gitlabgit.CommitDelete, FilePath: ch.Path})
		case l.files[ch.Path].Exists:
			actions = append(actions, gitlabgit.CommitAction{Action: gitlabgit.CommitUpdate, FilePath: ch.Path, Content: ch.Content})
		default:
			actions = append(actions, gitlabgit.CommitAction{Action: gitlabgit.CommitCreate, FilePath: ch.Path, Content: ch.Content})
		}
	}

	return actions
}
