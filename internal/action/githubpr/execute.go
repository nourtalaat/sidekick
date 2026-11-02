package githubpr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// File mode of every file the action writes: a regular, non-executable file.
const (
	fileMode  = "100644"
	blobType  = "blob"
	branchRef = "heads/"
)

// maxCommitTitleBytes is the longest first line of the commit message.
const maxCommitTitleBytes = 72

// maxPullRequestBodyBytes is the most of a description the action sends;
// GitHub refuses more than 65536 characters.
const maxPullRequestBodyBytes = 60000

// errNoBranch is returned when the idempotency key is not the branch name
// sidekick/<proposal id> that the action makes pull requests from.
var errNoBranch = errors.New("the invocation has no idempotency key to name the branch with")

// branchOfProposal is the shape of the branch the action makes: sidekick/
// and the proposal ID.
var branchOfProposal = regexp.MustCompile(`^sidekick/[0-9a-fA-F-]{36}$`)

// Execute implements action.Action. The branch is named by the idempotency
// key, sidekick/<proposal id>, so a repeat after a crash finds what the
// first attempt made: a pull request from the branch is returned as it is,
// and a branch without one gets its pull request. Otherwise it checks the
// files again, writes them as one commit on a new branch and opens the pull
// request.
func (p *PR) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	if err := p.Validate(ctx, inv.Params); err != nil {
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

	owner, name, _ := strings.Cut(params.Repo, "/")

	pr, branchExists, err := p.existing(ctx, owner, name, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if pr != nil {
		p.metrics.pullRequest(resultExisting)

		return p.result(owner, name, pr, resultExisting), nil
	}

	if !branchExists {
		pre, err := p.prepare(ctx, inv.Params, inv.Snapshot)
		if err != nil {
			p.metrics.pullRequest(resultError)

			return action.Result{}, fmt.Errorf("check the pull request before it is made: %w", err)
		}

		if len(pre.changes) == 0 {
			p.metrics.pullRequest(resultApplied)

			return action.Result{Summary: "The change is on the base branch already, so no pull request was opened."}, nil
		}

		if err := p.push(ctx, pre, branch, inv); err != nil {
			p.metrics.pullRequest(resultError)

			return action.Result{}, fmt.Errorf("push the branch: %w", err)
		}
	}

	pr, err = p.open(ctx, params, owner, name, branch, inv)
	if err != nil {
		p.metrics.pullRequest(resultError)

		return action.Result{}, fmt.Errorf("make the pull request from %s: %w", branch, err)
	}

	p.metrics.pullRequest(resultOpened)
	p.log.InfoContext(ctx, "pull request opened", "proposal_id", inv.ProposalID, "repo", params.Repo, "number", pr.GetNumber())

	return p.result(owner, name, pr, resultOpened), nil
}

// existing looks for what an earlier attempt made: whether the branch is
// there, and the pull request from it, in any state.
func (p *PR) existing(ctx context.Context, owner, name, branch string) (pr *gh.PullRequest, branchExists bool, err error) {
	_, _, err = p.client.Git.GetRef(ctx, owner, name, branchRef+branch)
	if isStatus(err, http.StatusNotFound) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, classify("look for the branch "+branch, err)
	}

	pr, err = p.pullRequestFrom(ctx, owner, name, branch)
	if err != nil {
		return nil, true, fmt.Errorf("look for the pull request of an earlier attempt: %w", err)
	}

	return pr, true, nil
}

