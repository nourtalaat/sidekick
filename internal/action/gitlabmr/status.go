package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

var _ gitchange.StateReporter = (*MR)(nil)

// PullRequestState implements gitchange.StateReporter. It reads the merge
// request made from branch in the project the proposal's parameters name, in
// any state, with the action's own token. The project must still be one that
// limits.projects allows: a project taken out of the config is not read, and
// the error wraps action.ErrPermanent, so the daily job logs it and moves on.
func (m *MR) PullRequestState(ctx context.Context, params json.RawMessage, branch string) (string, *gitchange.Status, error) {
	var target struct {
		Project string `json:"project"`
	}

	if err := json.Unmarshal(params, &target); err != nil {
		return "", nil, fmt.Errorf("%w: read the project of the proposal: %w", action.ErrPermanent, err)
	}

	if !gitlabapi.ValidProject(target.Project) {
		return "", nil, fmt.Errorf("%w: the proposal's project %q is not a full project path", action.ErrPermanent, target.Project)
	}

	if !m.limits.allowsProject(target.Project) {
		return "", nil, fmt.Errorf("%w: %w: project %q is not one of limits.projects (%s)", action.ErrPermanent, action.ErrLimit, target.Project, strings.Join(m.limits.Projects, ", "))
	}

	mr, err := m.mergeRequestFrom(ctx, target.Project, branch)
	if err != nil {
		return "", nil, fmt.Errorf("read the merge request of %s: %w", target.Project, err)
	}

	if mr == nil {
		return target.Project, nil, nil
	}

	return target.Project, statusOf(mr), nil
}

// CountCheck implements gitchange.StateReporter. It counts one check of the
// status job in sidekick_gitlabmr_status_checks_total.
func (m *MR) CountCheck(result string) {
	m.metrics.statusCheck(result)
}

// statusOf describes a merge request as the status job records it: its number
// in the project (the iid), its page, its state, and when it was merged or
// closed.
func statusOf(mr *apiMergeRequest) *gitchange.Status {
	s := &gitchange.Status{Number: mr.IID, URL: mr.WebURL, State: stateOf(mr)}

	switch s.State {
	case gitchange.StateMerged:
		s.MergedAt = mr.MergedAt
	case gitchange.StateClosed:
		s.ClosedAt = mr.ClosedAt
	}

	return s
}
