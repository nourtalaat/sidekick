package githubgit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/githubapp"
)

// What a read of CI asks GitHub for, at most: checks and statuses per page,
// and pages of each. A commit with more than ciMaxPages * ciPageSize of
// either is read only up to that.
const (
	ciPageSize = 100
	ciMaxPages = 5
)

// The permissions a read of CI needs, as the app's settings page names them.
const (
	permissionChecks   = "Checks (read)"
	permissionStatuses = "Commit statuses (read)"
)

// CIReading reads the CI of pull requests with three clients: one that reads
// the pull request, with the action's own permissions, and one each for check
// runs and commit statuses, whose installation tokens ask for only that
// permission. They are apart so that an app that was not given Checks or
// Commit statuses fails only the reads of CI. Embed it in an action to make
// the action a gitchange.CIReader.
type CIReading struct {
	pulls    *gh.Client
	checks   *gh.Client
	statuses *gh.Client
	timeout  time.Duration
}

var _ gitchange.CIReader = (*CIReading)(nil)

// NewCIReading returns a CIReading. pulls is the action's own client; base
// describes the app and the API, and its Permissions are replaced for the
// clients of checks and statuses. timeout is CIReportTimeout. It makes no
// network call.
func NewCIReading(pulls *gh.Client, base githubapp.Options, timeout time.Duration) (*CIReading, error) {
	base.Permissions = githubapp.ChecksRead()

	checks, err := githubapp.NewClient(base)
	if err != nil {
		return nil, fmt.Errorf("make the GitHub client for checks: %w", err)
	}

	base.Permissions = githubapp.StatusesRead()

	statuses, err := githubapp.NewClient(base)
	if err != nil {
		return nil, fmt.Errorf("make the GitHub client for commit statuses: %w", err)
	}

	return &CIReading{pulls: pulls, checks: checks, statuses: statuses, timeout: timeout}, nil
}

// ReadCI implements gitchange.CIReader. It reads the check runs and the
// commit statuses of the head commit; both count. Nothing running and nothing
// failed is passed, nothing at all is none, and a failure while others run is
// still pending. When the app lacks Checks or Commit statuses, or GitHub
// refuses the read, the state is unknown and the message names the missing
// permission.
func (c *CIReading) ReadCI(ctx context.Context, params json.RawMessage, pr action.PullRequestRef) (gitchange.CI, error) {
	owner, name, repo, err := RepoOfParams(params)
	if err != nil {
		return gitchange.CI{}, fmt.Errorf("read the proposal: %w", err)
	}

	page, _, err := c.pulls.PullRequests.Get(ctx, owner, name, int(pr.Number))
	if err != nil {
		return gitchange.CI{}, Classify(fmt.Sprintf("read pull request %d of %s", pr.Number, repo), err)
	}

	var t tally

	missing, err := c.read(ctx, &t, owner, name, pr.HeadSHA)
	if err != nil {
		return gitchange.CI{}, fmt.Errorf("read the CI of %s: %w", repo, err)
	}

	ci := t.result()
	ci.Repo, ci.URL = repo, page.GetHTMLURL()

	if len(missing) > 0 {
		ci = gitchange.CI{State: gitchange.CIUnknown, Repo: repo, URL: ci.URL, Message: missingMessage(missing)}
	}

	return ci, nil
}

// CIReportTimeout implements gitchange.CIReader.
func (c *CIReading) CIReportTimeout() time.Duration {
	return c.timeout
}

// CIHost implements gitchange.CIReader.
func (c *CIReading) CIHost() string {
	return "github"
}

// read adds the check runs and the commit statuses of sha to t. It returns
// the permissions GitHub did not give, which are not an error, and the first
// error that is.
func (c *CIReading) read(ctx context.Context, t *tally, owner, name, sha string) (missing []string, err error) {
	if err := c.readChecks(ctx, t, owner, name, sha); err != nil {
		if !noPermission(err) {
			return nil, fmt.Errorf("check runs: %w", err)
		}

		missing = append(missing, permissionChecks)
	}

	if err := c.readStatuses(ctx, t, owner, name, sha); err != nil {
		if !noPermission(err) {
			return nil, fmt.Errorf("commit statuses: %w", err)
		}

		missing = append(missing, permissionStatuses)
	}

	return missing, nil
}

