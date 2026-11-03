package gitlabgit

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// OpenDraft makes a merge request, as OpenMergeRequest does, and checks that
// it is a draft, as RequireDraft does. The title of req must start with the
// draft prefix.
func OpenDraft(ctx context.Context, client *gitlabapi.Client, project, branch string, req CreateRequest) (*MergeRequest, error) {
	mr, err := OpenMergeRequest(ctx, client, project, req)
	if err != nil {
		return nil, fmt.Errorf("open the merge request: %w", err)
	}

	if err := RequireDraft(ctx, client, project, branch, mr); err != nil {
		return nil, fmt.Errorf("check that the merge request is a draft: %w", err)
	}

	return mr, nil
}

// RequireDraft makes sure Sidekick never leaves a merge request that is not a
// draft, whether it just made it or found it from an earlier attempt. A merge
// request that is open and not a draft is closed. Then the branch of a closed
// merge request that is not a draft is deleted if it is still there, and the
// error wraps action.ErrPermanent. A merge request that was merged is left
// alone: a person did that. One that GitLab is merging (locked) cannot be
// closed and is left alone with the same error. A draft passes.
func RequireDraft(ctx context.Context, client *gitlabapi.Client, project, branch string, mr *MergeRequest) error {
	state := StateOf(mr)
	if mr.Draft || state == gitchange.StateMerged {
		return nil
	}

	title := project + "!" + strconv.Itoa(mr.IID)

	if mr.State == gitlabLocked {
		return fmt.Errorf("%w: %s is not a draft and GitLab is merging it, so Sidekick left it as it is", action.ErrPermanent, title)
	}

	if state == gitchange.StateOpen {
		if _, err := CloseMergeRequest(ctx, client, project, mr.IID); err != nil {
			return fmt.Errorf("close the merge request %s that is not a draft: %w", title, err)
		}
	}

	if err := DeleteBranch(ctx, client, project, branch); err != nil {
		return fmt.Errorf("delete the branch of the merge request %s that is not a draft: %w", title, err)
	}

	return fmt.Errorf("%w: %s was not a draft, so Sidekick closed it and deleted its branch", action.ErrPermanent, title)
}
