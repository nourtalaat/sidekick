package gitlabgit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// The merge request the CI tests read, and its routes.
var (
	ciParams       = json.RawMessage(`{"project":"` + testProject + `"}`)
	ciPR           = action.PullRequestRef{Number: 12, HeadSHA: testHeadSHA}
	ciMRPath       = apiPath("merge_requests/12")
	ciPipelinePath = apiPath("merge_requests/12/pipelines")
	ciJobsPath     = apiPath("pipelines/77/jobs")
)

const ciOtherSHA = "eeee000000000000000000000000000000000005"

// pipeline is a pipeline as the merge request's pipelines list answers.
func pipeline(id int, sha, status string) string {
	return fmt.Sprintf(`{"id":%d,"sha":%q,"ref":"sidekick/x","status":%q,"web_url":"https://gitlab.example.com/acme/platform/api/-/pipelines/%d"}`, id, sha, status, id)
}

// job is a job as the jobs of a pipeline list answers.
func job(name, status string, allowFailure bool) string {
	return fmt.Sprintf(`{"name":%q,"status":%q,"allow_failure":%t,"web_url":"https://gitlab.example.com/acme/platform/api/-/jobs/%s"}`, name, status, allowFailure, strings.ReplaceAll(name, " ", "-"))
}

// list is a JSON list.
func list(items ...string) fakehttp.Response {
	return reply("[" + strings.Join(items, ",") + "]")
}

