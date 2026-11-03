package gitlabmr

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
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// The actions of a commit, as the Commits API names them.
const (
	commitCreate = "create"
	commitUpdate = "update"
	commitDelete = "delete"
)

// fallbackTitle is the title of a merge request whose given title is nothing
// but a draft prefix.
const fallbackTitle = "Update monitor definitions"

// listPageSize is how many merge requests the lookup by source branch asks
// for. A branch has one merge request, or a few when its earlier ones were
// closed.
const listPageSize = 20

var (
	// errNoBranch is returned when the idempotency key is not the branch
	// name sidekick/<proposal id> that the action makes merge requests from.
	errNoBranch = errors.New("the invocation has no idempotency key to name the branch with")
	// errNoIID is returned when GitLab does not say which merge request it
	// made.
	errNoIID = errors.New("GitLab did not return the merge request")
)

// branchOfProposal is the shape of the branch the action makes: sidekick/
// and the proposal ID.
var branchOfProposal = regexp.MustCompile(`^sidekick/[0-9a-fA-F-]{36}$`)

// commitRequest is the body of the call that makes a commit.
type commitRequest struct {
	Branch        string         `json:"branch"`
	StartSHA      string         `json:"start_sha"`
	CommitMessage string         `json:"commit_message"`
	Actions       []commitAction `json:"actions"`
}

// commitAction is one file of a commit. Content is nil for a delete.
type commitAction struct {
	Action   string  `json:"action"`
	FilePath string  `json:"file_path"`
	Content  *string `json:"content,omitempty"`
}

// createRequest is the body of the call that opens a merge request. It has
// only what the action sets: it never assigns, asks for a review, approves,
// sets auto-merge or squash.
type createRequest struct {
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
	Description  string `json:"description"`
}

// updateRequest is the body of the call that closes a merge request.
type updateRequest struct {
	StateEvent string `json:"state_event"`
}

// Execute implements action.Action. The branch is named by the idempotency
// key, sidekick/<proposal id>, so a repeat after a crash finds what the first
// attempt made. It looks for a merge request from the branch in any state
// first, and returns the one it finds as it is, even when the branch is gone
// because the merge request was merged. A branch without a merge request gets
// one. Otherwise it checks the files again, writes them as one commit that
// makes the branch, and opens the merge request.
func (m *MR) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	if err := m.Validate(ctx, inv.Params); err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if !branchOfProposal.MatchString(inv.IdempotencyKey) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoBranch)
	}

	branch := inv.IdempotencyKey

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("decode the parameters: %w", err)
	}

	mr, err := m.mergeRequestFrom(ctx, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if mr != nil {
		m.metrics.mergeRequest(resultExisting)

		return m.result(params.Project, mr, resultExisting, ""), nil
	}

	var pushed string

	branchExists, err := m.branchExists(ctx, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the branch of an earlier attempt: %w", err)
	}

	if !branchExists {
		pre, err := m.prepare(ctx, inv.Params, inv.Snapshot)
		if err != nil {
			m.metrics.mergeRequest(resultError)

			return action.Result{}, fmt.Errorf("check the merge request before it is made: %w", err)
		}

		if len(pre.changes) == 0 {
			m.metrics.mergeRequest(resultApplied)

			return action.Result{Summary: "The change is on the base branch already, so no merge request was opened."}, nil
		}

		pushed, err = m.push(ctx, pre, branch, inv)
		if err != nil {
			m.metrics.mergeRequest(resultError)

			return action.Result{}, fmt.Errorf("push the branch: %w", err)
		}
	}

	mr, err = m.open(ctx, params, branch, inv)
	if err != nil {
		m.metrics.mergeRequest(resultError)

		return action.Result{}, fmt.Errorf("make the merge request from %s: %w", branch, err)
	}

	m.metrics.mergeRequest(resultOpened)
	m.log.InfoContext(ctx, "merge request opened", "proposal_id", inv.ProposalID, "project", params.Project, "iid", mr.IID)

	return m.result(params.Project, mr, resultOpened, pushed), nil
}

