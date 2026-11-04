package githubgit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/githubapp"
)

// The fake GitHub's routes for the pull request used by the CI tests.
const (
	ciHeadSHA   = "4f2c1a9d0e8b7c6a5f4e3d2c1b0a99887766554433"
	ciParams    = `{"repo":"acme/api"}`
	ciPullPath  = "/repos/acme/api/pulls/12"
	ciRunsPath  = "/repos/acme/api/commits/" + ciHeadSHA + "/check-runs"
	ciStatePath = "/repos/acme/api/commits/" + ciHeadSHA + "/status"
	ciPullURL   = "https://github.com/acme/api/pull/12"
)

// ciPR is the pull request the tests read the CI of.
var ciPR = action.PullRequestRef{Number: 12, HeadSHA: ciHeadSHA}

// run is a check run as GitHub describes it.
func run(name, status, conclusion string) string {
	if conclusion == "" {
		return fmt.Sprintf(`{"name":%q,"status":%q,"conclusion":null,"html_url":"https://github.com/acme/api/runs/%s"}`, name, status, slug(name))
	}

	return fmt.Sprintf(`{"name":%q,"status":%q,"conclusion":%q,"html_url":"https://github.com/acme/api/runs/%s"}`, name, status, conclusion, slug(name))
}

// slug turns a name into the last part of an address, so a test can write the
// address a name has.
func slug(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}

		return '-'
	}, name)
}

// runs is the answer of the check runs call.
func runs(items ...string) string {
	return fmt.Sprintf(`{"total_count":%d,"check_runs":[%s]}`, len(items), strings.Join(items, ","))
}

// statuses is the answer of the combined status call. The combined state is
// pending whatever the contexts say when there are none.
func statuses(items ...string) string {
	return fmt.Sprintf(`{"state":"pending","total_count":%d,"statuses":[%s]}`, len(items), strings.Join(items, ","))
}

// status is one commit status as GitHub describes it.
func status(context, state string) string {
	return fmt.Sprintf(`{"context":%q,"state":%q,"target_url":"https://ci.example.com/%s"}`, context, state, slug(context))
}

// newCIReading returns a CIReading whose three clients talk to one fake
// GitHub.
func newCIReading(t *testing.T) (*CIReading, *fakehttp.Server) {
	t.Helper()

	client, fake := newFakeClient(t)

	return &CIReading{pulls: client, checks: client, statuses: client, timeout: 2 * time.Hour}, fake
}

func TestReadCI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		runs     string
		statuses string
		want     gitchange.CI
	}{
		{
			name:     "every check passed, a skipped and a neutral one included",
			runs:     runs(run("build", "completed", "success"), run("lint", "completed", "neutral"), run("docs", "completed", "skipped")),
			statuses: statuses(status("deploy/preview", "success")),
			want:     gitchange.CI{State: gitchange.CIPassed, Passed: 4, Total: 4},
		},
		{
			name:     "a failed check run and a failed status are named",
			runs:     runs(run("build", "completed", "success"), run("unit tests", "completed", "failure"), run("e2e", "completed", "timed_out"), run("deploy", "completed", "cancelled")),
			statuses: statuses(status("legacy/ci", "error"), status("coverage", "failure")),
			want: gitchange.CI{
				State: gitchange.CIFailed, Passed: 1, Total: 6, FailedCount: 5,
				Failed: []gitchange.CICheck{
					{Name: "unit tests", URL: "https://github.com/acme/api/runs/unit-tests"},
					{Name: "e2e", URL: "https://github.com/acme/api/runs/e2e"},
					{Name: "deploy", URL: "https://github.com/acme/api/runs/deploy"},
					{Name: "legacy/ci", URL: "https://ci.example.com/legacy-ci"},
					{Name: "coverage", URL: "https://ci.example.com/coverage"},
				},
			},
		},
		{
			name:     "a check that needs action counts as failed",
			runs:     runs(run("approval", "completed", "action_required")),
			statuses: statuses(),
			want: gitchange.CI{
				State: gitchange.CIFailed, Total: 1, FailedCount: 1,
				Failed: []gitchange.CICheck{{Name: "approval", URL: "https://github.com/acme/api/runs/approval"}},
			},
		},
		{
			name:     "nothing at all is none, whatever the combined state says",
			runs:     runs(),
			statuses: statuses(),
			want:     gitchange.CI{State: gitchange.CINone},
		},
		{
			name:     "a check still running is pending",
			runs:     runs(run("build", "completed", "success"), run("e2e", "in_progress", "")),
			statuses: statuses(),
			want:     gitchange.CI{State: gitchange.CIPending, Passed: 1, Total: 2},
		},
		{
			name:     "a queued check run is pending",
			runs:     runs(run("build", "queued", "")),
			statuses: statuses(),
			want:     gitchange.CI{State: gitchange.CIPending, Total: 1},
		},
		{
			name:     "a pending status is pending",
			runs:     runs(run("build", "completed", "success")),
			statuses: statuses(status("legacy/ci", "pending")),
			want:     gitchange.CI{State: gitchange.CIPending, Passed: 1, Total: 2},
		},
		{
			name:     "a failure while another check runs is pending, with the failure counted",
			runs:     runs(run("unit tests", "completed", "failure"), run("e2e", "in_progress", "")),
			statuses: statuses(),
			want: gitchange.CI{
				State: gitchange.CIPending, Total: 2, FailedCount: 1,
				Failed: []gitchange.CICheck{{Name: "unit tests", URL: "https://github.com/acme/api/runs/unit-tests"}},
			},
		},
		{
			name:     "a name with markup and hidden characters is plain text",
			runs:     runs(run("<img src=x onerror=1>\u202e\u200b  b", "completed", "failure")),
			statuses: statuses(),
			want: gitchange.CI{
				State: gitchange.CIFailed, Total: 1, FailedCount: 1,
				Failed: []gitchange.CICheck{{Name: "<img src=x onerror=1> b", URL: "https://github.com/acme/api/runs/" + slug("<img src=x onerror=1>\u202e\u200b  b")}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, fake := newCIReading(t)
			fake.Handle(http.MethodGet, ciPullPath, responseOf(pullJSON(12, "open", true)))
			fake.Handle(http.MethodGet, ciRunsPath, responseOf(tt.runs))
			fake.Handle(http.MethodGet, ciStatePath, responseOf(tt.statuses))

			got, err := c.ReadCI(t.Context(), json.RawMessage(ciParams), ciPR)
			require.NoError(t, err)

			tt.want.Repo, tt.want.URL = "acme/api", ciPullURL
			require.Equal(t, tt.want, got)

			require.Equal(t, []string{"GET " + ciPullPath, "GET " + ciRunsPath, "GET " + ciStatePath}, calls(fake))
			require.Equal(t, "latest", fake.Requests()[1].Query.Get("filter"))
			require.Equal(t, "100", fake.Requests()[1].Query.Get("per_page"))
			require.Equal(t, "100", fake.Requests()[2].Query.Get("per_page"))
		})
	}
}

