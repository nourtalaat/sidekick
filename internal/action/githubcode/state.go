package githubcode

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

var _ gitchange.StateReporter = (*Code)(nil)

// PullRequestState implements gitchange.StateReporter. It reads the pull
// request made from branch in the repository the proposal's parameters name.
func (c *Code) PullRequestState(ctx context.Context, params json.RawMessage, branch string) (string, *gitchange.Status, error) {
	owner, name, repo, err := githubgit.RepoOfParams(params)
	if err != nil {
		return "", nil, fmt.Errorf("read the proposal: %w", err)
	}

	st, err := githubgit.Status(ctx, c.client, owner, name, branch)
	if err != nil {
		return "", nil, fmt.Errorf("read the pull request of %s: %w", repo, err)
	}

	return repo, st, nil
}

// CountCheck implements gitchange.StateReporter. It counts one check of the
// status job in sidekick_githubcode_status_checks_total.
func (c *Code) CountCheck(result string) {
	c.metrics.StatusCheck(result)
}
