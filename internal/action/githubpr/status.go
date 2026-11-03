package githubpr

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

var _ gitchange.StateReporter = (*PR)(nil)

// PullRequestState implements gitchange.StateReporter. It reads the pull
// request made from branch in the repository the proposal's parameters name.
func (p *PR) PullRequestState(ctx context.Context, params json.RawMessage, branch string) (string, *gitchange.Status, error) {
	owner, name, repo, err := githubgit.RepoOfParams(params)
	if err != nil {
		return "", nil, fmt.Errorf("read the proposal: %w", err)
	}

	st, err := p.status(ctx, owner, name, branch)
	if err != nil {
		return "", nil, fmt.Errorf("read the pull request of %s: %w", repo, err)
	}

	return repo, st, nil
}

// CountCheck implements gitchange.StateReporter. It counts one check of the
// status job in sidekick_githubpr_status_checks_total.
func (p *PR) CountCheck(result string) {
	p.metrics.StatusCheck(result)
}

// status reads the pull request made from branch, in any state. It returns
// nil when there is none.
func (p *PR) status(ctx context.Context, owner, name, branch string) (*gitchange.Status, error) {
	st, err := githubgit.Status(ctx, p.client, owner, name, branch)
	if err != nil {
		return nil, fmt.Errorf("read the pull request status: %w", err)
	}

	return st, nil
}
