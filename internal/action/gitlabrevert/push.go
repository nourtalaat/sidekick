package gitlabrevert

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// createBranchRequest is the body of the call that makes a branch.
type createBranchRequest struct {
	Branch string `json:"branch"`
	Ref    string `json:"ref"`
}

// revertRequest is the body of the call that reverts a commit on a branch.
type revertRequest struct {
	Branch string `json:"branch"`
}

// commitsAhead counts the commits of the branch that the base branch lacks.
func (r *Revert) commitsAhead(ctx context.Context, project, base, branch string) (int, error) {
	var cmp struct {
		Commits []apiRef `json:"commits"`
	}

	query := url.Values{"from": {base}, "to": {branch}}

	if _, err := r.client.Get(ctx, gitlabapi.ProjectPath(project, "repository", "compare"), query, &cmp); err != nil {
		return 0, fmt.Errorf("list the commits: %w", gitlabgit.Classify(fmt.Sprintf("compare %s with %s", branch, base), err))
	}

	return len(cmp.Commits), nil
}

// createBranch makes the branch from a commit. A branch that is there already,
// made by another attempt a moment ago, is not an error: GitLab answers 400
// for it, so the branch is read again.
func (r *Revert) createBranch(ctx context.Context, project, branch, from string) error {
	err := r.client.Send(ctx, http.MethodPost, gitlabapi.ProjectPath(project, "repository", "branches"), createBranchRequest{Branch: branch, Ref: from}, nil)
	if err == nil {
		return nil
	}

	if _, lookErr := gitlabgit.BaseOf(ctx, r.client, project, branch); lookErr == nil {
		return nil
	}

	return fmt.Errorf("create the branch: %w", gitlabgit.Classify("make the branch "+branch, err))
}

// revertCommit runs GitLab's revert of a commit on the branch and returns the
// SHA of the revert commit. A 400 is GitLab saying the commit cannot be reverted
// on the branch as it is now, which is a conflict: the error wraps
// action.ErrConflict and action.ErrPermanent and carries GitLab's message.
func (r *Revert) revertCommit(ctx context.Context, project, sha, branch, what string) (string, error) {
	var made apiRef

	path := gitlabapi.ProjectPath(project, "repository", "commits", sha, "revert")

	err := r.client.Send(ctx, http.MethodPost, path, revertRequest{Branch: branch}, &made)

	switch {
	case gitlabapi.IsStatus(err, http.StatusBadRequest):
		return "", fmt.Errorf("revert %s on %s: %w: %w: %w", what, branch, action.ErrConflict, action.ErrPermanent, err)
	case err != nil:
		return "", gitlabgit.Classify(fmt.Sprintf("revert %s on %s", what, branch), err)
	default:
		return made.ID, nil
	}
}
