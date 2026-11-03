package githubgit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
)

// branchRef starts the name of a branch in the Git data API.
const branchRef = "heads/"

// stateOfClosed is the state GitHub gives a pull request that is not open.
const stateOfClosed = "closed"

// ErrNoNumber is returned when GitHub does not return the pull request it
// made.
var ErrNoNumber = errors.New("GitHub did not return the pull request")

// ErrNoBranch is returned when the idempotency key is not the branch name
// sidekick/<proposal id> that the action makes pull requests from.
var ErrNoBranch = errors.New("the invocation has no idempotency key to name the branch with")

// ErrNoUndoOf is returned when an undo does not say which proposal it
// undoes.
var ErrNoUndoOf = fmt.Errorf("%w: the call does not say which proposal it undoes", action.ErrPermanent)

// BranchOfProposal is the shape of the branch an action makes: sidekick/ and
// the proposal ID.
var BranchOfProposal = regexp.MustCompile(`^sidekick/[0-9a-fA-F-]{36}$`)

// UndoBranch checks the invocation of an undo and returns the branch of the
// proposal that is undone.
func UndoBranch(inv action.Invocation) (string, error) {
	if inv.UndoOf == "" {
		return "", ErrNoUndoOf
	}

	branch := action.IdempotencyKey(inv.UndoOf)
	if !BranchOfProposal.MatchString(branch) || !BranchOfProposal.MatchString(inv.IdempotencyKey) {
		return "", fmt.Errorf("%w: %w", action.ErrPermanent, ErrNoBranch)
	}

	return branch, nil
}

// Existing looks for what an earlier attempt made: whether the branch is
// there, and the pull request from it, in any state.
func Existing(ctx context.Context, client *gh.Client, owner, repo, branch string) (pr *gh.PullRequest, branchExists bool, err error) {
	_, _, err = client.Git.GetRef(ctx, owner, repo, branchRef+branch)
	if IsStatus(err, http.StatusNotFound) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, Classify("look for the branch "+branch, err)
	}

	pr, err = PullRequestFrom(ctx, client, owner, repo, branch)
	if err != nil {
		return nil, true, fmt.Errorf("look for the pull request of an earlier attempt: %w", err)
	}

	return pr, true, nil
}

// PullRequestFrom returns the pull request whose head is branch of the
// repository, open or not, or nil when there is none.
func PullRequestFrom(ctx context.Context, client *gh.Client, owner, repo, branch string) (*gh.PullRequest, error) {
	prs, _, err := client.PullRequests.List(ctx, owner, repo, &gh.PullRequestListOptions{
		Head:        owner + ":" + branch,
		State:       "all",
		ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return nil, Classify("look for the pull request from "+branch, err)
	}

	if len(prs) == 0 {
		return nil, nil
	}

	return prs[0], nil
}

// CommitTo makes one commit with the tree entries on top of the base commit
// and points the branch at it. The commit exists before the branch, so a
// crash in between leaves only objects nothing refers to. A branch that
// exists already, made by another attempt a moment ago, is left as it is.
func CommitTo(ctx context.Context, client *gh.Client, owner, repo string, b Base, entries []*gh.TreeEntry, message, branch string) error {
	tree, _, err := client.Git.CreateTree(ctx, owner, repo, b.Tree, entries)
	if err != nil {
		return Classify("make the tree", err)
	}

	commit, _, err := client.Git.CreateCommit(ctx, owner, repo, gh.Commit{
		Message: new(message),
		Tree:    &gh.Tree{SHA: tree.SHA},
		Parents: []*gh.Commit{{SHA: new(b.Commit)}},
	}, nil)
	if err != nil {
		return Classify("make the commit", err)
	}

	_, _, err = client.Git.CreateRef(ctx, owner, repo, gh.CreateRef{Ref: "refs/" + branchRef + branch, SHA: commit.GetSHA()})
	if err != nil && !AlreadyExists(err) {
		return Classify("make the branch "+branch, err)
	}

	return nil
}

// OpenPullRequest makes a pull request. A pull request that exists already,
// made by another attempt a moment ago, is returned instead.
func OpenPullRequest(ctx context.Context, client *gh.Client, owner, repo string, req gh.CreatePullRequest) (*gh.PullRequest, error) {
	pr, _, err := client.PullRequests.Create(ctx, owner, repo, req)
	if AlreadyExists(err) {
		found, ferr := PullRequestFrom(ctx, client, owner, repo, req.Head)
		if ferr != nil {
			return nil, fmt.Errorf("find the pull request made meanwhile: %w", ferr)
		}

		if found != nil {
			return found, nil
		}
	}

	if err != nil {
		return nil, Classify("open the pull request", err)
	}

	if pr.GetNumber() == 0 {
		return nil, ErrNoNumber
	}

	return pr, nil
}

// ClosePullRequest closes a pull request and returns it as GitHub now
// describes it.
func ClosePullRequest(ctx context.Context, client *gh.Client, owner, repo string, number int) (*gh.PullRequest, error) {
	closed, _, err := client.PullRequests.Edit(ctx, owner, repo, number, &gh.PullRequest{State: new(stateOfClosed)})
	if err != nil {
		return nil, Classify("close the pull request", err)
	}

	return closed, nil
}

// DeleteBranch deletes a branch. A branch that is gone already is not an
// error.
func DeleteBranch(ctx context.Context, client *gh.Client, owner, repo, branch string) error {
	_, err := client.Git.DeleteRef(ctx, owner, repo, branchRef+branch)
	if IsStatus(err, http.StatusNotFound) || IsStatus(err, http.StatusUnprocessableEntity) {
		return nil
	}

	if err != nil {
		return Classify("delete the branch "+branch, err)
	}

	return nil
}
