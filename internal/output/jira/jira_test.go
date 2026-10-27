package jira

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/output/ticket"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// update rewrites the golden files: go test ./internal/output/jira -update.
var update = flag.Bool("update", false, "rewrite the golden files in fixture/golden/")

func TestValidate(t *testing.T) {
	t.Parallel()

	valid := func() Config {
		return Config{URL: "https://acme.atlassian.net", Email: testEmail, APITokenSecret: tokenRef, Projects: []string{"OPS"}}
	}

	tests := []struct {
		name     string
		edit     func(c *Config)
		wantErrs []string
	}{
		{name: "valid", edit: func(*Config) {}},
		{
			name:     "empty",
			edit:     func(c *Config) { *c = Config{} },
			wantErrs: []string{"url: required", "email: required", "apiTokenSecret: required", "projects: list at least one project key"},
		},
		{name: "an http site", edit: func(c *Config) { c.URL = "http://acme.atlassian.net" }, wantErrs: []string{"url: must use https"}},
		{name: "a name as the email", edit: func(c *Config) { c.Email = "Sidekick <sidekick@example.com>" }, wantErrs: []string{"email: expected an email address"}},
		{name: "a project that is not a key", edit: func(c *Config) { c.Projects = []string{"OPS-1"} }, wantErrs: []string{`projects[0]: "OPS-1" is not a project key`}},
		{name: "an empty label", edit: func(c *Config) { c.Labels = []string{""} }, wantErrs: []string{"labels[0]: must not be empty"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := valid()
			tt.edit(&c)

			err := c.Validate()
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestNewWithoutSecrets(t *testing.T) {
	t.Parallel()

	_, err := New(t.Context(), testOutput, Config{}, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()

	falseAlarm := finalUpdate()
	falseAlarm.Report.Verdict = report.FalsePositive
	falseAlarm.Report.FalsePositive = &report.FalsePositiveDetail{
		WhyFired:   "A nightly batch job raises p99 for two minutes.",
		Prevention: []report.Prevention{{Kind: report.PreventDuration, Detail: "Fire only after 10 minutes above 2s."}},
	}

	failed := finalUpdate()
	failed.Report = nil

	untrusted := finalUpdate()
	untrusted.Event.Title = "*urgent* [~accountid:123] {color:red}x{color}"
	untrusted.Report.Reason = "See [the fix|https://evil.example.com]\nh1. Heading\n!https://evil.example.com/p.png!"
	untrusted.Report.Findings = []report.Finding{{Claim: "line one\n\nline two", Evidence: []source.Evidence{{Title: "q", URL: "javascript:alert(1)"}}}}

	tests := []struct {
		name string
		u    output.Update
	}{
		{name: "true-positive", u: finalUpdate()},
		{name: "false-alarm", u: falseAlarm},
		{name: "failed", u: failed},
		{name: "untrusted", u: untrusted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := json.MarshalIndent(render(ticket.Layout(tt.u)), "", "  ")
			require.NoError(t, err)

			path := filepath.Join("fixture", "golden", tt.name+".json")
			if *update {
				require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o600))
			}

			want, err := os.ReadFile(path) //nolint:gosec // A fixture path chosen by the test.
			require.NoError(t, err, "run go test ./internal/output/jira -update to create the golden file")
			require.JSONEq(t, string(want), string(got))
		})
	}
}

func TestKeyFromURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		site   string
		url    string
		want   string
		wantOK bool
	}{
		{name: "an issue link", site: "https://acme.atlassian.net", url: "https://acme.atlassian.net/browse/OPS-123", want: "OPS-123", wantOK: true},
		{name: "a site with a path", site: "https://jira.example.com/jira/", url: "https://jira.example.com/jira/browse/ops-7/", want: "OPS-7", wantOK: true},
		{name: "the host in another case", site: "https://acme.atlassian.net", url: "https://ACME.atlassian.net/browse/OPS-1", want: "OPS-1", wantOK: true},
		{name: "another site", site: "https://acme.atlassian.net", url: "https://other.atlassian.net/browse/OPS-123"},
		{name: "plain http", site: "https://acme.atlassian.net", url: "http://acme.atlassian.net/browse/OPS-123"},
		{name: "a board link", site: "https://acme.atlassian.net", url: "https://acme.atlassian.net/jira/software/projects/OPS/boards/1"},
		{name: "not a key", site: "https://acme.atlassian.net", url: "https://acme.atlassian.net/browse/OPS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := newClient(tt.site, testEmail, nil, nil)
			require.NoError(t, err)

			got, ok := c.KeyFromURL(tt.url)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   fakehttp.Response
		want       ticket.Issue
		wantErr    error
		wantStatus int
	}{
		{name: "found", response: fakehttp.Response{BodyFile: "fixture/issue.json"}, want: ticket.Issue{ID: testIssueID, Key: "OPS-123", Scope: "OPS"}},
		{name: "not found", response: fakehttp.Response{Status: http.StatusNotFound, BodyFile: "fixture/not_found.json"}, wantErr: ticket.ErrNotFound},
		{name: "signed out", response: fakehttp.Response{Status: http.StatusUnauthorized}, wantStatus: http.StatusUnauthorized},
		{name: "a server error", response: fakehttp.Response{Status: http.StatusServiceUnavailable}, wantStatus: http.StatusServiceUnavailable},
		{name: "no ID", response: fakehttp.Response{Body: []byte(`{}`)}, wantErr: errNoID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, pathIssue, tt.response)
			api.ExpectAllCalled()

			got, err := testClient(t, api).Issue(t.Context(), "OPS-123")
			requireErr(t, err, tt.wantErr, tt.wantStatus)
			require.Equal(t, tt.want, got)
			require.Equal(t, "project", api.Requests()[0].Query.Get("fields"))
			requireSignedIn(t, api)
		})
	}
}

