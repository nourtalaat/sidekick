package githubgit

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

// StateOf returns the state of a pull request as the actions tell it: a
// closed pull request that has a merge time is merged.
func StateOf(pr *gh.PullRequest) string {
	switch {
	case pr.MergedAt != nil:
		return gitchange.StateMerged
	case pr.GetState() == stateOfClosed:
		return gitchange.StateClosed
	default:
		return gitchange.StateOpen
	}
}

// StatusOf describes a pull request as the status job records it.
func StatusOf(pr *gh.PullRequest) *gitchange.Status {
	s := &gitchange.Status{Number: pr.GetNumber(), URL: pr.GetHTMLURL(), State: StateOf(pr)}

	switch s.State {
	case gitchange.StateMerged:
		s.MergedAt = timeOf(pr.MergedAt)
	case gitchange.StateClosed:
		s.ClosedAt = timeOf(pr.ClosedAt)
	}

	return s
}

// Status reads the pull request made from branch, in any state. It returns
// nil when there is none.
func Status(ctx context.Context, client *gh.Client, owner, repo, branch string) (*gitchange.Status, error) {
	pr, err := PullRequestFrom(ctx, client, owner, repo, branch)
	if err != nil {
		return nil, fmt.Errorf("read the pull request: %w", err)
	}

	if pr == nil {
		return nil, nil
	}

	return StatusOf(pr), nil
}

// RepoOfParams reads the repository, as owner/name, from the parameters of a
// proposal. An error wraps action.ErrPermanent, because the same parameters
// fail again.
func RepoOfParams(params json.RawMessage) (owner, name, repo string, err error) {
	var target struct {
		Repo string `json:"repo"`
	}

	if err := json.Unmarshal(params, &target); err != nil {
		return "", "", "", fmt.Errorf("%w: read the repository of the proposal: %w", action.ErrPermanent, err)
	}

	owner, name, ok := strings.Cut(target.Repo, "/")
	if !ok || !ValidRepo(target.Repo) {
		return "", "", "", fmt.Errorf("%w: the proposal's repository %q is not owner/name", action.ErrPermanent, target.Repo)
	}

	return owner, name, target.Repo, nil
}

// timeOf returns the time of a GitHub timestamp, or nil.
func timeOf(t *gh.Timestamp) *time.Time {
	if t == nil {
		return nil
	}

	return &t.Time
}
