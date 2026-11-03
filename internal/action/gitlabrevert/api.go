package gitlabrevert

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// How many merge requests of a commit, and commits of a merge request, one
// call asks for. More than a few merge requests hold one commit rarely, and a
// merge request with more than one commit is refused, so two are enough.
const (
	mergeRequestsOfCommitPageSize = 20
	mergeRequestCommitsPageSize   = 2
)

// The modes in a diff that the action cannot revert by blob: a symbolic link
// and a submodule. A new or deleted file has the mode "0" on its missing side.
const (
	modeSymlink   = "120000"
	modeSubmodule = "160000"
	modeAbsent    = "0"
)

// apiCommit is a commit as GitLab's commits endpoint answers.
type apiCommit struct {
	ID            string    `json:"id"`
	ParentIDs     []string  `json:"parent_ids"`
	CommittedDate time.Time `json:"committed_date"`
}

// apiRef is a commit that an answer names only by its ID.
type apiRef struct {
	ID string `json:"id"`
}

// apiDiff is one file of the diff of a commit.
type apiDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	AMode       string `json:"a_mode"`
	BMode       string `json:"b_mode"`
	NewFile     bool   `json:"new_file"`
	DeletedFile bool   `json:"deleted_file"`
	Collapsed   bool   `json:"collapsed"`
	TooLarge    bool   `json:"too_large"`
}

// commit reads a commit. A commit GitLab does not know is the model's
// mistake and is refused, so it reads why.
func (r *Revert) commit(ctx context.Context, project, sha, what string) (*apiCommit, error) {
	var c apiCommit

	if _, err := r.client.Get(ctx, gitlabapi.ProjectPath(project, "repository", "commits", sha), nil, &c); err != nil {
		return nil, apiFailure("read "+what, what, err)
	}

	return &c, nil
}

// mergeRequest reads a merge request by its IID.
func (r *Revert) mergeRequest(ctx context.Context, project string, iid int, what string) (*gitlabgit.MergeRequest, error) {
	var mr gitlabgit.MergeRequest

	if _, err := r.client.Get(ctx, gitlabapi.ProjectPath(project, "merge_requests", strconv.Itoa(iid)), nil, &mr); err != nil {
		return nil, apiFailure("read "+what, what, err)
	}

	return &mr, nil
}

// mergeRequestCommits reads the first commits of a merge request: as many as
// it takes to tell a merge request of one commit from one of several.
func (r *Revert) mergeRequestCommits(ctx context.Context, project string, iid int, what string) ([]apiRef, error) {
	var commits []apiRef

	path := gitlabapi.ProjectPath(project, "merge_requests", strconv.Itoa(iid), "commits")
	query := url.Values{"per_page": {strconv.Itoa(mergeRequestCommitsPageSize)}}

	if _, err := r.client.Get(ctx, path, query, &commits); err != nil {
		return nil, apiFailure("read the commits of "+what, what, err)
	}

	return commits, nil
}

// mergeRequestsOf reads the merge requests that hold a commit, in any state.
func (r *Revert) mergeRequestsOf(ctx context.Context, project, sha, what string) ([]gitlabgit.MergeRequest, error) {
	var found []gitlabgit.MergeRequest

	path := gitlabapi.ProjectPath(project, "repository", "commits", sha, "merge_requests")
	query := url.Values{"per_page": {strconv.Itoa(mergeRequestsOfCommitPageSize)}}

	if _, err := r.client.Get(ctx, path, query, &found); err != nil {
		return nil, apiFailure("list the merge requests of "+what, what, err)
	}

	return found, nil
}

// onBase reports whether the commit is on the branch: the common ancestor of
// the two is the commit itself. GitLab answers 404 when they have none.
func (r *Revert) onBase(ctx context.Context, project, sha, branch, what string) (bool, error) {
	var base apiRef

	query := url.Values{"refs[]": {sha, branch}}

	_, err := r.client.Get(ctx, gitlabapi.ProjectPath(project, "repository", "merge_base"), query, &base)

	switch {
	case gitlabapi.IsStatus(err, http.StatusNotFound):
		return false, nil
	case err != nil:
		return false, gitlabgit.Classify(fmt.Sprintf("compare %s with %s", what, branch), err)
	default:
		return base.ID == sha, nil
	}
}

// diff reads the files a commit changes, against its first parent. It asks for
// one more than limit, so that a commit over the limit is seen without reading
// every file. A diff GitLab cut short, or answered with a body too large to
// read, is refused.
func (r *Revert) diff(ctx context.Context, project, sha string, limit int, what string) ([]apiDiff, error) {
	query := url.Values{"per_page": {strconv.Itoa(limit + 1)}}

	var files []apiDiff

	if _, err := r.client.Get(ctx, gitlabapi.ProjectPath(project, "repository", "commits", sha, "diff"), query, &files); err != nil {
		if errors.Is(err, gitlabapi.ErrResponseTooLarge) {
			return nil, refuse("the diff of %s is too large to read; revert it by hand", what)
		}

		return nil, apiFailure("read the diff of "+what, what, err)
	}

	return files, nil
}
