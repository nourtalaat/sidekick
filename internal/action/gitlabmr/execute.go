package gitlabmr

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// fallbackTitle is the title of a merge request whose given title is nothing
// but a draft prefix.
const fallbackTitle = "Update monitor definitions"

// Execute implements action.Action. The branch is named by the idempotency
// key, sidekick/<proposal id>, so a repeat after a crash finds what the first
// attempt made. It looks for a merge request from the branch in any state
// first, and returns the one it finds as it is, even when the branch is gone
// because the merge request was merged. A branch without a merge request gets
// one. Otherwise it checks the files again, writes them as one commit that
// makes the branch, and opens the merge request.
func (m *MR) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	if err := m.Validate(ctx, inv.Params); err != nil {
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

	mr, branchExists, err := gitlabgit.Existing(ctx, m.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if mr != nil {
		if err := m.requireDraft(ctx, params.Project, branch, mr); err != nil {
			m.metrics.MergeRequest(gitlabgit.ResultError)

			return action.Result{}, fmt.Errorf("check the merge request of an earlier attempt: %w", err)
		}

		m.metrics.MergeRequest(gitlabgit.ResultExisting)

		return gitlabgit.Result(params.Project, mr, gitlabgit.ResultExisting, ""), nil
	}

	var pushed string

	if !branchExists {
		pre, err := m.prepare(ctx, inv.Params, inv.Snapshot)
		if err != nil {
			m.metrics.MergeRequest(gitlabgit.ResultError)

			return action.Result{}, fmt.Errorf("check the merge request before it is made: %w", err)
		}

		if len(pre.changes) == 0 {
			m.metrics.MergeRequest(gitlabgit.ResultApplied)

			return action.Result{Summary: "The change is on the base branch already, so no merge request was opened."}, nil
		}

		pushed, err = m.push(ctx, pre, branch, inv)
		if err != nil {
			m.metrics.MergeRequest(gitlabgit.ResultError)

			return action.Result{}, fmt.Errorf("push the branch: %w", err)
		}
	}

	mr, err = m.open(ctx, params, branch, inv)
	if err != nil {
		m.metrics.MergeRequest(gitlabgit.ResultError)

		return action.Result{}, fmt.Errorf("make the merge request from %s: %w", branch, err)
	}

	m.metrics.MergeRequest(gitlabgit.ResultOpened)
	m.log.InfoContext(ctx, "merge request opened", "proposal_id", inv.ProposalID, "project", params.Project, "iid", mr.IID)

	return gitlabgit.Result(params.Project, mr, gitlabgit.ResultOpened, pushed), nil
}

// push writes the changes as one commit that makes the branch from the head
// of the base branch. The branch and its commit appear together, so a crash
// never leaves an empty branch. The parent is the commit the files were read
// at, so a base branch that moved meanwhile does not change what the commit
// is built on. It returns the SHA of the commit. A branch that exists already,
// made by another attempt a moment ago, is left as it is, and the SHA is empty.
func (m *MR) push(ctx context.Context, pre *prepared, branch string, inv action.Invocation) (string, error) {
	actions := make([]gitlabgit.CommitAction, 0, len(pre.changes))

	for _, c := range pre.changes {
		switch {
		case c.Deleted():
			actions = append(actions, gitlabgit.CommitAction{Action: gitlabgit.CommitDelete, FilePath: c.Path})
		case pre.files[c.Path].Exists:
			actions = append(actions, gitlabgit.CommitAction{Action: gitlabgit.CommitUpdate, FilePath: c.Path, Content: c.Content})
		default:
			actions = append(actions, gitlabgit.CommitAction{Action: gitlabgit.CommitCreate, FilePath: c.Path, Content: c.Content})
		}
	}

	sha, err := gitlabgit.CommitTo(ctx, m.client, pre.params.Project, gitlabgit.CommitRequest{
		Branch:        branch,
		StartSHA:      pre.base.Commit,
		CommitMessage: gitlabgit.CommitMessage(pre.params.Title, inv.ProposalID),
		Actions:       actions,
	})
	if err != nil {
		return "", fmt.Errorf("make the commit: %w", err)
	}

	return sha, nil
}

// open makes the merge request of a proposal from the branch. When
// limits.draft is true it also checks that GitLab made it a draft, and closes
// it if it did not.
func (m *MR) open(ctx context.Context, params Params, branch string, inv action.Invocation) (*gitlabgit.MergeRequest, error) {
	req := gitlabgit.CreateRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(params.Title, m.limits.draft(), fallbackTitle),
		Description:  gitlabgit.Description(params.Body, inv.ProposalID, inv.InvestigationID, m.publicURL),
	}

	if m.limits.draft() {
		mr, err := gitlabgit.OpenDraft(ctx, m.client, params.Project, branch, req)
		if err != nil {
			return nil, fmt.Errorf("open the draft merge request: %w", err)
		}

		return mr, nil
	}

	mr, err := gitlabgit.OpenMergeRequest(ctx, m.client, params.Project, req)
	if err != nil {
		return nil, fmt.Errorf("open the merge request: %w", err)
	}

	return mr, nil
}
