package gitlabgit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// The actions of a commit, as the Commits API names them.
const (
	CommitCreate = "create"
	CommitUpdate = "update"
	CommitDelete = "delete"
)

// listPageSize is how many merge requests the lookup by source branch asks
// for. A branch has one merge request, or a few when its earlier ones were
// closed.
const listPageSize = 20

var (
	// ErrNoBranch is returned when the idempotency key is not the branch
	// name sidekick/<proposal id> that the action makes merge requests from.
	ErrNoBranch = errors.New("the invocation has no idempotency key to name the branch with")
	// ErrNoIID is returned when GitLab does not say which merge request it
	// made.
	ErrNoIID = errors.New("GitLab did not return the merge request")
	// ErrNoUndoOf is returned when an undo does not say which proposal it
	// undoes.
	ErrNoUndoOf = fmt.Errorf("%w: the call does not say which proposal it undoes", action.ErrPermanent)
)

// BranchOfProposal is the shape of the branch an action makes: sidekick/ and
// the proposal ID.
var BranchOfProposal = regexp.MustCompile(`^sidekick/[0-9a-fA-F-]{36}$`)

// CommitRequest is the body of the call that makes a commit.
type CommitRequest struct {
	Branch        string         `json:"branch"`
	StartSHA      string         `json:"start_sha"`
	CommitMessage string         `json:"commit_message"`
	Actions       []CommitAction `json:"actions"`
}

// CommitAction is one file of a commit. Content is nil for a delete.
type CommitAction struct {
	Action   string  `json:"action"`
	FilePath string  `json:"file_path"`
	Content  *string `json:"content,omitempty"`
}

// CreateRequest is the body of the call that opens a merge request. It has
// only what the actions set: they never assign, ask for a review, approve, set
// auto-merge or squash.
type CreateRequest struct {
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
	Description  string `json:"description"`
}

// updateRequest is the body of the call that closes a merge request.
type updateRequest struct {
	StateEvent string `json:"state_event"`
}

// Existing looks for what an earlier attempt made: the merge request from the
// branch, in any state, and, only when there is none, whether the branch is
// there. A merge request that was merged has no branch any more, so branchExists
// is false whenever mr is not nil.
func Existing(ctx context.Context, client *gitlabapi.Client, project, branch string) (mr *MergeRequest, branchExists bool, err error) {
	mr, err = MergeRequestFrom(ctx, client, project, branch)
	if err != nil {
		return nil, false, fmt.Errorf("look for the merge request of an earlier attempt: %w", err)
	}

	if mr != nil {
		return mr, false, nil
	}

	branchExists, err = hasBranch(ctx, client, project, branch)
	if err != nil {
		return nil, false, fmt.Errorf("look for the branch of an earlier attempt: %w", err)
	}

	return nil, branchExists, nil
}

// MergeRequestFrom returns the merge request whose source branch is branch,
// in any state, or nil when there is none. When there are several, it prefers
// one that is open, then one that was merged, then the newest.
func MergeRequestFrom(ctx context.Context, client *gitlabapi.Client, project, branch string) (*MergeRequest, error) {
	query := url.Values{
		"source_branch": {branch},
		"state":         {"all"},
		"scope":         {"all"},
		"order_by":      {"created_at"},
		"sort":          {"desc"},
		"per_page":      {strconv.Itoa(listPageSize)},
	}

	var found []MergeRequest

	if _, err := client.Get(ctx, gitlabapi.ProjectPath(project, "merge_requests"), query, &found); err != nil {
		return nil, Classify("look for the merge request from "+branch, err)
	}

	if len(found) == 0 {
		return nil, nil
	}

	best := slices.MaxFunc(found, func(a, b MergeRequest) int { return rankOf(a.State) - rankOf(b.State) })

	return &best, nil
}

// CommitTo makes the commit and returns its SHA. GitLab answers every failure
// of the call with 400, so when it fails, the branch is read again: a branch
// that is there now was made by another attempt, and its commit is the one
// wanted, with an empty SHA.
func CommitTo(ctx context.Context, client *gitlabapi.Client, project string, req CommitRequest) (string, error) {
	var made struct {
		ID string `json:"id"`
	}

	err := client.Send(ctx, http.MethodPost, gitlabapi.ProjectPath(project, "repository", "commits"), req, &made)
	if err == nil {
		return made.ID, nil
	}

	if exists, lookErr := hasBranch(ctx, client, project, req.Branch); lookErr == nil && exists {
		return "", nil
	}

	return "", Classify("make the commit on "+req.Branch, err)
}

