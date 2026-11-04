package gitlabgit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// What a read of CI asks GitLab for, at most: items per page, and pages of
// the pipelines of a merge request and of the jobs of a pipeline.
const (
	ciPageSize = 100
	ciMaxPages = 5
)

// unreadableMessage is what the report says when GitLab refuses the token.
const unreadableMessage = "Sidekick could not read the pipelines. The token needs the api scope."

// The statuses GitLab gives a pipeline.
const (
	pipelineSuccess  = "success"
	pipelineFailed   = "failed"
	pipelineCanceled = "canceled"
	pipelineSkipped  = "skipped"
)

// The statuses GitLab gives a job. A job that has not run and waits for a
// person (manual) or was left out (skipped) is not counted.
const (
	jobSuccess  = "success"
	jobFailed   = "failed"
	jobCanceled = "canceled"
	jobSkipped  = "skipped"
	jobManual   = "manual"
)

// CIReading reads the CI of merge requests from their pipelines. Embed it in
// an action to make the action a gitchange.CIReader.
type CIReading struct {
	client  *gitlabapi.Client
	timeout time.Duration
}

var _ gitchange.CIReader = (*CIReading)(nil)

// NewCIReading returns a CIReading that reads with client, the action's own,
// and reports at timeout.
func NewCIReading(client *gitlabapi.Client, timeout time.Duration) *CIReading {
	return &CIReading{client: client, timeout: timeout}
}

// ReadCI implements gitchange.CIReader. It reads the pipelines of the merge
// request and takes the newest one for the head commit. No pipeline is none
// (GitLab does not tell a project with no CI from a pipeline that has not
// started yet), a pipeline that is not finished is pending, and a finished
// one is passed or failed, with its failed jobs named. When GitLab refuses the
// token, the state is unknown.
func (c *CIReading) ReadCI(ctx context.Context, params json.RawMessage, pr action.PullRequestRef) (gitchange.CI, error) {
	project, err := ProjectOfParams(params)
	if err != nil {
		return gitchange.CI{}, fmt.Errorf("read the proposal: %w", err)
	}

	ci, err := c.read(ctx, project, pr)
	if refused(err) {
		return gitchange.CI{State: gitchange.CIUnknown, Repo: project, Message: unreadableMessage}, nil
	}

	if err != nil {
		return gitchange.CI{}, fmt.Errorf("read the CI of %s: %w", project, err)
	}

	return ci, nil
}

// CIReportTimeout implements gitchange.CIReader.
func (c *CIReading) CIReportTimeout() time.Duration {
	return c.timeout
}

// CIHost implements gitchange.CIReader.
func (c *CIReading) CIHost() string {
	return "gitlab"
}

// apiPipeline is a pipeline as the merge request's pipelines list answers.
type apiPipeline struct {
	ID     int64  `json:"id"`
	SHA    string `json:"sha"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

// apiJob is a job as the jobs of a pipeline list answers.
type apiJob struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	WebURL       string `json:"web_url"`
	AllowFailure bool   `json:"allow_failure"`
}

// read reads the merge request, its newest pipeline for the head commit, and
// the jobs of that pipeline.
func (c *CIReading) read(ctx context.Context, project string, pr action.PullRequestRef) (gitchange.CI, error) {
	iid := strconv.FormatInt(pr.Number, 10)

	var mr MergeRequest
	if _, err := c.client.Get(ctx, gitlabapi.ProjectPath(project, "merge_requests", iid), nil, &mr); err != nil {
		return gitchange.CI{}, Classify("read merge request "+iid, err)
	}

	ci := gitchange.CI{Repo: project, URL: mr.WebURL}

	pipeline, err := c.newestPipeline(ctx, project, iid, pr.HeadSHA)
	if err != nil {
		return gitchange.CI{}, fmt.Errorf("find the pipeline: %w", err)
	}

	if pipeline == nil || pipeline.Status == pipelineSkipped {
		ci.State = gitchange.CINone

		return ci, nil
	}

	jobs, err := c.jobs(ctx, project, pipeline.ID)
	if err != nil {
		return gitchange.CI{}, fmt.Errorf("list the jobs: %w", err)
	}

	count(&ci, jobs)

	switch pipeline.Status {
	case pipelineSuccess:
		ci.State = gitchange.CIPassed
	case pipelineFailed, pipelineCanceled:
		ci.State = gitchange.CIFailed

		if ci.FailedCount == 0 {
			ci.FailedCount = 1
			ci.Failed = []gitchange.CICheck{{Name: "pipeline " + strconv.FormatInt(pipeline.ID, 10), URL: pipeline.WebURL}}
		}
	default:
		ci.State = gitchange.CIPending
	}

	return ci, nil
}

// newestPipeline returns the pipeline with the highest ID among the merge
// request's pipelines for the commit sha, or nil when there is none. GitLab
// does not say in which order the list comes, and it holds pipelines of the
// branch for earlier commits too.
func (c *CIReading) newestPipeline(ctx context.Context, project, iid, sha string) (*apiPipeline, error) {
	var newest *apiPipeline

	page := 1

	for range ciMaxPages {
		items, next, err := gitlabapi.ListPage[apiPipeline](ctx, c.client, gitlabapi.ProjectPath(project, "merge_requests", iid, "pipelines"), nil, page, ciPageSize)
		if err != nil {
			return nil, Classify("list the pipelines of merge request "+iid, err)
		}

		for _, p := range items {
			if p.SHA == sha && (newest == nil || p.ID > newest.ID) {
				newest = &p
			}
		}

		if next == 0 {
			break
		}

		page = next
	}

	return newest, nil
}

// jobs returns the jobs of a pipeline, up to ciMaxPages pages.
func (c *CIReading) jobs(ctx context.Context, project string, id int64) ([]apiJob, error) {
	var all []apiJob

	page := 1

	for range ciMaxPages {
		items, next, err := gitlabapi.ListPage[apiJob](ctx, c.client, gitlabapi.ProjectPath(project, "pipelines", strconv.FormatInt(id, 10), "jobs"), nil, page, ciPageSize)
		if err != nil {
			return nil, Classify("list the jobs of pipeline "+strconv.FormatInt(id, 10), err)
		}

		all = append(all, items...)

		if next == 0 {
			break
		}

		page = next
	}

	return all, nil
}

// count adds the jobs to ci. A failed job that is allowed to fail does not
// count, and a skipped job or one that waits for a person is left out.
func count(ci *gitchange.CI, jobs []apiJob) {
	for _, j := range jobs {
		switch {
		case j.Status == jobSkipped || j.Status == jobManual:
			continue
		case j.Status == jobFailed && j.AllowFailure:
			continue
		}

		ci.Total++

		switch j.Status {
		case jobSuccess:
			ci.Passed++
		case jobFailed, jobCanceled:
			ci.FailedCount++

			if len(ci.Failed) < gitchange.MaxFailedChecks {
				ci.Failed = append(ci.Failed, gitchange.CICheck{Name: gitchange.CleanName(j.Name), URL: j.WebURL})
			}
		}
	}
}

// refused reports whether err says GitLab did not let the token read.
func refused(err error) bool {
	return gitlabapi.IsStatus(err, http.StatusUnauthorized) || gitlabapi.IsStatus(err, http.StatusForbidden)
}
