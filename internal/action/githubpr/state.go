package githubpr

import gh "github.com/google/go-github/v93/github"

// The states of a pull request, as pull_request_states.state holds them.
const (
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"
)

// stateOfClosed is the state GitHub gives a pull request that is not open.
const stateOfClosed = "closed"

// stateOf returns the state of a pull request as the action tells it: a
// closed pull request that has a merge time is merged.
func stateOf(pr *gh.PullRequest) string {
	switch {
	case pr.MergedAt != nil:
		return StateMerged
	case pr.GetState() == stateOfClosed:
		return StateClosed
	default:
		return StateOpen
	}
}