// mergeRequestFrom returns the merge request whose source branch is branch,
// in any state, or nil when there is none. When there are several, it prefers
// one that is open, then one that was merged, then the newest.
func (m *MR) mergeRequestFrom(ctx context.Context, project, branch string) (*apiMergeRequest, error) {
	query := url.Values{
		"source_branch": {branch},
		"state":         {"all"},
		"scope":         {"all"},
		"order_by":      {"created_at"},
		"sort":          {"desc"},
		"per_page":      {strconv.Itoa(listPageSize)},
	}

	var found []apiMergeRequest

	if _, err := m.client.Get(ctx, gitlabapi.ProjectPath(project, "merge_requests"), query, &found); err != nil {
		return nil, gitlabgit.Classify("look for the merge request from "+branch, err)
	}

	if len(found) == 0 {
		return nil, nil
	}

	best := slices.MaxFunc(found, func(a, b apiMergeRequest) int { return rankOf(a.State) - rankOf(b.State) })

	return &best, nil
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

// branchExists reports whether the branch is in the project.
func (m *MR) branchExists(ctx context.Context, project, branch string) (bool, error) {
	var b apiBranch

	_, err := m.client.Get(ctx, branchPath(project, branch), nil, &b)

	switch {
	case gitlabapi.IsStatus(err, http.StatusNotFound):
		return false, nil
	case err != nil:
		return false, gitlabgit.Classify("look for the branch "+branch, err)
	default:
		return true, nil
	}
}

// push writes the changes as one commit that makes the branch from the head
// of the base branch. The branch and its commit appear together, so a crash
// never leaves an empty branch. The parent is the commit the files were read
// at, so a base branch that moved meanwhile does not change what the commit
// is built on. It returns the SHA of the commit. A branch that exists already,
// made by another attempt a moment ago, is left as it is, and the SHA is empty.
func (m *MR) push(ctx context.Context, pre *prepared, branch string, inv action.Invocation) (string, error) {
	actions := make([]commitAction, 0, len(pre.changes))

	for _, c := range pre.changes {
		switch {
		case c.Deleted():
			actions = append(actions, commitAction{Action: commitDelete, FilePath: c.Path})
		case pre.files[c.Path].Exists:
			actions = append(actions, commitAction{Action: commitUpdate, FilePath: c.Path, Content: c.Content})
		default:
			actions = append(actions, commitAction{Action: commitCreate, FilePath: c.Path, Content: c.Content})
		}
	}

	return m.commitTo(ctx, pre.params.Project, commitRequest{
		Branch:        branch,
		StartSHA:      pre.base.Commit,
		CommitMessage: gitlabgit.CommitMessage(pre.params.Title, inv.ProposalID),
		Actions:       actions,
	})
}

// commitTo makes the commit and returns its SHA. GitLab answers every failure of the call with
// 400, so when it fails, the branch is read again: a branch that is there now
// was made by another attempt, and its commit is the one wanted.
func (m *MR) commitTo(ctx context.Context, project string, req commitRequest) (string, error) {
	var made struct {
		ID string `json:"id"`
	}

	err := m.client.Send(ctx, http.MethodPost, gitlabapi.ProjectPath(project, "repository", "commits"), req, &made)
	if err == nil {
		return made.ID, nil
	}

	if exists, lookErr := m.branchExists(ctx, project, req.Branch); lookErr == nil && exists {
		return "", nil
	}

	return "", gitlabgit.Classify("make the commit on "+req.Branch, err)
}

// open makes the merge request of a proposal from the branch.
func (m *MR) open(ctx context.Context, params Params, branch string, inv action.Invocation) (*apiMergeRequest, error) {
	return m.openMergeRequest(ctx, params.Project, createRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(params.Title, m.limits.draft(), fallbackTitle),
		Description:  gitlabgit.Description(params.Body, inv.ProposalID, inv.InvestigationID, m.publicURL),
	})
}

// openMergeRequest makes a merge request. One that exists already, made by
// another attempt a moment ago, is returned instead: GitLab answers 409 when
// another open merge request has the same source branch.
func (m *MR) openMergeRequest(ctx context.Context, project string, req createRequest) (*apiMergeRequest, error) {
	var mr apiMergeRequest

	err := m.client.Send(ctx, http.MethodPost, gitlabapi.ProjectPath(project, "merge_requests"), req, &mr)
	if gitlabapi.IsStatus(err, http.StatusConflict) {
		found, lookErr := m.mergeRequestFrom(ctx, project, req.SourceBranch)
		if lookErr != nil {
			return nil, fmt.Errorf("find the merge request made meanwhile: %w", lookErr)
		}

		if found != nil {
			return found, nil
		}
	}

	if err != nil {
		return nil, gitlabgit.Classify("open the merge request", err)
	}

	if mr.IID == 0 {
		return nil, errNoIID
	}

	return &mr, nil
}

// result describes the merge request for people and for the proposal. Its
// head is the head of the source branch as GitLab says it, or the commit the
// action made when GitLab does not say.
func (m *MR) result(project string, mr *apiMergeRequest, how, pushed string) action.Result {
	title := project + "!" + strconv.Itoa(mr.IID)

	draft := ""
	if mr.Draft {
		draft = "draft "
	}

	var summary string

	switch state := stateOf(mr); {
	case how == resultOpened:
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

// apiBranch is a branch as GitLab's branches endpoint answers.
type apiBranch struct {
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// branchPath returns the API path of a branch of a project.
func branchPath(project, branch string) string {
	return gitlabapi.ProjectPath(project, "repository", "branches", url.PathEscape(branch))
}