func TestCreateComment(t *testing.T) {
	t.Parallel()

	comment := ticket.Comment{
		IdempotencyKey: "sidekick:" + testOutput + ":" + testInvest + ":0",
		Doc:            ticket.Doc{Blocks: []ticket.Block{{Kind: ticket.Paragraph, Lines: []ticket.Line{{{Text: "Real issue"}}}}}},
	}

	tests := []struct {
		name       string
		setup      func(api *fakehttp.Server)
		want       string
		wantErr    error
		wantStatus int
	}{
		{
			name: "created with its idempotency key",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_empty.json"})
				api.Handle(http.MethodPost, pathComments, fakehttp.Response{Status: http.StatusCreated, BodyFile: "fixture/comment_created.json"})
			},
			want: testCommentID,
		},
		{
			name: "an earlier attempt went through, so its comment is edited",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_with_ours.json"})
				api.Handle(http.MethodPut, pathComment, fakehttp.Response{BodyFile: "fixture/comment_updated.json"})
			},
			want: testCommentID,
		},
		{
			name: "rate limited, then created",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_empty.json"})
				api.Handle(http.MethodPost, pathComments,
					fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "0"}},
					fakehttp.Response{Status: http.StatusCreated, BodyFile: "fixture/comment_created.json"},
				)
			},
			want: testCommentID,
		},
		{
			name: "a server error",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_empty.json"})
				api.Handle(http.MethodPost, pathComments, fakehttp.Response{Status: http.StatusInternalServerError})
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "the comments cannot be listed",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{Status: http.StatusBadGateway})
			},
			wantStatus: http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			tt.setup(api)
			api.ExpectAllCalled()

			got, err := testClient(t, api).CreateComment(t.Context(), ticket.Issue{ID: testIssueID, Key: "OPS-123"}, comment)
			requireErr(t, err, tt.wantErr, tt.wantStatus)
			require.Equal(t, tt.want, got)
			requireSignedIn(t, api)

			list := api.Requests()[0]
			require.Equal(t, "-created", list.Query.Get("orderBy"))
			require.Equal(t, "properties", list.Query.Get("expand"))

			for _, r := range api.Requests() {
				if r.Method != http.MethodPost {
					continue
				}

				var sent struct {
					Body       node       `json:"body"`
					Properties []property `json:"properties"`
				}

				require.NoError(t, json.Unmarshal(r.Body, &sent))
				require.Equal(t, []property{{Key: propertyKey, Value: propertyValue{IdempotencyKey: comment.IdempotencyKey}}}, sent.Properties)
				require.Equal(t, "doc", sent.Body.Type)
			}
		})
	}
}

func TestUpdateComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   fakehttp.Response
		wantErr    error
		wantStatus int
	}{
		{name: "updated", response: fakehttp.Response{BodyFile: "fixture/comment_updated.json"}},
		{name: "deleted", response: fakehttp.Response{Status: http.StatusNotFound}, wantErr: ticket.ErrNotFound},
		{name: "forbidden", response: fakehttp.Response{Status: http.StatusForbidden}, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPut, pathComment, tt.response)
			api.ExpectAllCalled()

			err := testClient(t, api).UpdateComment(t.Context(), ticket.Issue{ID: testIssueID}, testCommentID, ticket.Comment{})
			requireErr(t, err, tt.wantErr, tt.wantStatus)
			require.JSONEq(t, `{"body":{"type":"doc","version":1}}`, string(api.Requests()[0].Body))
		})
	}
}

