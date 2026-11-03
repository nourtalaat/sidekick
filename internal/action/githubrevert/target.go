package githubrevert

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// shortSHA is how many characters of a commit SHA a message shows.
const shortSHA = 12

// The statuses of repos/compare-commits that say the first commit is on the
// branch: the branch is ahead of it, or at it.
const (
	statusAhead     = "ahead"
	statusIdentical = "identical"
)

// firstPage asks for the first page of a list with one item, or for as much
// as one page of a long list holds, when only its length matters.
var (
	onePerPage     = &gh.ListOptions{PerPage: 1}
	hundredPerPage = &gh.ListOptions{PerPage: 100}
)

// target is what a revert undoes: the changes a commit made to its first
// parent.
type target struct {
	// what names it in messages: the commit or the pull request.
	what string
	// reverts says what the revert undoes, for its commit message: the
	// commit, or the pull request with its merge commit.
	reverts string
	// subject is the first line of the commit message, or the title of the
	// pull request, for the title of the revert.
	subject string
	// parent is the SHA of the first parent of the commit, which the revert
	// goes back to; commitTree and parentTree are the trees of the commit
	// and of its first parent.
	parent     string
	commitTree string
	parentTree string
	// whole is set for a pull request of several commits whose merge commit
	// has one parent: it is the pull request to prove the merge commit
	// holds the whole change of.
	whole *pullRequest
}

// pullRequest is what holdsWholeChange needs of a pull request.
type pullRequest struct {
	number  int
	headSHA string
}

// resolve finds the commit to revert and checks it: it landed on the base
// branch, not longer ago than limits.maxAge, and is not a revert made by
// this action. It reads no file. With undo set, it finds the commit and
// checks nothing else.
func (r *Revert) resolve(ctx context.Context, owner, name string, p Params, undo bool) (*target, error) {
	if p.Commit != "" {
		return r.resolveCommit(ctx, owner, name, p, undo)
	}

	return r.resolvePullRequest(ctx, owner, name, p, undo)
}

// resolveCommit resolves a commit by its SHA. Its age is the time in its
// committer date, which GitHub sets when it merges, squashes or rebases a
// pull request; Git does not record when a commit reached a branch.
func (r *Revert) resolveCommit(ctx context.Context, owner, name string, p Params, undo bool) (*target, error) {
	what := "commit " + p.Commit[:shortSHA]

	c, _, err := r.client.Git.GetCommit(ctx, owner, name, p.Commit)
	if err != nil {
		return nil, readFailure("read "+what, what, err)
	}

	if !undo {
		if err := r.checkCommit(ctx, owner, name, p, c, what); err != nil {
			return nil, fmt.Errorf("check %s: %w", what, err)
		}
	}

	t := &target{what: what, reverts: "commit " + p.Commit, subject: firstLine(c.GetMessage())}

	if err := r.fillParent(ctx, owner, name, t, c); err != nil {
		return nil, fmt.Errorf("read the parent of %s: %w", what, err)
	}

	return t, nil
}

// resolvePullRequest resolves a merged pull request to its merge commit. A
// merge commit is reverted against its first parent. A squash commit, and the
// one commit of a pull request merged by rebase, have one parent and hold
// the whole change; a pull request of several commits whose merge commit has
// one parent is a squash or a rebase, which holdsWholeChange tells apart
// once the files are known.
func (r *Revert) resolvePullRequest(ctx context.Context, owner, name string, p Params, undo bool) (*target, error) {
	what := fmt.Sprintf("pull request #%d", p.PullRequest)

	pr, _, err := r.client.PullRequests.Get(ctx, owner, name, p.PullRequest)
	if err != nil {
		return nil, readFailure("read "+what, what, err)
	}

	switch {
	case !pr.GetMerged() || pr.MergedAt == nil:
		return nil, refuse("%s is not merged, so there is nothing to revert", what)
	case pr.GetBase().GetRef() != p.BaseBranch:
		return nil, refuse("%s was merged into %q, not into the base branch %q", what, pr.GetBase().GetRef(), p.BaseBranch)
	case pr.GetMergeCommitSHA() == "":
		return nil, refuse("GitHub gives no merge commit for %s", what)
	}

	if !undo {
		if err := r.checkPullRequest(pr, what); err != nil {
			return nil, fmt.Errorf("check %s: %w", what, err)
		}
	}

	c, _, err := r.client.Git.GetCommit(ctx, owner, name, pr.GetMergeCommitSHA())
	if err != nil {
		return nil, githubgit.Classify("read the merge commit of "+what, err)
	}

	if !undo && hasRevertTrailer(c.GetMessage()) {
		return nil, errRevertOfRevert(what)
	}

	t := &target{what: what, reverts: fmt.Sprintf("%s, merge commit %s", what, pr.GetMergeCommitSHA()), subject: pr.GetTitle()}

	if len(c.Parents) == 1 && pr.GetCommits() > 1 {
		t.whole = &pullRequest{number: p.PullRequest, headSHA: pr.GetHead().GetSHA()}
	}

	if err := r.fillParent(ctx, owner, name, t, c); err != nil {
		return nil, fmt.Errorf("read the parent of the merge commit of %s: %w", what, err)
	}

	return t, nil
}

