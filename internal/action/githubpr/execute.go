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
		return action.Result{}, err
	}

	if pr != nil {
		p.metrics.pullRequest(resultExisting)

		return p.result(owner, name, pr, resultExisting), nil
	}

	if !branchExists {
		pre, err := p.prepare(ctx, inv.Params)
		if err != nil {
			p.metrics.pullRequest(resultError)

			return action.Result{}, fmt.Errorf("check the pull request before it is made: %w", err)
		}

		if err := p.push(ctx, pre, branch, inv); err != nil {
			p.metrics.pullRequest(resultError)

			return action.Result{}, err
		}
	}

	pr, err = p.open(ctx, params, owner, name, branch, inv)
	if err != nil {
		p.metrics.pullRequest(resultError)

		return action.Result{}, err
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
		return nil, true, err
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
// and points the new branch at it. The commit exists before the branch, so
// a crash in between leaves only objects nothing refers to.
func (p *PR) push(ctx context.Context, pre *prepared, branch string, inv action.Invocation) error {
	entries := make([]*gh.TreeEntry, 0, len(pre.changes))

	for _, c := range pre.changes {
		entry := &gh.TreeEntry{Path: new(c.path)}

		if !c.deleted() {
			entry.Mode, entry.Type, entry.Content = new(fileMode), new(blobType), c.content
		}

		entries = append(entries, entry)
	}

	tree, _, err := p.client.Git.CreateTree(ctx, pre.owner, pre.name, pre.base.tree, entries)
	if err != nil {
		return classify("make the tree", err)
	}

	commit, _, err := p.client.Git.CreateCommit(ctx, pre.owner, pre.name, gh.Commit{
		Message: new(commitMessage(pre.params.Title, inv.ProposalID)),
		Tree:    &gh.Tree{SHA: tree.SHA},
		Parents: []*gh.Commit{{SHA: new(pre.base.commit)}},
	}, nil)
	if err != nil {
		return classify("make the commit", err)
	}

	_, _, err = p.client.Git.CreateRef(ctx, pre.owner, pre.name, gh.CreateRef{Ref: "refs/" + branchRef + branch, SHA: commit.GetSHA()})
	if err != nil && !alreadyExists(err) {
		return classify("make the branch "+branch, err)
	}

	return nil
}

// open makes the pull request from the branch. A pull request that exists
// already, made by another attempt a moment ago, is returned instead.
func (p *PR) open(ctx context.Context, params Params, owner, name, branch string, inv action.Invocation) (*gh.PullRequest, error) {
	pr, _, err := p.client.PullRequests.Create(ctx, owner, name, gh.CreatePullRequest{
		Title: new(oneLine(plainText(params.Title))),
		Head:  branch,
		Base:  params.BaseBranch,
		Body:  new(p.description(params.Body, inv)),
		Draft: new(p.limits.draft()),
	})
	if alreadyExists(err) {
		found, ferr := p.pullRequestFrom(ctx, owner, name, branch)
		if ferr != nil {
			return nil, ferr
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

	b.WriteString(source.CutText(plainText(body), maxPullRequestBodyBytes))
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

	summary := "Opened "
	if how == resultExisting {
		summary = "Found the already opened "
	}

	if pr.GetDraft() {
		summary += "draft "
	}

	return action.Result{
		Summary: summary + "pull request " + title,
		Links:   []action.Link{{Title: title, URL: pr.GetHTMLURL()}},
	}
}

// commitMessage returns the message of the commit: the title on one line,
// and the proposal it is for.
func commitMessage(title, proposalID string) string {
	line := source.CutText(oneLine(plainText(title)), maxCommitTitleBytes)

	return line + "\n\nSidekick proposal " + proposalID + "\n"
}
