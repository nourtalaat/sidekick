package gitlabgit

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// ErrNoSHA is returned when GitLab does not say which commit a branch is at.
var ErrNoSHA = errors.New("GitLab did not say which commit the branch is at")

// Base is the state of the base branch that a merge request is built on.
type Base struct {
	// Commit is the SHA of the branch's head commit.
	Commit string
}

// apiBranch is a branch as GitLab's branches endpoint answers.
type apiBranch struct {
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// BaseOf reads the head commit of a branch.
func BaseOf(ctx context.Context, client *gitlabapi.Client, project, branch string) (Base, error) {
	var b apiBranch

	if _, err := client.Get(ctx, branchPath(project, branch), nil, &b); err != nil {
		return Base{}, Classify(fmt.Sprintf("read branch %s of %s", branch, project), err)
	}

	if b.Commit.ID == "" {
		return Base{}, ErrNoSHA
	}

	return Base{Commit: b.Commit.ID}, nil
}

// branchPath returns the API path of a branch of a project.
func branchPath(project, branch string) string {
	return gitlabapi.ProjectPath(project, "repository", "branches", url.PathEscape(branch))
}