// checkCommit refuses a commit that is a revert made by this action, that is
// not on the base branch, or that landed longer ago than limits.maxAge.
func (r *Revert) checkCommit(ctx context.Context, owner, name string, p Params, c *gh.Commit, what string) error {
	if hasRevertTrailer(c.GetMessage()) {
		return errRevertOfRevert(what)
	}

	if err := r.onBase(ctx, owner, name, p.Commit, p.BaseBranch, what); err != nil {
		return fmt.Errorf("check that the commit is on the base branch: %w", err)
	}

	return r.checkLanded(what, c.GetCommitter().GetDate().Time)
}

// checkPullRequest refuses a pull request whose description marks it as a
// revert made by this action, or that was merged longer ago than
// limits.maxAge.
func (r *Revert) checkPullRequest(pr *gh.PullRequest, what string) error {
	if hasRevertTrailer(pr.GetBody()) {
		return errRevertOfRevert(what)
	}

	return r.checkLanded(what, pr.MergedAt.Time)
}

// fillParent sets the first parent of the commit and the trees of both. A
// commit with no parent has nothing to go back to, and one with more than
// two is not a commit Git or GitHub makes.
func (r *Revert) fillParent(ctx context.Context, owner, name string, t *target, c *gh.Commit) error {
	switch len(c.Parents) {
	case 1, 2:
	case 0:
		return refuse("%s has no parent commit, so there is nothing to go back to", t.what)
	default:
		return refuse("%s has %d parents; revert its commits one at a time", t.what, len(c.Parents))
	}

	t.parent = c.Parents[0].GetSHA()
	t.commitTree = c.GetTree().GetSHA()

	if t.parent == "" || t.commitTree == "" {
		return fmt.Errorf("%w: GitHub did not say which parent or tree %s has", action.ErrPermanent, t.what)
	}

	parent, _, err := r.client.Git.GetCommit(ctx, owner, name, t.parent)
	if err != nil {
		return githubgit.Classify("read the parent of "+t.what, err)
	}

	t.parentTree = parent.GetTree().GetSHA()
	if t.parentTree == "" {
		return fmt.Errorf("%w: GitHub did not say which tree the parent of %s has", action.ErrPermanent, t.what)
	}

	return nil
}

// onBase refuses a commit that is not on the base branch: the branch must
// be ahead of it or at it.
func (r *Revert) onBase(ctx context.Context, owner, name, sha, branch, what string) error {
	cmp, _, err := r.client.Repositories.CompareCommits(ctx, owner, name, sha, branch, onePerPage)
	if err != nil {
		return readFailure(fmt.Sprintf("compare %s with %s", what, branch), what, err)
	}

	if status := cmp.GetStatus(); status != statusAhead && status != statusIdentical {
		return refuse("%s is not on the base branch %q (the branch is %s it)", what, branch, cmp.GetStatus())
	}

	return nil
}

// checkLanded refuses a change that landed on the base branch longer ago
// than limits.maxAge, or that GitHub gave no date for.
func (r *Revert) checkLanded(what string, landed time.Time) error {
	if landed.IsZero() {
		return refuse("GitHub gave no date for %s", what)
	}

	return checkAge(what, landed, r.now(), r.limits.maxAge())
}

// errRevertOfRevert is the refusal to revert a revert made by this action.
func errRevertOfRevert(what string) error {
	return refuse("%s is a revert made by this action; do not revert a revert, use github.code_change instead", what)
}

// firstLine returns the first line of a commit message.
func firstLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")

	return strings.TrimSpace(line)
}

// readFailure turns a failed read into an error. A commit or pull request
// that GitHub does not know is the model's mistake and is refused, so it
// reads why; anything else is classified as a failure of the call.
func readFailure(doing, what string, err error) error {
	if githubgit.IsStatus(err, http.StatusNotFound) {
		return refuse("%s was not found in the repository", what)
	}

	return githubgit.Classify(doing, err)
}
