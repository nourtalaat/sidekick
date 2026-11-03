package gitlabcode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

var _ gitchange.StateReporter = (*Code)(nil)

// PullRequestState implements gitchange.StateReporter. It reads the merge
// request made from branch in the project the proposal's parameters name, in
// any state, with the action's own token. The project must still be one that
// limits.projects allows: a project taken out of the config is not read, and
// the error wraps action.ErrPermanent, so the daily job logs it and moves on.
func (c *Code) PullRequestState(ctx context.Context, params json.RawMessage, branch string) (string, *gitchange.Status, error) {
	project, err := gitlabgit.ProjectOfParams(params)
	if err != nil {
		return "", nil, fmt.Errorf("read the proposal: %w", err)
	}

	if !c.limits.allowsProject(project) {
		return "", nil, fmt.Errorf("%w: %w: project %q is not one of limits.projects (%s)", action.ErrPermanent, action.ErrLimit, project, strings.Join(c.limits.Projects, ", "))
	}

	st, err := gitlabgit.Status(ctx, c.client, project, branch)
	if err != nil {
		return "", nil, fmt.Errorf("read the merge request of %s: %w", project, err)
	}

	return project, st, nil
}

// CountCheck implements gitchange.StateReporter. It counts one check of the
// status job in sidekick_gitlabcode_status_checks_total.
func (c *Code) CountCheck(result string) {
	c.metrics.StatusCheck(result)
}
