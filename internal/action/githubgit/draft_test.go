package githubgit

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=utf-8"}

// newFakeClient returns a go-github client that talks to a fresh fake GitHub.
func newFakeClient(t *testing.T) (*gh.Client, *fakehttp.Server) {
	t.Helper()

	fake := fakehttp.New(t)
	base := fake.URL() + "/"

	client, err := gh.NewClient(gh.WithHTTPClient(fake.Client()), gh.WithURLs(&base, nil))
	require.NoError(t, err)

	return client, fake
}

// calls returns the method and path of every request the fake received.
func calls(f *fakehttp.Server) []string {
	var out []string

	for _, r := range f.Requests() {
		out = append(out, r.Method+" "+r.Path)
	}

	return out
}

// pullJSON is a pull request as GitHub describes it.
func pullJSON(number int, state string, draft bool) string {
	return fmt.Sprintf(`{"number":%d,"state":%q,"draft":%t,"html_url":"https://github.com/acme/api/pull/%d"}`, number, state, draft, number)
}

// responseOf is a 200 JSON answer.
func responseOf(body string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(body)}
}

// fakehttpResponse is a JSON answer with a status.
func fakehttpResponse(status int, body string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: jsonHeaders, Body: []byte(body)}
}

// noContent is the answer to a delete.
func noContent() fakehttp.Response {
	return fakehttp.Response{Status: http.StatusNoContent}
}

func TestRequireDraft(t *testing.T) {
	t.Parallel()

	const branch = "sidekick/0192d3a4-0000-7000-8000-0000000000a1"

	closedAt := gh.Timestamp{}
	merged := &gh.PullRequest{Number: new(7), State: new("closed"), MergedAt: &closedAt}

	tests := []struct {
		name      string
		pr        *gh.PullRequest
		setup     func(f *fakehttp.Server)
		wantErr   string
		wantPerm  bool
		wantCalls []string
	}{
		{name: "a draft passes", pr: &gh.PullRequest{Number: new(7), State: new("open"), Draft: new(true)}},
		{name: "a merged pull request that is not a draft is left alone", pr: merged},
		{
			name: "an open pull request that is not a draft is closed and its branch deleted",
			pr:   &gh.PullRequest{Number: new(7), State: new("open")},
			setup: func(f *fakehttp.Server) {
				f.Handle(http.MethodPatch, "/repos/acme/api/pulls/7", fakehttp.Response{Headers: jsonHeaders, Body: []byte(pullJSON(7, "closed", false))})
				f.Handle(http.MethodDelete, "/repos/acme/api/git/refs/heads/"+branch, fakehttp.Response{Status: http.StatusNoContent})
			},
			wantErr:   "GitHub did not open acme/api#7 as a draft",
			wantPerm:  true,
			wantCalls: []string{"PATCH /repos/acme/api/pulls/7", "DELETE /repos/acme/api/git/refs/heads/" + branch},
		},
		{
			name: "a closed pull request that is not a draft only has its branch deleted",
			pr:   &gh.PullRequest{Number: new(7), State: new("closed")},
			setup: func(f *fakehttp.Server) {
				f.Handle(http.MethodDelete, "/repos/acme/api/git/refs/heads/"+branch, fakehttp.Response{Status: http.StatusUnprocessableEntity, Headers: jsonHeaders, Body: []byte(`{"message":"Reference does not exist"}`)})
			},
			wantErr:   "so Sidekick closed it and deleted its branch",
			wantPerm:  true,
			wantCalls: []string{"DELETE /repos/acme/api/git/refs/heads/" + branch},
		},
		{
			name: "a pull request that cannot be closed",
			pr:   &gh.PullRequest{Number: new(7), State: new("open")},
			setup: func(f *fakehttp.Server) {
				f.Handle(http.MethodPatch, "/repos/acme/api/pulls/7", fakehttp.Response{Status: http.StatusBadGateway, Headers: jsonHeaders, Body: []byte(`{"message":"Bad gateway"}`)})
			},
			wantErr:   "close the pull request acme/api#7 that is not a draft",
			wantCalls: []string{"PATCH /repos/acme/api/pulls/7"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, fake := newFakeClient(t)
			if tt.setup != nil {
				tt.setup(fake)
			}

			err := RequireDraft(t.Context(), client, "acme", "api", branch, tt.pr)
			require.Equal(t, tt.wantCalls, calls(fake))

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantPerm, errors.Is(err, action.ErrPermanent))
		})
	}
}

func TestOpenDraft(t *testing.T) {
	t.Parallel()

	const branch = "sidekick/0192d3a4-0000-7000-8000-0000000000a1"

	req := gh.CreatePullRequest{Title: new("t"), Head: branch, Base: "main", Draft: new(true)}

	t.Run("a draft is returned", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeClient(t)
		fake.Handle(http.MethodPost, "/repos/acme/api/pulls", fakehttp.Response{Status: http.StatusCreated, Headers: jsonHeaders, Body: []byte(pullJSON(12, "open", true))})

		pr, err := OpenDraft(t.Context(), client, "acme", "api", branch, req)
		require.NoError(t, err)
		require.Equal(t, 12, pr.GetNumber())
		require.Equal(t, []string{"POST /repos/acme/api/pulls"}, calls(fake))
	})

	t.Run("a pull request that is not a draft is closed and refused", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeClient(t)
		fake.Handle(http.MethodPost, "/repos/acme/api/pulls", fakehttp.Response{Status: http.StatusCreated, Headers: jsonHeaders, Body: []byte(pullJSON(12, "open", false))})
		fake.Handle(http.MethodPatch, "/repos/acme/api/pulls/12", fakehttp.Response{Headers: jsonHeaders, Body: []byte(pullJSON(12, "closed", false))})
		fake.Handle(http.MethodDelete, "/repos/acme/api/git/refs/heads/"+branch, fakehttp.Response{Status: http.StatusNoContent})

		_, err := OpenDraft(t.Context(), client, "acme", "api", branch, req)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "check that the pull request is a draft")
	})

	t.Run("a pull request that cannot be opened", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeClient(t)
		fake.Handle(http.MethodPost, "/repos/acme/api/pulls", fakehttp.Response{Status: http.StatusForbidden, Headers: jsonHeaders, Body: []byte(`{"message":"Forbidden"}`)})

		_, err := OpenDraft(t.Context(), client, "acme", "api", branch, req)
		require.ErrorContains(t, err, "open the pull request")
	})
}