func TestReadCINamesAtMostTenFailedChecks(t *testing.T) {
	t.Parallel()

	var items []string

	for i := range 13 {
		items = append(items, run(fmt.Sprintf("job-%02d", i), "completed", "failure"))
	}

	c, fake := newCIReading(t)
	fake.Handle(http.MethodGet, ciPullPath, responseOf(pullJSON(12, "open", true)))
	fake.Handle(http.MethodGet, ciRunsPath, responseOf(runs(items...)))
	fake.Handle(http.MethodGet, ciStatePath, responseOf(statuses()))

	got, err := c.ReadCI(t.Context(), json.RawMessage(ciParams), ciPR)
	require.NoError(t, err)
	require.Equal(t, gitchange.CIFailed, got.State)
	require.Equal(t, 13, got.FailedCount)
	require.Len(t, got.Failed, gitchange.MaxFailedChecks)
	require.Equal(t, "job-09", got.Failed[9].Name)
}

func TestReadCIFollowsThePages(t *testing.T) {
	t.Parallel()

	c, fake := newCIReading(t)
	next := fakehttp.Response{
		Headers: map[string]string{
			"Content-Type": "application/json; charset=utf-8",
			"Link":         `<` + fake.URL() + ciRunsPath + `?page=2&per_page=100&filter=latest>; rel="next"`,
		},
		Body: []byte(`{"total_count":2,"check_runs":[` + run("build", "completed", "success") + `]}`),
	}

	fake.Handle(http.MethodGet, ciPullPath, responseOf(pullJSON(12, "open", true)))
	fake.Handle(http.MethodGet, ciRunsPath, next, responseOf(`{"total_count":2,"check_runs":[`+run("lint", "completed", "failure")+`]}`))
	fake.Handle(http.MethodGet, ciStatePath, responseOf(statuses()))

	got, err := c.ReadCI(t.Context(), json.RawMessage(ciParams), ciPR)
	require.NoError(t, err)
	require.Equal(t, gitchange.CIFailed, got.State)
	require.Equal(t, 2, got.Total)
	require.Equal(t, "2", fake.Requests()[2].Query.Get("page"))
}

func TestReadCIStopsAfterTheMostPages(t *testing.T) {
	t.Parallel()

	c, fake := newCIReading(t)
	page := fakehttp.Response{
		Headers: map[string]string{
			"Content-Type": "application/json; charset=utf-8",
			"Link":         `<` + fake.URL() + ciRunsPath + `?page=2>; rel="next"`,
		},
		Body: []byte(runs(run("build", "completed", "success"))),
	}

	fake.Handle(http.MethodGet, ciPullPath, responseOf(pullJSON(12, "open", true)))
	fake.HandleFunc(http.MethodGet, ciRunsPath, func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range page.Headers {
			w.Header().Set(k, v)
		}

		_, _ = w.Write(page.Body)
	})
	fake.Handle(http.MethodGet, ciStatePath, responseOf(statuses()))

	_, err := c.ReadCI(t.Context(), json.RawMessage(ciParams), ciPR)
	require.NoError(t, err)

	var runCalls int

	for _, r := range fake.Requests() {
		if r.Path == ciRunsPath {
			runCalls++
		}
	}

	require.Equal(t, ciMaxPages, runCalls)
}

