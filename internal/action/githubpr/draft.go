package githubpr

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// open makes the pull request. When limits.draft is true it also checks that
// GitHub made it a draft, and closes it if it did not.
func (p *PR) open(ctx context.Context, owner, name, branch string, req gh.CreatePullRequest) (*gh.PullRequest, error) {
	if p.limits.draft() {
		pr, err := githubgit.OpenDraft(ctx, p.client, owner, name, branch, req)
		if err != nil {
			return nil, fmt.Errorf("open the draft pull request: %w", err)
		}

		return pr, nil
	}

	pr, err := githubgit.OpenPullRequest(ctx, p.client, owner, name, req)
	if err != nil {
		return nil, fmt.Errorf("open the pull request: %w", err)
	}

	return pr, nil
}

// requireDraft checks a pull request found from an earlier attempt when
// limits.draft is true: one that is not a draft is closed, as
// githubgit.RequireDraft does. When it is false, any pull request passes.
func (p *PR) requireDraft(ctx context.Context, owner, name, branch string, pr *gh.PullRequest) error {
	if !p.limits.draft() {
		return nil
	}

	if err := githubgit.RequireDraft(ctx, p.client, owner, name, branch, pr); err != nil {
		return fmt.Errorf("check that the pull request is a draft: %w", err)
	}

	return nil
}
