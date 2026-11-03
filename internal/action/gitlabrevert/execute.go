package gitlabrevert

import (
	"context"
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// TypeName is the action type this package registers. A config selects it
// with "type: gitlab.revert".
const TypeName = "gitlab.revert"

func init() {
	registry.Actions.Register(TypeName, registry.Typed(New))
}

var _ action.Action = (*Revert)(nil)

// titleFallback is the merge request title when the model's title is nothing
// but a draft prefix.
const titleFallback = "Revert"

var errNoSnapshot = errors.New("the invocation has no snapshot to write")

// New builds the action from its validated config. It makes no network call:
// the token is read and used by the first call to GitLab.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	return newRevert(name, cfg, deps)
}

// Execute implements action.Action. The branch is named by the idempotency key,
// sidekick/<proposal id>, so a repeat after a crash finds what the first attempt
// made: a merge request from the branch, in any state, is returned as it is
// (after the check that it is a draft), and a branch without one gets its merge
// request. Otherwise it reads the commit and the files again, checks them
// against the snapshot, makes the branch from the head of the base branch, has
// GitLab revert the commit on it, and opens a draft merge request. It never
// merges, approves, assigns, asks for a review or turns on auto-merge.
func (r *Revert) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	params, err := r.params(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if !gitlabgit.BranchOfProposal.MatchString(inv.IdempotencyKey) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, gitlabgit.ErrNoBranch)
	}

	branch := inv.IdempotencyKey

	mr, branchExists, err := gitlabgit.Existing(ctx, r.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if mr != nil {
		return r.existing(ctx, params.Project, branch, mr)
	}

	pushed, done, err := r.revert(ctx, inv, params, branch, branchExists)
	if err != nil {
		r.metrics.MergeRequest(gitlabgit.ResultError)

		return action.Result{}, fmt.Errorf("revert the commit on %s: %w", branch, err)
	}

	if done != nil {
		r.metrics.MergeRequest(gitlabgit.ResultApplied)

		return *done, nil
	}

	mr, err = gitlabgit.OpenDraft(ctx, r.client, params.Project, branch, r.mergeRequestRequest(params, branch, inv))
	if err != nil {
		r.metrics.MergeRequest(gitlabgit.ResultError)

		return action.Result{}, fmt.Errorf("make the merge request from %s: %w", branch, err)
	}

	r.metrics.MergeRequest(gitlabgit.ResultOpened)
	r.log.InfoContext(ctx, "revert merge request opened", "proposal_id", inv.ProposalID, "project", params.Project, "iid", mr.IID)

	return gitlabgit.Result(params.Project, mr, gitlabgit.ResultOpened, pushed), nil
}

// existing returns the merge request of an earlier attempt, after checking that
// it is a draft.
func (r *Revert) existing(ctx context.Context, project, branch string, mr *gitlabgit.MergeRequest) (action.Result, error) {
	if err := gitlabgit.RequireDraft(ctx, r.client, project, branch, mr); err != nil {
		r.metrics.MergeRequest(gitlabgit.ResultError)

		return action.Result{}, fmt.Errorf("check the merge request of an earlier attempt: %w", err)
	}

	r.metrics.MergeRequest(gitlabgit.ResultExisting)

	return gitlabgit.Result(project, mr, gitlabgit.ResultExisting, ""), nil
}

// revert checks the revert against the snapshot, makes the branch and has
// GitLab revert the commit on it. It returns the SHA of the revert commit, which
// is empty when an earlier attempt made it already. done is set, with the
// result, when every file is back to what it was before the commit already, so
// there is nothing to revert.
func (r *Revert) revert(ctx context.Context, inv action.Invocation, params Params, branch string, branchExists bool) (pushed string, done *action.Result, err error) {
	if branchExists {
		applied, err := r.applied(ctx, params, branch)
		if err != nil {
			return "", nil, fmt.Errorf("look at the branch of an earlier attempt: %w", err)
		}

		if applied {
			return "", nil, nil
		}
	}

	if inv.Snapshot == nil {
		return "", nil, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	l, err := r.load(ctx, inv.Params)
	if err != nil {
		return "", nil, fmt.Errorf("check the revert before it is made: %w", err)
	}

	writes, err := l.applySnapshot(*inv.Snapshot)
	if err != nil {
		return "", nil, fmt.Errorf("compare with the snapshot: %w", err)
	}

	if len(writes) == 0 {
		if branchExists {
			r.discard(ctx, params.Project, branch)
		}

		return "", &action.Result{Summary: "The change is reverted on the base branch already, so no merge request was opened."}, nil
	}

	if !branchExists {
		if err := r.createBranch(ctx, params.Project, branch, l.head.Commit); err != nil {
			return "", nil, fmt.Errorf("make the branch: %w", err)
		}
	}

	pushed, err = r.revertCommit(ctx, params.Project, l.target.sha, branch, l.target.what)
	if err != nil {
		r.discard(ctx, params.Project, branch)

		return "", nil, fmt.Errorf("revert %s: %w", l.target.what, err)
	}

	return pushed, nil, nil
}

// applied reports whether an earlier attempt made the revert commit on the
// branch: the branch has one commit that the base branch lacks. A branch made
// and left empty by a crash has none.
func (r *Revert) applied(ctx context.Context, params Params, branch string) (bool, error) {
	n, err := r.commitsAhead(ctx, params.Project, params.BaseBranch, branch)
	if err != nil {
		return false, fmt.Errorf("count the commits of the branch: %w", err)
	}

	switch n {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("%w: the branch %s has %d commits that the base branch lacks, and it should have one", action.ErrPermanent, branch, n)
	}
}

// discard deletes the branch of an attempt that failed, so a repeat starts from
// the head of the base branch. A failure to delete it is logged: the repeat
// finds the branch and checks it.
func (r *Revert) discard(ctx context.Context, project, branch string) {
	if err := gitlabgit.DeleteBranch(context.WithoutCancel(ctx), r.client, project, branch); err != nil {
		r.log.WarnContext(ctx, "could not delete the branch of a failed revert", "project", project, "branch", branch, "error", err)
	}
}

// mergeRequestRequest is the draft merge request of a proposal. The draft is
// not a setting: nothing in the config can turn it off.
func (r *Revert) mergeRequestRequest(params Params, branch string, inv action.Invocation) gitlabgit.CreateRequest {
	return gitlabgit.CreateRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(params.Title, true, titleFallback),
		Description:  gitlabgit.Description(description(params, inv.ProposalID), inv.ProposalID, inv.InvestigationID, r.publicURL),
	}
}

// description is the text of the merge request before the shared footer: the
// model's reason, what is reverted (in a code span, so GitLab links and mentions
// nothing) and the line that marks the merge request as a revert of this action.
func description(params Params, proposalID string) string {
	what := "commit `" + params.Commit + "`"
	if params.MergeRequest != 0 {
		what = fmt.Sprintf("merge request `!%d`", params.MergeRequest)
	}

	return params.Body + "\n\nReverts " + what + ".\n\n" + revertTrailer + " " + proposalID
}
