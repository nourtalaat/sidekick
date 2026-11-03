package githubpr

import (
	"context"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// open makes the pull request. When limits.draft is true it also checks that
// GitHub made it a draft, and closes it if it did not.
func (p *PR) open(ctx context.Context, owner, name, branch string, req gh.CreatePullRequest) (*gh.PullRequest, error) {
	if p.limits.draft() {
		return githubgit.OpenDraft(ctx, p.client, owner, name, branch, req)
	}

	return githubgit.OpenPullRequest(ctx, p.client, owner, name, req)
}

// requireDraft checks a pull request found from an earlier attempt when
// limits.draft is true: one that is not a draft is closed, as
// githubgit.RequireDraft does. When it is false, any pull request passes.
func (p *PR) requireDraft(ctx context.Context, owner, name, branch string, pr *gh.PullRequest) error {
	if !p.limits.draft() {
		return nil
	}

	return githubgit.RequireDraft(ctx, p.client, owner, name, branch, pr)
}
