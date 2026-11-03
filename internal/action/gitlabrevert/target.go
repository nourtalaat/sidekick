package gitlabrevert

import (
	"context"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// shortSHA is how many characters of a commit SHA a message shows.
const shortSHA = 12

// target is what a revert undoes: the changes a commit made to its first
// parent.
type target struct {
	// what names it in messages: the commit or the merge request.
	what string
	// reverts says what the revert undoes, for the merge request text.
	reverts string
	// sha is the commit to revert, and parent its first parent, which the
	// revert goes back to.
	sha    string
	parent string
}

// resolve finds the commit to revert and checks it: it landed on the base
// branch, not longer ago than limits.maxAge, and is not a revert made by this
// action. It reads no file.
func (r *Revert) resolve(ctx context.Context, p Params) (*target, error) {
	if p.Commit != "" {
		return r.resolveCommit(ctx, p)
	}

	return r.resolveMergeRequest(ctx, p)
}

// resolveCommit resolves a commit by its SHA. Its age is its committer date,
// which GitLab sets when it merges or squashes a merge request; Git does not
// record when a commit reached a branch.
func (r *Revert) resolveCommit(ctx context.Context, p Params) (*target, error) {
	what := "commit " + p.Commit[:shortSHA]

	c, err := r.commit(ctx, p.Project, p.Commit, what)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}

	if err := r.checkCommit(ctx, p, c, what); err != nil {
		return nil, fmt.Errorf("check %s: %w", what, err)
	}

	t, err := newTarget(what, "commit "+p.Commit, p.Commit, c)
	if err != nil {
		return nil, fmt.Errorf("read the parent of %s: %w", what, err)
	}

	return t, nil
}

// resolveMergeRequest resolves a merged merge request to the commit that holds
// the whole change: its merge commit (reverted against its first parent), or
// its squash commit, or, for a merge request merged with fast-forward, its one
// commit. A fast-forward merge request of several commits has no one commit to
// revert and is refused.
func (r *Revert) resolveMergeRequest(ctx context.Context, p Params) (*target, error) {
	what := fmt.Sprintf("merge request !%d", p.MergeRequest)

	mr, err := r.mergeRequest(ctx, p.Project, p.MergeRequest, what)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}

	if err := r.checkMergeRequest(mr, p, what); err != nil {
		return nil, fmt.Errorf("check %s: %w", what, err)
	}

	sha, err := r.commitOf(ctx, p.Project, mr, what)
	if err != nil {
		return nil, fmt.Errorf("find the commit of %s: %w", what, err)
	}

	c, err := r.commit(ctx, p.Project, sha, "the commit of "+what)
	if err != nil {
		return nil, fmt.Errorf("read the commit of %s: %w", what, err)
	}

	t, err := newTarget(what, what+", commit "+sha, sha, c)
	if err != nil {
		return nil, fmt.Errorf("read the parent of the commit of %s: %w", what, err)
	}

	return t, nil
}

// commitOf returns the SHA of the commit that holds the whole change of a
// merged merge request.
func (r *Revert) commitOf(ctx context.Context, project string, mr *gitlabgit.MergeRequest, what string) (string, error) {
	switch {
	case mr.MergeCommitSHA != "":
		return mr.MergeCommitSHA, nil
	case mr.SquashCommitSHA != "":
		return mr.SquashCommitSHA, nil
	}

	commits, err := r.mergeRequestCommits(ctx, project, mr.IID, what)
	if err != nil {
		return "", fmt.Errorf("read the commits: %w", err)
	}

	switch len(commits) {
	case 0:
		return "", refuse("GitLab gives no commit for %s", what)
	case 1:
		return commits[0].ID, nil
	default:
		return "", refuse("%s was merged by fast-forward with several commits, so no one commit holds the change; revert its commits one at a time, with commit", what)
	}
}

// checkCommit refuses a commit that is held by a revert made by this action,
// that is not on the base branch, or that landed longer ago than limits.maxAge.
func (r *Revert) checkCommit(ctx context.Context, p Params, c *apiCommit, what string) error {
	held, err := r.mergeRequestsOf(ctx, p.Project, p.Commit, what)
	if err != nil {
		return fmt.Errorf("look for the merge requests of the commit: %w", err)
	}

	for i := range held {
		if isSidekickRevert(&held[i]) {
			return errRevertOfRevert(what)
		}
	}

	onBase, err := r.onBase(ctx, p.Project, p.Commit, p.BaseBranch, what)
	if err != nil {
		return fmt.Errorf("check that the commit is on the base branch: %w", err)
	}

	if !onBase {
		return refuse("%s is not on the base branch %q", what, p.BaseBranch)
	}

	return r.checkLanded(what, c.CommittedDate)
}

// checkMergeRequest refuses a merge request that is not merged, was merged into
// another branch, is a revert made by this action, or was merged longer ago than
// limits.maxAge.
func (r *Revert) checkMergeRequest(mr *gitlabgit.MergeRequest, p Params, what string) error {
	switch {
	case gitlabgit.StateOf(mr) != gitchange.StateMerged || mr.MergedAt == nil:
		return refuse("%s is not merged, so there is nothing to revert", what)
	case mr.TargetBranch != p.BaseBranch:
		return refuse("%s was merged into %q, not into the base branch %q", what, mr.TargetBranch, p.BaseBranch)
	case isSidekickRevert(mr):
		return errRevertOfRevert(what)
	}

	return r.checkLanded(what, *mr.MergedAt)
}

// checkLanded refuses a change that landed on the base branch longer ago than
// limits.maxAge, or that GitLab gave no date for.
func (r *Revert) checkLanded(what string, landed time.Time) error {
	if landed.IsZero() {
		return refuse("GitLab gave no date for %s", what)
	}

	return checkAge(what, landed, r.now(), r.limits.maxAge())
}

// newTarget builds the target from the commit to revert. A commit with no
// parent has nothing to go back to, and one with more than two is not a commit
// Git or GitLab makes.
func newTarget(what, reverts, sha string, c *apiCommit) (*target, error) {
	switch len(c.ParentIDs) {
	case 1, 2:
	case 0:
		return nil, refuse("%s has no parent commit, so there is nothing to go back to", what)
	default:
		return nil, refuse("%s has %d parents; revert its commits one at a time", what, len(c.ParentIDs))
	}

	return &target{what: what, reverts: reverts, sha: sha, parent: c.ParentIDs[0]}, nil
}

// errRevertOfRevert is the refusal to revert a revert made by this action.
func errRevertOfRevert(what string) error {
	return refuse("%s is a revert made by this action; do not revert a revert, use gitlab.merge_request instead", what)
}
