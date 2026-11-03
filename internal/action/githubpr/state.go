package githubpr

import (
	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// stateOfClosed is the state GitHub gives a pull request that is not open.
const stateOfClosed = "closed"

// stateOf returns the state of a pull request as the action tells it: a
// closed pull request that has a merge time is merged.
func stateOf(pr *gh.PullRequest) string {
	switch {
	case pr.MergedAt != nil:
		return gitchange.StateMerged
	case pr.GetState() == stateOfClosed:
		return gitchange.StateClosed
	default:
		return gitchange.StateOpen
	}
}