// pullRequestFrom returns the pull request whose head is branch of the
// repository, open or not, or nil when there is none.
func (p *PR) pullRequestFrom(ctx context.Context, owner, name, branch string) (*gh.PullRequest, error) {
	prs, _, err := p.client.PullRequests.List(ctx, owner, name, &gh.PullRequestListOptions{
		Head:        owner + ":" + branch,
		State:       "all",
		ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return nil, classify("look for the pull request from "+branch, err)
	}

	if len(prs) == 0 {
		return nil, nil
	}

	return prs[0], nil
}

// push writes the changes as one commit on top of the base branch's head
// and points the new branch at it.
func (p *PR) push(ctx context.Context, pre *prepared, branch string, inv action.Invocation) error {
	entries := make([]*gh.TreeEntry, 0, len(pre.changes))

	for _, c := range pre.changes {
		entry := &gh.TreeEntry{Path: new(c.path)}

		if !c.deleted() {
			entry.Mode, entry.Type, entry.Content = new(fileMode), new(blobType), c.content
		}

		entries = append(entries, entry)
	}

	return p.commitTo(ctx, pre.owner, pre.name, pre.base, entries, commitMessage(pre.params.Title, inv.ProposalID), branch)
}

// commitTo makes one commit with the tree entries on top of the base commit
// and points the branch at it. The commit exists before the branch, so a
// crash in between leaves only objects nothing refers to. A branch that
// exists already, made by another attempt a moment ago, is left as it is.
func (p *PR) commitTo(ctx context.Context, owner, name string, b base, entries []*gh.TreeEntry, message, branch string) error {
	tree, _, err := p.client.Git.CreateTree(ctx, owner, name, b.tree, entries)
	if err != nil {
		return classify("make the tree", err)
	}

	commit, _, err := p.client.Git.CreateCommit(ctx, owner, name, gh.Commit{
		Message: new(message),
		Tree:    &gh.Tree{SHA: tree.SHA},
		Parents: []*gh.Commit{{SHA: new(b.commit)}},
	}, nil)
	if err != nil {
		return classify("make the commit", err)
	}

	_, _, err = p.client.Git.CreateRef(ctx, owner, name, gh.CreateRef{Ref: "refs/" + branchRef + branch, SHA: commit.GetSHA()})
	if err != nil && !alreadyExists(err) {
		return classify("make the branch "+branch, err)
	}

	return nil
}

// open makes the pull request of a proposal from the branch.
func (p *PR) open(ctx context.Context, params Params, owner, name, branch string, inv action.Invocation) (*gh.PullRequest, error) {
	return p.openPullRequest(ctx, owner, name, gh.CreatePullRequest{
		Title: new(oneLine(plainText(params.Title))),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(p.description(params.Body, inv)),
		Draft: new(p.limits.draft()),
	})
}

// openPullRequest makes a pull request. A pull request that exists
// already, made by another attempt a moment ago, is returned instead.
func (p *PR) openPullRequest(ctx context.Context, owner, name string, req gh.CreatePullRequest) (*gh.PullRequest, error) {
	pr, _, err := p.client.PullRequests.Create(ctx, owner, name, req)
	if alreadyExists(err) {
		found, ferr := p.pullRequestFrom(ctx, owner, name, req.Head)
		if ferr != nil {
			return nil, fmt.Errorf("find the pull request made meanwhile: %w", ferr)
		}

		if found != nil {
			return found, nil
		}
	}

	if err != nil {
		return nil, classify("open the pull request", err)
	}

	if pr.GetNumber() == 0 {
		return nil, errNoNumber
	}

	return pr, nil
}

// description returns the body of the pull request: the model's text, made
// safe, and a line that says where it came from.
func (p *PR) description(body string, inv action.Invocation) string {
	var b strings.Builder

	b.WriteString(source.CutText(markdownText(body), maxPullRequestBodyBytes))
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`.", inv.ProposalID)

	if link := weblink.Investigation(p.publicURL, inv.InvestigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	return b.String()
}

// result describes the pull request for people and for the proposal.
func (p *PR) result(owner, name string, pr *gh.PullRequest, how string) action.Result {
	title := fmt.Sprintf("%s/%s#%d", owner, name, pr.GetNumber())

	draft := ""
	if pr.GetDraft() {
		draft = "draft "
	}

	var summary string

	switch state := stateOf(pr); {
	case how == resultOpened:
		summary = "Opened " + draft + "pull request " + title
	case state == StateMerged:
		summary = "Found pull request " + title + ", which has been merged since it was opened"
	case state == StateClosed:
		summary = "Found pull request " + title + ", which has been closed since it was opened"
	default:
		summary = "Found the already opened " + draft + "pull request " + title
	}

	return action.Result{Summary: summary, Links: []action.Link{{Title: title, URL: pr.GetHTMLURL()}}}
}

// commitMessage returns the message of the commit: the title on one line,
// and the proposal it is for.
func commitMessage(title, proposalID string) string {
	line := source.CutText(oneLine(plainText(title)), maxCommitTitleBytes)

	return line + "\n\nSidekick proposal " + proposalID + "\n"
}
