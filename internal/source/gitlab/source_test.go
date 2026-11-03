package gitlab

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Shared data of the commit tests.
const (
	abcSHA = "abc1234def5678abc1234def5678abc1234def56"
	botSHA = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"
)

func TestSourceCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tool     string
		input    string
		pageSize int
		routes   []route
		// wantQueries are the encoded queries of the requests, in order.
		wantQueries   []string
		want          string
		wantEvidence  []source.Evidence
		wantTruncated bool
		wantNoData    bool
		wantText      string
		wantToolError string
		wantErr       string
	}{
		{
			name:     "list_commits pages through the commits",
			tool:     toolListCommits,
			input:    `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","path":"config/","limit":3}`,
			pageSize: 2,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				page("commits-page-1.json", 2), page("commits-page-2.json", 0),
			}}},
			wantQueries: []string{
				"page=1&path=config%2F&per_page=2&since=2026-05-04T09%3A00%3A00Z",
				"page=2&path=config%2F&per_page=2&since=2026-05-04T09%3A00%3A00Z",
			},
			want: `{"project":"acme/checkout","commits":[
				{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.","author":"Ada Lovelace","date":"2026-05-04T10:05:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/abc1234def5678abc1234def5678abc1234def56"},
				{"sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","message":"Bump the image tag","author":"Deploy Bot","date":"2026-05-04T09:30:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"},
				{"sha":"9988776655443322110099887766554433221100","message":"Add a retry to the payment client","author":"Grace Hopper","date":"2026-05-04T09:10:00Z"}
			]}`,
			wantEvidence: []source.Evidence{
				commitLink("abc1234d", abcSHA, at(10, 5)),
				commitLink("0f1e2d3c", botSHA, at(9, 30)),
			},
		},
		{
			name:     "list_commits stops at the limit and finds the project by service",
			tool:     toolListCommits,
			input:    `{"service":"checkout-api","since":"2026-05-04T09:00:00Z","ref":"feature/x","limit":2}`,
			pageSize: 2,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				page("commits-page-1.json", 2),
			}}},
			wantQueries: []string{"page=1&per_page=2&ref_name=feature%2Fx&since=2026-05-04T09%3A00%3A00Z"},
			want: `{"project":"acme/checkout","commits":[
				{"sha":"abc1234def5678abc1234def5678abc1234def56","message":"Lower DB pool size to 5 to save connections\n\nThe pool was too large for the new database.","author":"Ada Lovelace","date":"2026-05-04T10:05:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/abc1234def5678abc1234def5678abc1234def56"},
				{"sha":"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c","message":"Bump the image tag","author":"Deploy Bot","date":"2026-05-04T09:30:00Z","url":"https://gitlab.example.com/acme/checkout/-/commit/0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"}
			]}`,
			wantEvidence: []source.Evidence{
				commitLink("abc1234d", abcSHA, at(10, 5)),
				commitLink("0f1e2d3c", botSHA, at(9, 30)),
			},
			wantTruncated: true,
		},
		{
			name:        "list_commits sends times in UTC",
			tool:        toolListCommits,
			input:       `{"project":"acme/checkout","since":"2026-05-04T11:00:00+02:00","until":"2026-05-04T12:00:00Z"}`,
			routes:      []route{{http.MethodGet, commitsPath, []fakehttp.Response{body("[]")}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z&until=2026-05-04T12%3A00%3A00Z"},
			wantNoData:  true,
		},
		{
			name:  "a project GitLab does not have is no data",
			tool:  toolListCommits,
			input: `{"project":"acme/missing","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, apiRoot + "/projects/acme/missing/repository/commits", []fakehttp.Response{
				failed(http.StatusNotFound, "404 Project Not Found"),
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantNoData:  true,
			wantText:    notFoundText,
		},
		{
			name:  "a request GitLab refuses is a tool error with its message",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				failed(http.StatusForbidden, "403 Forbidden"),
			}}},
			wantQueries:   []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantToolError: "GitLab answered 403: 403 Forbidden",
		},
		{
			name:  "an error message that is an object is kept as JSON",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				{Status: http.StatusBadRequest, Headers: jsonHeaders, Body: []byte(`{"message":{"ref_name":["is invalid"]}}`)},
			}}},
			wantQueries:   []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantToolError: `GitLab answered 400: {"ref_name":["is invalid"]}`,
		},
		{
			name:  "a refused token is a failure of the source",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				failed(http.StatusUnauthorized, "401 Unauthorized"),
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantErr:     "GitLab answered 401: 401 Unauthorized",
		},
		{
			name:  "a rate limit is a failure of the source",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "30", "Content-Type": "text/plain"}, Body: []byte("Retry later")},
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantErr:     "GitLab answered 429",
		},
		{
			name:  "GitLab failing is a failure of the source",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				failed(http.StatusBadGateway, "502 Bad Gateway"),
			}}},
			wantQueries: []string{"page=1&per_page=30&since=2026-05-04T09%3A00%3A00Z"},
			wantErr:     "GitLab answered 502: 502 Bad Gateway",
		},
		{
			name:  "a second page that fails fails the call",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","limit":3}`,
			routes: []route{{http.MethodGet, commitsPath, []fakehttp.Response{
				page("commits-page-1.json", 2), failed(http.StatusInternalServerError, "500 Internal Server Error"),
			}}},
			pageSize: 2,
			wantQueries: []string{
				"page=1&per_page=2&since=2026-05-04T09%3A00%3A00Z",
				"page=2&per_page=2&since=2026-05-04T09%3A00%3A00Z",
			},
			wantErr: "read page 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			var paths []string

			for _, r := range tt.routes {
				api.Handle(r.method, r.path, r.responses...)
				paths = append(paths, r.path)
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api, tt.pageSize)

			got, err := s.Call(t.Context(), source.Call{ID: "call-1", Tool: tt.tool, Input: json.RawMessage(tt.input)})
			require.Equal(t, tt.wantQueries, queries(api, paths))

			switch {
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)

				return
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)
				_, isToolErr := errors.AsType[*source.ToolError](err)
				require.False(t, isToolErr, "a failure of the source is not a tool error")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantNoData, got.NoData)
			require.Equal(t, tt.wantText, got.Text)
			require.Equal(t, tt.wantTruncated, got.Truncated)
			require.Equal(t, tt.wantEvidence, got.Evidence)

			if tt.want == "" {
				require.Empty(t, got.Structured)

				return
			}

			require.JSONEq(t, tt.want, string(got.Structured))
		})
	}
}

func TestSourceCallInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{
			name:  "no project",
			tool:  toolListCommits,
			input: `{"since":"2026-05-04T09:00:00Z"}`,
			want:  "give project (the full path, such as group/project) or service",
		},
		{
			name:  "both project and service",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","service":"checkout-api"}`,
			want:  "give project or service, not both",
		},
		{
			name:  "an unknown service",
			tool:  toolListCommits,
			input: `{"service":"billing"}`,
			want:  `no project is configured for service "billing"; known services: checkout-api`,
		},
		{
			name:  "a project the config does not allow",
			tool:  toolListCommits,
			input: `{"project":"other/secret"}`,
			want:  `project "other/secret" is not one Sidekick may read; allowed: acme/**`,
		},
		{name: "a project path that climbs out", tool: toolListCommits, input: `{"project":"acme/.."}`, want: `project "acme/.." must be a full path such as group/project`},
		{name: "a numeric project ID", tool: toolListCommits, input: `{"project":"42"}`, want: `project "42" must be a full path such as group/project`},
		{
			name:  "an encoded project path",
			tool:  toolListCommits,
			input: `{"project":"acme%2Fcheckout"}`,
			want:  `project "acme%2Fcheckout" must be a full path such as group/project`,
		},
		{
			name:  "a project path with a query",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout?x=1"}`,
			want:  `project "acme/checkout?x=1" must be a full path such as group/project`,
		},
		{
			name:  "a time that does not parse",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"yesterday"}`,
			want:  `since: "yesterday" is not an RFC 3339 time such as 2026-05-04T10:00:00Z`,
		},
		{
			name:  "a ref with dots",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","ref":"main/../../x"}`,
			want:  `ref: "main/../../x" is not a valid branch, tag or commit SHA`,
		},
		{
			name:  "a ref with a query",
			tool:  toolListCommits,
			input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","ref":"main?x=1"}`,
			want:  `ref: "main?x=1" is not a valid branch, tag or commit SHA`,
		},
		{name: "input that does not decode", tool: toolListCommits, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches GitLab")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "create_merge_request"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
		wantMsg string
		wantAPI string
	}{
		{name: "gitlab.com by default", cfg: Config{}, wantAPI: "https://gitlab.com/api/v4"},
		{name: "a self-managed GitLab under a sub-path", cfg: Config{BaseURL: "https://example.com/gitlab/"}, wantAPI: "https://example.com/gitlab/api/v4"},
		{name: "no secrets resolver", cfg: Config{}, wantErr: errNoSecrets},
		{
			name:    "settings the guard would refuse",
			cfg:     Config{Settings: source.Settings{Tools: source.ToolSettings{Rename: map[string]string{toolListCommits: "bad name"}}}},
			wantMsg: "tools.rename.list_commits",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deps := registry.Deps{}
			if tt.wantErr == nil {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "gitlab", tt.cfg, deps)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantMsg != "":
				require.ErrorContains(t, err, tt.wantMsg)
			default:
				require.NoError(t, err)

				built, ok := s.(*Source)
				require.True(t, ok)
				require.Equal(t, tt.wantAPI, built.client.apiURL)
			}
		})
	}
}
