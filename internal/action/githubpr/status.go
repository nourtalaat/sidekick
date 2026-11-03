package githubpr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

var _ gitchange.StateReporter = (*PR)(nil)

// PullRequestState implements gitchange.StateReporter. It reads the pull
// request made from branch in the repository the proposal's parameters name.
func (p *PR) PullRequestState(ctx context.Context, params json.RawMessage, branch string) (string, *gitchange.Status, error) {
	var target struct {
		Repo string `json:"repo"`
	}

	if err := json.Unmarshal(params, &target); err != nil {
		return "", nil, fmt.Errorf("%w: read the repository of the proposal: %w", action.ErrPermanent, err)
	}

	owner, name, ok := strings.Cut(target.Repo, "/")
	if !ok || !validRepo(target.Repo) {
		return "", nil, fmt.Errorf("%w: the proposal's repository %q is not owner/name", action.ErrPermanent, target.Repo)
	}

	st, err := p.status(ctx, owner, name, branch)
	if err != nil {
		return "", nil, fmt.Errorf("read the pull request of %s: %w", target.Repo, err)
	}

	return target.Repo, st, nil
}

// CountCheck implements gitchange.StateReporter. It counts one check of the
// status job in sidekick_githubpr_status_checks_total.
func (p *PR) CountCheck(result string) {
	p.metrics.statusCheck(result)
}

// status reads the pull request made from branch, in any state. It returns
// nil when there is none.
func (p *PR) status(ctx context.Context, owner, name, branch string) (*gitchange.Status, error) {
	pr, err := p.pullRequestFrom(ctx, owner, name, branch)
	if err != nil {
		return nil, fmt.Errorf("read the pull request: %w", err)
	}

	if pr == nil {
		return nil, nil
	}

	return statusOf(pr), nil
}

// statusOf describes a pull request as the status job records it.
func statusOf(pr *gh.PullRequest) *gitchange.Status {
	s := &gitchange.Status{Number: pr.GetNumber(), URL: pr.GetHTMLURL(), State: stateOf(pr)}

	switch s.State {
	case gitchange.StateMerged:
		s.MergedAt = timeOf(pr.MergedAt)
	case gitchange.StateClosed:
		s.ClosedAt = timeOf(pr.ClosedAt)
	}

	return s
}

// timeOf returns the time of a GitHub timestamp, or nil.
func timeOf(t *gh.Timestamp) *time.Time {
	if t == nil {
		return nil
	}

	return &t.Time
}
