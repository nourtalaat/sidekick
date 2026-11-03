package githubgit

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestCheckBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{name: "empty means github.com"},
		{name: "https", in: "https://github.example.com/api/v3/"},
		{name: "plain http on localhost", in: "http://localhost:8080/"},
		{name: "plain http on loopback", in: "http://127.0.0.1:8080/"},
		{name: "not a URL", in: "github.example.com", wantErr: `baseURL: "github.example.com" must be an absolute https URL`},
		{name: "plain http elsewhere", in: "http://github.example.com/", wantErr: "must use https; plain http is only allowed for localhost"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckBaseURL(tt.in)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestBaseURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://api.github.com/", BaseURL(""))
	require.Equal(t, "https://ghe.example.com/api/v3/", BaseURL("https://ghe.example.com/api/v3"))
	require.Equal(t, "https://ghe.example.com/api/v3/", BaseURL("https://ghe.example.com/api/v3/"))
}

func TestRepos(t *testing.T) {
	t.Parallel()

	require.Len(t, CheckRepos(nil), 1)
	require.Empty(t, CheckRepos([]string{"acme/api", "acme/web.v2"}))
	require.Len(t, CheckRepos([]string{"acme", "acme/*", "../x", "a/.."}), 4)

	require.True(t, AllowsRepo([]string{"acme/API"}, "Acme/api"))
	require.False(t, AllowsRepo([]string{"acme/api"}, "acme/other"))
	require.False(t, ValidRepo("./api"))
}

func TestStateOfAndStatusOf(t *testing.T) {
	t.Parallel()

	at := gh.Timestamp{Time: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}

	tests := []struct {
		name string
		pr   *gh.PullRequest
		want string
	}{
		{name: "open", pr: &gh.PullRequest{Number: new(1), State: new("open")}, want: gitchange.StateOpen},
		{name: "closed", pr: &gh.PullRequest{Number: new(1), State: new("closed"), ClosedAt: &at}, want: gitchange.StateClosed},
		{name: "merged", pr: &gh.PullRequest{Number: new(1), State: new("closed"), MergedAt: &at}, want: gitchange.StateMerged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, StateOf(tt.pr))
			require.Equal(t, tt.want, StatusOf(tt.pr).State)
		})
	}
}

func TestRepoOfParams(t *testing.T) {
	t.Parallel()

	owner, name, repo, err := RepoOfParams(json.RawMessage(`{"repo":"acme/api","title":"x"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"acme", "api", "acme/api"}, []string{owner, name, repo})

	for _, bad := range []string{`{`, `{"repo":"acme"}`, `{"repo":"../x"}`} {
		_, _, _, err := RepoOfParams(json.RawMessage(bad))
		require.ErrorIs(t, err, action.ErrPermanent, bad)
	}
}

func TestDescriptionAndCommitMessage(t *testing.T) {
	t.Parallel()

	body := Description("Fixes #3 for @bob ![x](http://a/b.png)", "p1", "i1", "https://sidekick.example.com")
	require.Contains(t, body, "`#3`")
	require.Contains(t, body, "`@bob`")
	require.Contains(t, body, `!\[x]`)
	require.Contains(t, body, "Opened by Sidekick for proposal `p1`.")
	require.Contains(t, body, "[The investigation](")
	require.NotContains(t, Description("x", "p1", "i1", ""), "The investigation")

	msg := CommitMessage("A "+strings.Repeat("long ", 40)+"title\nsecond line", "p1")
	title, rest, _ := strings.Cut(msg, "\n")
	require.LessOrEqual(t, len(title), MaxCommitTitleBytes)
	require.Equal(t, "\nSidekick proposal p1\n", rest)
}

func TestUndoBranch(t *testing.T) {
	t.Parallel()

	const (
		original = "0192d3a4-0000-7000-8000-0000000000a1"
		undo     = "0192d3a4-0000-7000-8000-0000000000a2"
	)

	branch, err := UndoBranch(action.Invocation{UndoOf: original, IdempotencyKey: action.IdempotencyKey(undo)})
	require.NoError(t, err)
	require.Equal(t, "sidekick/"+original, branch)

	_, err = UndoBranch(action.Invocation{IdempotencyKey: action.IdempotencyKey(undo)})
	require.ErrorIs(t, err, ErrNoUndoOf)

	_, err = UndoBranch(action.Invocation{UndoOf: original, IdempotencyKey: "other"})
	require.ErrorIs(t, err, ErrNoBranch)
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestClassify(t *testing.T) {
	t.Parallel()

	resp := func(status int) error {
		return &gh.ErrorResponse{Response: &http.Response{StatusCode: status}}
	}

	require.ErrorIs(t, Classify("read", resp(http.StatusNotFound)), action.ErrPermanent)
	require.ErrorIs(t, Classify("read", resp(http.StatusUnprocessableEntity)), action.ErrPermanent)
	require.NotErrorIs(t, Classify("read", resp(http.StatusBadGateway)), action.ErrPermanent)

	limited := &gh.RateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden, Request: &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/x"}}}}
	require.NotErrorIs(t, Classify("read", limited), action.ErrPermanent)
	require.True(t, IsStatus(resp(http.StatusNotFound), http.StatusNotFound))
	require.False(t, IsStatus(resp(http.StatusNotFound), http.StatusGone))
}

func TestAlreadyExists(t *testing.T) {
	t.Parallel()

	exists := &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}, Message: "Reference already exists"}
	inList := &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}, Errors: []gh.Error{{Message: "A pull request already exists for acme:b"}}}
	other := &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}, Message: "Validation Failed"}

	require.True(t, AlreadyExists(exists))
	require.True(t, AlreadyExists(inList))
	require.False(t, AlreadyExists(other))
	require.False(t, AlreadyExists(nil))
}
