package githubgit

import (
	"context"
	"errors"
	"fmt"

	gh "github.com/google/go-github/v93/github"
)

var (
	// ErrNoSHA is returned when GitHub does not say which commit a branch is
	// at.
	ErrNoSHA = errors.New("GitHub did not say which commit the branch is at")
	// ErrNoTree is returned when GitHub does not say which tree a commit has.
	ErrNoTree = errors.New("GitHub did not say which tree the commit has")
)

// Base is the state of the base branch that a pull request is built on.
type Base struct {
	// Commit and Tree are the SHAs of the branch's head commit and its tree.
	Commit string
	Tree   string
}

// BaseOf reads the head commit and tree of a branch.
func BaseOf(ctx context.Context, client *gh.Client, owner, repo, branch string) (Base, error) {
	ref, _, err := client.Git.GetRef(ctx, owner, repo, "heads/"+branch)
	if err != nil {
		return Base{}, Classify(fmt.Sprintf("read branch %s of %s/%s", branch, owner, repo), err)
	}

	sha := ref.GetObject().GetSHA()
	if sha == "" {
		return Base{}, ErrNoSHA
	}

	commit, _, err := client.Git.GetCommit(ctx, owner, repo, sha)
	if err != nil {
		return Base{}, Classify("read the head commit of "+branch, err)
	}

	tree := commit.GetTree().GetSHA()
	if tree == "" {
		return Base{}, ErrNoTree
	}

	return Base{Commit: sha, Tree: tree}, nil
}
