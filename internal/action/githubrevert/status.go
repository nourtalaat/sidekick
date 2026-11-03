package githubrevert

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

var _ gitchange.StateReporter = (*Revert)(nil)

// PullRequestState implements gitchange.StateReporter. It reads the pull
// request made from branch in the repository the proposal's parameters name.
func (r *Revert) PullRequestState(ctx context.Context, params json.RawMessage, branch string) (string, *gitchange.Status, error) {
	owner, name, repo, err := githubgit.RepoOfParams(params)
	if err != nil {
		return "", nil, fmt.Errorf("read the proposal: %w", err)
	}

	st, err := githubgit.Status(ctx, r.client, owner, name, branch)
	if err != nil {
		return "", nil, fmt.Errorf("read the pull request of %s: %w", repo, err)
	}

	return repo, st, nil
}

// CountCheck implements gitchange.StateReporter. It counts one check of the
// status job in sidekick_githubrevert_status_checks_total.
func (r *Revert) CountCheck(result string) {
	r.metrics.StatusCheck(result)
}