func TestReadCI(t *testing.T) {
	t.Parallel()

	mrURL := testWebURL + "12"

	tests := []struct {
		name      string
		pipelines fakehttp.Response
		jobs      fakehttp.Response
		want      gitchange.CI
	}{
		{
			name:      "no pipeline is none",
			pipelines: list(),
			want:      gitchange.CI{State: gitchange.CINone},
		},
		{
			name:      "a pipeline of an earlier commit is none",
			pipelines: list(pipeline(70, ciOtherSHA, "success")),
			want:      gitchange.CI{State: gitchange.CINone},
		},
		{
			name:      "a skipped pipeline is none",
			pipelines: list(pipeline(77, testHeadSHA, "skipped")),
			want:      gitchange.CI{State: gitchange.CINone},
		},
		{
			name:      "a successful pipeline counts its jobs, but not those that were skipped, wait for a person or may fail",
			pipelines: list(pipeline(77, testHeadSHA, "success")),
			jobs:      list(job("build", "success", false), job("test", "success", false), job("lint", "failed", true), job("deploy", "manual", false), job("docs", "skipped", false)),
			want:      gitchange.CI{State: gitchange.CIPassed, Passed: 2, Total: 2},
		},
		{
			name:      "the newest pipeline of the head commit counts",
			pipelines: list(pipeline(70, testHeadSHA, "failed"), pipeline(77, testHeadSHA, "success"), pipeline(80, ciOtherSHA, "failed")),
			jobs:      list(job("build", "success", false)),
			want:      gitchange.CI{State: gitchange.CIPassed, Passed: 1, Total: 1},
		},
		{
			name:      "a failed pipeline names its failed jobs",
			pipelines: list(pipeline(77, testHeadSHA, "failed")),
			jobs:      list(job("build", "success", false), job("unit tests", "failed", false), job("e2e", "canceled", false)),
			want: gitchange.CI{
				State: gitchange.CIFailed, Passed: 1, Total: 3, FailedCount: 2,
				Failed: []gitchange.CICheck{
					{Name: "unit tests", URL: "https://gitlab.example.com/acme/platform/api/-/jobs/unit-tests"},
					{Name: "e2e", URL: "https://gitlab.example.com/acme/platform/api/-/jobs/e2e"},
				},
			},
		},
		{
			name:      "a failed pipeline with no failed job is named itself",
			pipelines: list(pipeline(77, testHeadSHA, "failed")),
			jobs:      list(),
			want: gitchange.CI{
				State: gitchange.CIFailed, FailedCount: 1,
				Failed: []gitchange.CICheck{{Name: "pipeline 77", URL: "https://gitlab.example.com/acme/platform/api/-/pipelines/77"}},
			},
		},
		{
			name:      "a running pipeline is pending, with the failure it has seen",
			pipelines: list(pipeline(77, testHeadSHA, "running")),
			jobs:      list(job("build", "success", false), job("unit tests", "failed", false), job("e2e", "running", false), job("deploy", "created", false)),
			want: gitchange.CI{
				State: gitchange.CIPending, Passed: 1, Total: 4, FailedCount: 1,
				Failed: []gitchange.CICheck{{Name: "unit tests", URL: "https://gitlab.example.com/acme/platform/api/-/jobs/unit-tests"}},
			},
		},
		{
			name:      "a pipeline that waits for a manual job is pending",
			pipelines: list(pipeline(77, testHeadSHA, "manual")),
			jobs:      list(job("build", "success", false), job("release", "manual", false)),
			want:      gitchange.CI{State: gitchange.CIPending, Passed: 1, Total: 1},
		},
		{
			name:      "a job name with markup and hidden characters is plain text",
			pipelines: list(pipeline(77, testHeadSHA, "failed")),
			jobs:      list(job("<b>x</b>\u202e\u200b  y", "failed", false)),
			want: gitchange.CI{
				State: gitchange.CIFailed, Total: 1, FailedCount: 1,
				Failed: []gitchange.CICheck{{Name: "<b>x</b> y", URL: "https://gitlab.example.com/acme/platform/api/-/jobs/<b>x</b>\u202e\u200b--y"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t)
			b.gitlab.Handle(http.MethodGet, ciMRPath, reply(mergeRequestBody(12, "opened", true)))
			b.gitlab.Handle(http.MethodGet, ciPipelinePath, tt.pipelines)

			if tt.jobs.Body != nil {
				b.gitlab.Handle(http.MethodGet, ciJobsPath, tt.jobs)
			}

			c := NewCIReading(b.client, time.Hour)

			got, err := c.ReadCI(t.Context(), ciParams, ciPR)
			require.NoError(t, err)

			tt.want.Repo, tt.want.URL = testProject, mrURL
			require.Equal(t, tt.want, got)
		})
	}
}

func TestReadCIFollowsThePagesOfPipelines(t *testing.T) {
	t.Parallel()

	b := newBed(t)
	b.gitlab.Handle(http.MethodGet, ciMRPath, reply(mergeRequestBody(12, "opened", true)))
	b.gitlab.Handle(http.MethodGet, ciPipelinePath,
		fakehttp.Response{Headers: map[string]string{"Content-Type": "application/json", "X-Next-Page": "2"}, Body: []byte("[" + pipeline(70, ciOtherSHA, "success") + "]")},
		list(pipeline(77, testHeadSHA, "success")),
	)
	b.gitlab.Handle(http.MethodGet, ciJobsPath, list(job("build", "success", false)))

	got, err := NewCIReading(b.client, time.Hour).ReadCI(t.Context(), ciParams, ciPR)
	require.NoError(t, err)
	require.Equal(t, gitchange.CIPassed, got.State)

	var pages []string

	for _, r := range b.gitlab.Requests() {
		if strings.HasSuffix(r.Path, "/pipelines") {
			pages = append(pages, r.Query.Get("page")+"/"+r.Query.Get("per_page"))
		}
	}

	require.Equal(t, []string{"1/100", "2/100"}, pages)
}

func TestReadCIWhenGitLabRefusesTheToken(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			b := newBed(t)
			b.gitlab.Handle(http.MethodGet, ciMRPath, failed(status, "refused"))

			got, err := NewCIReading(b.client, time.Hour).ReadCI(t.Context(), ciParams, ciPR)
			require.NoError(t, err, "a refused token is a state, not an error")
			require.Equal(t, gitchange.CI{State: gitchange.CIUnknown, Repo: testProject, Message: "Sidekick could not read the pipelines. The token needs the api scope."}, got)
		})
	}
}

func TestReadCIErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mr            fakehttp.Response
		params        json.RawMessage
		wantPermanent bool
	}{
		{name: "a server error is tried again later", mr: failed(http.StatusBadGateway, "bad gateway")},
		{name: "a merge request that is gone is permanent", mr: notFound, wantPermanent: true},
		{name: "parameters without a project are permanent", params: json.RawMessage(`{}`), mr: reply(mergeRequestBody(12, "opened", true)), wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t)
			b.gitlab.Handle(http.MethodGet, ciMRPath, tt.mr)

			params := tt.params
			if params == nil {
				params = ciParams
			}

			_, err := NewCIReading(b.client, time.Hour).ReadCI(t.Context(), params, ciPR)
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
		})
	}
}

func TestCIReadingIsAReader(t *testing.T) {
	t.Parallel()

	c := NewCIReading(nil, 3*time.Hour)

	require.Equal(t, 3*time.Hour, c.CIReportTimeout())
	require.Equal(t, "gitlab", c.CIHost())
}
