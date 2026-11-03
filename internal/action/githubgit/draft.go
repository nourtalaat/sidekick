package githubgit

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// OpenDraft makes a pull request, as OpenPullRequest does, and checks that it
// is a draft, as RequireDraft does.
func OpenDraft(ctx context.Context, client *gh.Client, owner, repo, branch string, req gh.CreatePullRequest) (*gh.PullRequest, error) {
	pr, err := OpenPullRequest(ctx, client, owner, repo, req)
	if err != nil {
		return nil, fmt.Errorf("open the pull request: %w", err)
	}

	if err := RequireDraft(ctx, client, owner, repo, branch, pr); err != nil {
		return nil, fmt.Errorf("check that the pull request is a draft: %w", err)
	}

	return pr, nil
}

// RequireDraft makes sure Sidekick never leaves a pull request that is not a
// draft, whether it just made it or found it from an earlier attempt, for
// example when GitHub does not make drafts for a private repository on its
// plan. A pull request that is open and not a draft is closed. Then the branch
// of a closed pull request that is not a draft is deleted if it is still
// there, and the error wraps action.ErrPermanent. A pull request that was
// merged is left alone: a person did that. A draft passes.
func RequireDraft(ctx context.Context, client *gh.Client, owner, repo, branch string, pr *gh.PullRequest) error {
	state := StateOf(pr)
	if pr.GetDraft() || state == gitchange.StateMerged {
		return nil
	}

	title := fmt.Sprintf("%s/%s#%d", owner, repo, pr.GetNumber())

	if state == gitchange.StateOpen {
		if _, err := ClosePullRequest(ctx, client, owner, repo, pr.GetNumber()); err != nil {
			return fmt.Errorf("close the pull request %s that is not a draft: %w", title, err)
		}
	}

	if err := DeleteBranch(ctx, client, owner, repo, branch); err != nil {
		return fmt.Errorf("delete the branch of the pull request %s that is not a draft: %w", title, err)
	}

	return fmt.Errorf("%w: GitHub did not open %s as a draft (draft pull requests may need another plan for private repositories), so Sidekick closed it and deleted its branch", action.ErrPermanent, title)
}