func TestReadCIWithoutAPermission(t *testing.T) {
	t.Parallel()

	denied := fakehttpResponse(http.StatusForbidden, `{"message":"Resource not accessible by integration"}`)

	tests := []struct {
		name     string
		runs     fakehttp.Response
		statuses fakehttp.Response
		want     string
	}{
		{
			name:     "no Checks",
			runs:     denied,
			statuses: responseOf(statuses()),
			want:     "Sidekick could not read the checks. The GitHub App needs Checks (read).",
		},
		{
			name:     "no Commit statuses",
			runs:     responseOf(runs()),
			statuses: denied,
			want:     "Sidekick could not read the checks. The GitHub App needs Commit statuses (read).",
		},
		{
			name:     "neither",
			runs:     fakehttpResponse(http.StatusUnprocessableEntity, `{"message":"The permissions requested are not granted to this installation."}`),
			statuses: denied,
			want:     "Sidekick could not read the checks. The GitHub App needs Checks (read) and Commit statuses (read).",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, fake := newCIReading(t)
			fake.Handle(http.MethodGet, ciPullPath, responseOf(pullJSON(12, "open", true)))
			fake.Handle(http.MethodGet, ciRunsPath, tt.runs)
			fake.Handle(http.MethodGet, ciStatePath, tt.statuses)

			got, err := c.ReadCI(t.Context(), json.RawMessage(ciParams), ciPR)
			require.NoError(t, err)
			require.Equal(t, gitchange.CI{State: gitchange.CIUnknown, Repo: "acme/api", URL: ciPullURL, Message: tt.want}, got)
		})
	}
}

// tokenRefused is a transport that fails as the GitHub App client does when
// GitHub refuses to make an installation token with the permissions it asks for.
type tokenRefused struct{}

func (tokenRefused) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("make an installation token: %w: %w", githubapp.ErrToken, &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
		Message:  "The permissions requested are not granted to this installation.",
	})
}

func TestReadCIWhenTheTokenCannotBeMade(t *testing.T) {
	t.Parallel()

	c, fake := newCIReading(t)
	base := fake.URL() + "/"

	refused, err := gh.NewClient(gh.WithHTTPClient(&http.Client{Transport: tokenRefused{}}), gh.WithURLs(&base, nil))
	require.NoError(t, err)

	c.checks = refused

	fake.Handle(http.MethodGet, ciPullPath, responseOf(pullJSON(12, "open", true)))
	fake.Handle(http.MethodGet, ciStatePath, responseOf(statuses(status("legacy/ci", "success"))))

	got, err := c.ReadCI(t.Context(), json.RawMessage(ciParams), ciPR)
	require.NoError(t, err, "a missing permission is a state, not an error: the write actions are not touched")
	require.Equal(t, gitchange.CIUnknown, got.State)
	require.Equal(t, "Sidekick could not read the checks. The GitHub App needs Checks (read).", got.Message)
}

func TestReadCIErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pull      fakehttp.Response
		runs      fakehttp.Response
		params    string
		wantPerm  bool
		wantInErr string
	}{
		{
			name:     "a server error is tried again later",
			pull:     responseOf(pullJSON(12, "open", true)),
			runs:     fakehttpResponse(http.StatusBadGateway, `{"message":"Bad gateway"}`),
			wantPerm: false,
		},
		{
			name:     "a repository the app cannot see is permanent",
			pull:     fakehttpResponse(http.StatusNotFound, `{"message":"Not Found"}`),
			runs:     responseOf(runs()),
			wantPerm: true,
		},
		{
			name:     "parameters without a repository are permanent",
			params:   `{}`,
			pull:     responseOf(pullJSON(12, "open", true)),
			runs:     responseOf(runs()),
			wantPerm: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, fake := newCIReading(t)
			fake.HandleFunc(http.MethodGet, ciPullPath, answer(tt.pull))
			fake.HandleFunc(http.MethodGet, ciRunsPath, answer(tt.runs))
			fake.HandleFunc(http.MethodGet, ciStatePath, answer(responseOf(statuses())))

			params := tt.params
			if params == "" {
				params = ciParams
			}

			_, err := c.ReadCI(t.Context(), json.RawMessage(params), ciPR)
			require.Error(t, err)
			require.Equal(t, tt.wantPerm, isPermanent(err))
		})
	}
}

// answer is a handler that sends r.
func answer(r fakehttp.Response) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range r.Headers {
			w.Header().Set(k, v)
		}

		status := r.Status
		if status == 0 {
			status = http.StatusOK
		}

		w.WriteHeader(status)
		_, _ = w.Write(r.Body)
	}
}

// isPermanent reports whether err wraps action.ErrPermanent.
func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent)
}

func TestCIReadingIsAReader(t *testing.T) {
	t.Parallel()

	c := &CIReading{timeout: 3 * time.Hour}

	require.Equal(t, 3*time.Hour, c.CIReportTimeout())
	require.Equal(t, "github", c.CIHost())
}
