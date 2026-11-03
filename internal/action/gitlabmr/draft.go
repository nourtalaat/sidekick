package gitlabmr

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// openRevert makes the revert merge request. When limits.draft is true it also
// checks that GitLab made it a draft, and closes it if it did not.
func (m *MR) openRevert(ctx context.Context, project, branch string, req gitlabgit.CreateRequest) (*gitlabgit.MergeRequest, error) {
	if m.limits.draft() {
		mr, err := gitlabgit.OpenDraft(ctx, m.client, project, branch, req)
		if err != nil {
			return nil, fmt.Errorf("open the draft merge request: %w", err)
		}

		return mr, nil
	}

	mr, err := gitlabgit.OpenMergeRequest(ctx, m.client, project, req)
	if err != nil {
		return nil, fmt.Errorf("open the merge request: %w", err)
	}

	return mr, nil
}

// requireDraft checks a merge request found from an earlier attempt when
// limits.draft is true: one that is not a draft is closed, as
// gitlabgit.RequireDraft does. When it is false, any merge request passes.
func (m *MR) requireDraft(ctx context.Context, project, branch string, mr *gitlabgit.MergeRequest) error {
	if !m.limits.draft() {
		return nil
	}

	if err := gitlabgit.RequireDraft(ctx, m.client, project, branch, mr); err != nil {
		return fmt.Errorf("check that the merge request is a draft: %w", err)
	}

	return nil
}