// OpenMergeRequest makes a merge request. One that exists already, made by
// another attempt a moment ago, is returned instead: GitLab answers 409 when
// another open merge request has the same source branch.
func OpenMergeRequest(ctx context.Context, client *gitlabapi.Client, project string, req CreateRequest) (*MergeRequest, error) {
	var mr MergeRequest

	err := client.Send(ctx, http.MethodPost, gitlabapi.ProjectPath(project, "merge_requests"), req, &mr)
	if gitlabapi.IsStatus(err, http.StatusConflict) {
		found, lookErr := MergeRequestFrom(ctx, client, project, req.SourceBranch)
		if lookErr != nil {
			return nil, fmt.Errorf("find the merge request made meanwhile: %w", lookErr)
		}

		if found != nil {
			return found, nil
		}
	}

	if err != nil {
		return nil, Classify("open the merge request", err)
	}

	if mr.IID == 0 {
		return nil, ErrNoIID
	}

	return &mr, nil
}

// CloseMergeRequest closes a merge request and returns it as GitLab now
// describes it.
func CloseMergeRequest(ctx context.Context, client *gitlabapi.Client, project string, iid int) (*MergeRequest, error) {
	var closed MergeRequest

	err := client.Send(ctx, http.MethodPut, mergeRequestPath(project, iid), updateRequest{StateEvent: "close"}, &closed)
	if err != nil {
		return nil, Classify("close the merge request", err)
	}

	return &closed, nil
}

// DeleteBranch deletes a branch. A branch that is gone already is not an
// error.
func DeleteBranch(ctx context.Context, client *gitlabapi.Client, project, branch string) error {
	err := client.Send(ctx, http.MethodDelete, branchPath(project, branch), nil, nil)
	if gitlabapi.IsStatus(err, http.StatusNotFound) {
		return nil
	}

	if err != nil {
		return Classify("delete the branch "+branch, err)
	}

	return nil
}

// Result describes the merge request for people and for the proposal. Its head
// is the head of the source branch as GitLab says it, or pushed, the commit the
// action made, when GitLab does not say. how is the result of the attempt,
// ResultOpened for a merge request this call made.
func Result(project string, mr *MergeRequest, how, pushed string) action.Result {
	title := project + "!" + strconv.Itoa(mr.IID)

	draft := ""
	if mr.Draft {
		draft = "draft "
	}

	var summary string

	switch state := StateOf(mr); {
	case how == ResultOpened:
		summary = "Opened " + draft + "merge request " + title
	case state == gitchange.StateMerged:
		summary = "Found merge request " + title + ", which has been merged since it was opened"
	case state == gitchange.StateClosed:
		summary = "Found merge request " + title + ", which has been closed since it was opened"
	default:
		summary = "Found the already opened " + draft + "merge request " + title
	}

	head := mr.SHA
	if head == "" {
		head = pushed
	}

	return action.Result{
		Summary:     summary,
		Links:       []action.Link{{Title: title, URL: mr.WebURL}},
		PullRequest: &action.PullRequestRef{Number: int64(mr.IID), HeadSHA: head},
	}
}

// hasBranch reports whether the branch is in the project.
func hasBranch(ctx context.Context, client *gitlabapi.Client, project, branch string) (bool, error) {
	var b apiBranch

	_, err := client.Get(ctx, branchPath(project, branch), nil, &b)

	switch {
	case gitlabapi.IsStatus(err, http.StatusNotFound):
		return false, nil
	case err != nil:
		return false, Classify("look for the branch "+branch, err)
	default:
		return true, nil
	}
}

// rankOf orders the states of merge requests from the same branch: open
// before merged before closed.
func rankOf(state string) int {
	switch state {
	case gitlabOpened, gitlabLocked:
		return 2
	case gitlabMerged:
		return 1
	default:
		return 0
	}
}

// mergeRequestPath returns the API path of a merge request of a project.
func mergeRequestPath(project string, iid int) string {
	return gitlabapi.ProjectPath(project, "merge_requests", strconv.Itoa(iid))
}