func TestPublish(t *testing.T) {
	t.Parallel()

	posted := output.Ref{
		Sink:     testOutput,
		External: "OPS-123:" + testCommentID,
		Data:     map[string]string{"issue_id": testIssueID, "issue": "OPS-123", "comment": testCommentID, "seq": "5"},
	}

	tests := []struct {
		name    string
		edit    func(u *output.Update)
		setup   func(api *fakehttp.Server)
		want    output.Ref
		wantErr bool
	}{
		{
			name: "comments on the issue in the label",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathIssue, fakehttp.Response{BodyFile: "fixture/issue.json"})
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_empty.json"})
				api.Handle(http.MethodPost, pathComments, fakehttp.Response{Status: http.StatusCreated, BodyFile: "fixture/comment_created.json"})
			},
			want: posted,
		},
		{
			name: "comments on the issue the alert links to",
			edit: func(u *output.Update) {
				u.Event.Labels = nil
				u.Event.Links = []event.Link{{Title: "Ticket", URL: "{site}/browse/OPS-123"}}
			},
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathIssue, fakehttp.Response{BodyFile: "fixture/issue.json"})
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_empty.json"})
				api.Handle(http.MethodPost, pathComments, fakehttp.Response{Status: http.StatusCreated, BodyFile: "fixture/comment_created.json"})
			},
			want: posted,
		},
		{
			name: "no related issue posts nothing",
			edit: func(u *output.Update) { u.Event.Labels = map[string]string{defaultLabel: "SEC-1"} },
			want: output.Ref{Sink: testOutput, Data: map[string]string{"seq": "5"}},
		},
		{
			name: "an issue that moved to another project gets no comment",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathIssue, fakehttp.Response{BodyFile: "fixture/issue_moved.json"})
			},
			want: output.Ref{Sink: testOutput, Data: map[string]string{"seq": "5"}},
		},
		{
			name: "a deleted comment is posted again",
			edit: func(u *output.Update) {
				prev := posted
				prev.Data = map[string]string{"issue_id": testIssueID, "issue": "OPS-123", "comment": "10001", "seq": "4"}
				u.Previous = &prev
			},
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodPut, pathComments+"/10001", fakehttp.Response{Status: http.StatusNotFound})
				api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_empty.json"})
				api.Handle(http.MethodPost, pathComments, fakehttp.Response{Status: http.StatusCreated, BodyFile: "fixture/comment_created.json"})
			},
			want: output.Ref{Sink: testOutput, External: posted.External, Data: map[string]string{
				"issue_id": testIssueID, "issue": "OPS-123", "comment": testCommentID, "seq": "5", "generation": "1",
			}},
		},
		{
			name: "a server error fails the publish, so the job retries it",
			setup: func(api *fakehttp.Server) {
				api.Handle(http.MethodGet, pathIssue, fakehttp.Response{Status: http.StatusServiceUnavailable})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if tt.setup != nil {
				tt.setup(api)
			}

			api.ExpectAllCalled()

			u := finalUpdate()
			if tt.edit != nil {
				tt.edit(&u)
			}

			for i, l := range u.Event.Links {
				u.Event.Links[i].URL = api.URL() + l.URL[len("{site}"):]
			}

			got, err := testSink(t, api).Publish(t.Context(), u)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPublishRetriesAfterAServerError(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, pathIssue, fakehttp.Response{BodyFile: "fixture/issue.json"}, fakehttp.Response{BodyFile: "fixture/issue.json"})
	api.Handle(http.MethodGet, pathComments, fakehttp.Response{BodyFile: "fixture/comments_empty.json"}, fakehttp.Response{BodyFile: "fixture/comments_with_ours.json"})
	// The first create times out at the gateway after Jira saved the
	// comment; the retry finds it by its property and edits it.
	api.Handle(http.MethodPost, pathComments, fakehttp.Response{Status: http.StatusGatewayTimeout})
	api.Handle(http.MethodPut, pathComment, fakehttp.Response{BodyFile: "fixture/comment_updated.json"})
	api.ExpectAllCalled()

	sink := testSink(t, api)

	_, err := sink.Publish(t.Context(), finalUpdate())
	statusErr, ok := errors.AsType[*sinkhttp.StatusError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, http.StatusGatewayTimeout, statusErr.Status)

	got, err := sink.Publish(t.Context(), finalUpdate())
	require.NoError(t, err)
	require.Equal(t, "OPS-123:"+testCommentID, got.External)
}

// requireErr checks err: no error when want is nil and wantStatus is 0,
// a *sinkhttp.StatusError with wantStatus, or one that wraps want.
func requireErr(t *testing.T, err, want error, wantStatus int) {
	t.Helper()

	switch {
	case wantStatus != 0:
		statusErr, ok := errors.AsType[*sinkhttp.StatusError](err)
		require.True(t, ok, "got %v", err)
		require.Equal(t, wantStatus, statusErr.Status)
	case want != nil:
		require.ErrorIs(t, err, want)
	default:
		require.NoError(t, err)
	}
}

// testClient returns a client for the fake site.
func testClient(t *testing.T, api *fakehttp.Server) *client {
	t.Helper()

	hc, err := sinkhttp.New(Type, api.Client(), prometheus.NewRegistry())
	require.NoError(t, err)

	c, err := newClient(api.URL(), testEmail, hc, func(_ context.Context) (string, error) { return testToken, nil })
	require.NoError(t, err)

	return c
}
