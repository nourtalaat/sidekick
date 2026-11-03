package gitlabmr

import (
	"context"

	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// openRevert makes the revert merge request. When limits.draft is true it also
// checks that GitLab made it a draft, and closes it if it did not.
func (m *MR) openRevert(ctx context.Context, project, branch string, req gitlabgit.CreateRequest) (*gitlabgit.MergeRequest, error) {
	if m.limits.draft() {
		return gitlabgit.OpenDraft(ctx, m.client, project, branch, req)
	}

	return gitlabgit.OpenMergeRequest(ctx, m.client, project, req)
}

// requireDraft checks a merge request found from an earlier attempt when
// limits.draft is true: one that is not a draft is closed, as
// gitlabgit.RequireDraft does. When it is false, any merge request passes.
func (m *MR) requireDraft(ctx context.Context, project, branch string, mr *gitlabgit.MergeRequest) error {
	if !m.limits.draft() {
		return nil
	}

	return gitlabgit.RequireDraft(ctx, m.client, project, branch, mr)
}