// readChecks adds the latest check run of each check of sha to t.
func (c *CIReading) readChecks(ctx context.Context, t *tally, owner, name, sha string) error {
	opts := &gh.ListCheckRunsOptions{Filter: new("latest"), ListOptions: gh.ListOptions{PerPage: ciPageSize}}

	for range ciMaxPages {
		runs, resp, err := c.checks.Checks.ListCheckRunsForRef(ctx, owner, name, sha, opts)
		if err != nil {
			return Classify("list the check runs of "+sha, err)
		}

		for _, run := range runs.CheckRuns {
			t.addRun(run)
		}

		if resp.NextPage == 0 {
			return nil
		}

		opts.Page = resp.NextPage
	}

	return nil
}

// readStatuses adds the latest commit status of each context of sha to t.
func (c *CIReading) readStatuses(ctx context.Context, t *tally, owner, name, sha string) error {
	opts := &gh.ListOptions{PerPage: ciPageSize}

	for range ciMaxPages {
		combined, resp, err := c.statuses.Repositories.GetCombinedStatus(ctx, owner, name, sha, opts)
		if err != nil {
			return Classify("read the combined status of "+sha, err)
		}

		for _, status := range combined.Statuses {
			t.addStatus(status)
		}

		if resp.NextPage == 0 {
			return nil
		}

		opts.Page = resp.NextPage
	}

	return nil
}

// tally counts the checks of a commit.
type tally struct {
	total   int
	passed  int
	running int
	failed  []gitchange.CICheck
	// failedCount is how many failed; failed names only the first few.
	failedCount int
}

// addRun counts one check run.
func (t *tally) addRun(run *gh.CheckRun) {
	url := run.GetHTMLURL()
	if url == "" {
		url = run.GetDetailsURL()
	}

	switch {
	case run.GetStatus() != checkCompleted:
		t.add(outcomeRunning, run.GetName(), url)
	case passedConclusions[run.GetConclusion()]:
		t.add(outcomePassed, run.GetName(), url)
	default:
		t.add(outcomeFailed, run.GetName(), url)
	}
}

// addStatus counts one commit status.
func (t *tally) addStatus(status *gh.RepoStatus) {
	switch status.GetState() {
	case statusSuccess:
		t.add(outcomePassed, status.GetContext(), status.GetTargetURL())
	case statusPending:
		t.add(outcomeRunning, status.GetContext(), status.GetTargetURL())
	default:
		t.add(outcomeFailed, status.GetContext(), status.GetTargetURL())
	}
}

// add counts one check by its outcome.
func (t *tally) add(o outcome, name, url string) {
	t.total++

	switch o {
	case outcomePassed:
		t.passed++
	case outcomeRunning:
		t.running++
	case outcomeFailed:
		t.failedCount++

		if len(t.failed) < gitchange.MaxFailedChecks {
			t.failed = append(t.failed, gitchange.CICheck{Name: gitchange.CleanName(name), URL: url})
		}
	}
}

// result is the state the tally adds up to.
func (t *tally) result() gitchange.CI {
	ci := gitchange.CI{Passed: t.passed, Total: t.total, FailedCount: t.failedCount, Failed: t.failed}

	switch {
	case t.total == 0:
		ci.State = gitchange.CINone
	case t.running > 0:
		ci.State = gitchange.CIPending
	case t.failedCount > 0:
		ci.State = gitchange.CIFailed
	default:
		ci.State = gitchange.CIPassed
	}

	return ci
}

// outcome is what a check or status says.
type outcome int

const (
	outcomePassed outcome = iota
	outcomeRunning
	outcomeFailed
)

// The values GitHub gives a check run's status, and a commit status's state.
const (
	checkCompleted = "completed"
	statusSuccess  = "success"
	statusPending  = "pending"
)

// passedConclusions are the conclusions of a completed check run that do not
// need anyone to look: it passed, it had nothing to say, or it was skipped.
var passedConclusions = map[string]bool{"success": true, "neutral": true, "skipped": true}

// noPermission reports whether err says GitHub did not let the client read:
// the installation token could not be made with the permission the client
// asks for (422, or 403 for an app that is not installed with it), or GitHub
// refused the call (403, "Resource not accessible by integration"). A 404 is
// not counted: it means a repository the app cannot see, a permanent error of
// its own.
func noPermission(err error) bool {
	resp, ok := errors.AsType[*gh.ErrorResponse](err)
	if !ok || resp.Response == nil {
		return false
	}

	return resp.Response.StatusCode == http.StatusForbidden || resp.Response.StatusCode == http.StatusUnprocessableEntity
}

// missingMessage is the sentence that names the permissions the app lacks.
func missingMessage(missing []string) string {
	return "Sidekick could not read the checks. The GitHub App needs " + strings.Join(missing, " and ") + "."
}
