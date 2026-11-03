package gitlabgit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// The states GitLab gives a merge request.
const (
	gitlabOpened = "opened"
	gitlabLocked = "locked"
	gitlabMerged = "merged"
	gitlabClosed = "closed"
)

// MergeRequest is a merge request as GitLab's merge requests endpoints
// answer.
type MergeRequest struct {
	IID          int    `json:"iid"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	WebURL       string `json:"web_url"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Description  string `json:"description"`
	// SHA is the head commit of the source branch.
	SHA string `json:"sha"`
	// MergeCommitSHA and SquashCommitSHA are set for a merge request that was
	// merged with a merge commit or squashed.
	MergeCommitSHA  string `json:"merge_commit_sha"`
	SquashCommitSHA string `json:"squash_commit_sha"`
	// MergedAt and ClosedAt are set for a merge request that was merged or
	// closed.
	MergedAt *time.Time `json:"merged_at"`
	ClosedAt *time.Time `json:"closed_at"`
}

// StateOf returns the state of a merge request as the actions tell it. A
// merge request that GitLab is merging (locked) is still open.
func StateOf(mr *MergeRequest) string {
	switch mr.State {
	case gitlabMerged:
		return gitchange.StateMerged
	case gitlabClosed:
		return gitchange.StateClosed
	default:
		return gitchange.StateOpen
	}
}

// StatusOf describes a merge request as the status job records it: its number
// in the project (the iid), its page, its state, and when it was merged or
// closed.
func StatusOf(mr *MergeRequest) *gitchange.Status {
	s := &gitchange.Status{Number: mr.IID, URL: mr.WebURL, State: StateOf(mr)}

	switch s.State {
	case gitchange.StateMerged:
		s.MergedAt = mr.MergedAt
	case gitchange.StateClosed:
		s.ClosedAt = mr.ClosedAt
	}

	return s
}

// Status reads the merge request made from branch, in any state. It returns
// nil when there is none.
func Status(ctx context.Context, client *gitlabapi.Client, project, branch string) (*gitchange.Status, error) {
	mr, err := MergeRequestFrom(ctx, client, project, branch)
	if err != nil {
		return nil, fmt.Errorf("read the merge request: %w", err)
	}

	if mr == nil {
		return nil, nil
	}

	return StatusOf(mr), nil
}

// ProjectOfParams reads the project, as its full path, from the parameters of
// a proposal. An error wraps action.ErrPermanent, because the same parameters
// fail again. It does not check the project against the config.
func ProjectOfParams(params json.RawMessage) (string, error) {
	var target struct {
		Project string `json:"project"`
	}

	if err := json.Unmarshal(params, &target); err != nil {
		return "", fmt.Errorf("%w: read the project of the proposal: %w", action.ErrPermanent, err)
	}

	if !gitlabapi.ValidProject(target.Project) {
		return "", fmt.Errorf("%w: the proposal's project %q is not a full project path", action.ErrPermanent, target.Project)
	}

	return target.Project, nil
}
