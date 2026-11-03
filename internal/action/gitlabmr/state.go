package gitlabmr

import (
	"time"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// The states GitLab gives a merge request.
const (
	gitlabOpened = "opened"
	gitlabLocked = "locked"
	gitlabMerged = "merged"
	gitlabClosed = "closed"
)

// apiMergeRequest is a merge request as GitLab's merge requests endpoints
// answer.
type apiMergeRequest struct {
	IID    int    `json:"iid"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	WebURL string `json:"web_url"`
	// SHA is the head commit of the source branch.
	SHA string `json:"sha"`
	// MergedAt and ClosedAt are set for a merge request that was merged or
	// closed.
	MergedAt *time.Time `json:"merged_at"`
	ClosedAt *time.Time `json:"closed_at"`
}

// stateOf returns the state of a merge request as the action tells it. A
// merge request that GitLab is merging (locked) is still open.
func stateOf(mr *apiMergeRequest) string {
	switch mr.State {
	case gitlabMerged:
		return gitchange.StateMerged
	case gitlabClosed:
		return gitchange.StateClosed
	default:
		return gitchange.StateOpen
	}
}
