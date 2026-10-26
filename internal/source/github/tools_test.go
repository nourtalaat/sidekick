package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// update rewrites the tool snapshot from the source's tools.
var update = flag.Bool("update", false, "rewrite bench/tools/github.json from the source's tools")

// snapshotPath is the benchmark's tool snapshot of the source.
const snapshotPath = "../../../bench/tools/github.json"

// snapshotTool is one tool in a snapshot, in the shape of an MCP tools/list
// answer, so a server's list can be saved as a snapshot as it is.
type snapshotTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func TestToolSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "bench/tools/github.json matches the tools"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			list, err := (&Source{}).Tools(t.Context())
			require.NoError(t, err)

			snapshot := struct {
				Tools []snapshotTool `json:"tools"`
			}{}

			for _, tool := range list {
				require.True(t, tool.ReadOnly, "%s only reads", tool.Name)
				snapshot.Tools = append(snapshot.Tools, snapshotTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
			}

			got, err := json.MarshalIndent(snapshot, "", "  ")
			require.NoError(t, err)

			got = append(got, '\n')

			if *update {
				require.NoError(t, os.WriteFile(snapshotPath, got, 0o600))
			}

			want, err := os.ReadFile(snapshotPath)
			require.NoError(t, err)
			require.Equal(t, string(want), string(got), "run go test ./internal/source/github -run TestToolSnapshot -update")
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
		{name: "no repository", tool: toolListCommits, input: `{"since":"2026-05-04T09:00:00Z"}`, want: "give repo (owner/name) or service"},
		{
			name:  "both repo and service",
			tool:  toolListCommits,
			input: `{"repo":"acme/checkout","service":"checkout-api"}`,
			want:  "give repo or service, not both",
		},
		{
			name:  "an unknown service",
			tool:  toolGetCommit,
			input: `{"service":"billing","ref":"main"}`,
			want:  `no repository is configured for service "billing"; known services: checkout-api`,
		},
		{
			name:  "a repository the config does not allow",
			tool:  toolGetCommit,
			input: `{"repo":"other/secret","ref":"main"}`,
			want:  `repo "other/secret" is not one Sidekick may read; allowed: acme/*`,
		},
		{name: "a repository name that climbs out", tool: toolGetCommit, input: `{"repo":"acme/..","ref":"main"}`, want: `repo "acme/.." must have the form owner/name`},
		{
			name:  "a time that does not parse",
			tool:  toolListPullRequests,
			input: `{"repo":"acme/checkout","since":"yesterday"}`,
			want:  `since: "yesterday" is not an RFC 3339 time such as 2026-05-04T10:00:00Z`,
		},
		{name: "a ref with dots", tool: toolGetCommit, input: `{"repo":"acme/checkout","ref":"main/../../x"}`, want: `ref: "main/../../x" is not a valid branch, tag or commit SHA`},
		{name: "a ref with a query", tool: toolGetFile, input: `{"repo":"acme/checkout","path":"a","ref":"main?x=1"}`, want: `ref: "main?x=1" is not a valid branch, tag or commit SHA`},
		{name: "no ref", tool: toolGetCommit, input: `{"repo":"acme/checkout"}`, want: "give ref, the commit SHA"},
		{name: "a path that climbs out", tool: toolGetFile, input: `{"repo":"acme/checkout","path":"config/../../x"}`, want: `path: "config/../../x" is not a path inside the repository`},
		{name: "a workflow with a slash", tool: toolListWorkflowRuns, input: `{"repo":"acme/checkout","workflow":"a/b"}`, want: `workflow: "a/b" is not a workflow file name or ID`},
		{name: "compare without head", tool: toolCompare, input: `{"repo":"acme/checkout","base":"main"}`, want: "give both base and head"},
		{name: "input that does not decode", tool: toolListReleases, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, 0, 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches GitHub")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "create_pull_request"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), 0, 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

// TestThroughTheGuard shows what the model sees: every tool with the
// source prefix, input the schema refuses never reaching GitHub, and a
// result wrapped as untrusted data with its evidence.
func TestThroughTheGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        string
		wantIsError  bool
		wantEvidence int
	}{
		{name: "a call that reads commits", input: `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z"}`, wantEvidence: 2},
		{name: "input that fails the schema never reaches GitHub", input: `{"repo":"acme/checkout","since":"2026-05-04T09:00:00Z","author":"ada"}`, wantIsError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			exchanges := 0
			if !tt.wantIsError {
				exchanges = 1

				api.Handle(http.MethodPost, testExchangePath, tokenResponse("ghs_1", time.Now().Add(time.Hour)))
				api.Handle(http.MethodGet, "/repos/acme/checkout/commits", fixture("commits-page-1.json"))
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api, exchanges, 0)

			r, err := redact.New(redact.Options{HashKey: []byte("test")})
			require.NoError(t, err)

			g, err := tools.New([]tools.Source{{Name: "github", Source: s}}, tools.Options{Redactor: r})
			require.NoError(t, err)

			run := g.Start(t.Context(), "inv-1", nil)

			var names []string
			for _, spec := range run.Tools() {
				names = append(names, spec.Name)
			}

			require.Equal(t, []string{
				"github__list_commits", "github__get_commit", "github__list_pull_requests", "github__list_releases",
				"github__list_workflow_runs", "github__get_file", "github__compare",
			}, names)

			res := run.Call(t.Context(), model.ToolCall{ID: "call-1", Name: "github__list_commits", Input: json.RawMessage(tt.input)})
			require.Equal(t, tt.wantIsError, res.ToolResult.IsError)
			require.Len(t, res.Evidence, tt.wantEvidence)

			if !tt.wantIsError {
				require.Contains(t, res.ToolResult.Content, "Lower DB pool size to 5")
			}
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
	}{
		{name: "github.com by default", cfg: Config{}},
		{name: "no secrets resolver", cfg: Config{}, wantErr: errNoSecrets},
		{
			name:    "settings the guard would refuse",
			cfg:     Config{Settings: source.Settings{Tools: source.ToolSettings{Rename: map[string]string{toolGetFile: "bad name"}}}},
			wantMsg: "tools.rename.get_file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deps := registry.Deps{}
			if tt.wantErr == nil {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "github", tt.cfg, deps)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantMsg != "":
				require.ErrorContains(t, err, tt.wantMsg)
			default:
				require.NoError(t, err)
				built, ok := s.(*Source)
				require.True(t, ok)
				require.Equal(t, defaultBaseURL, built.client.BaseURL())
			}
		})
	}
}

func TestLimitedBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		limit   int64
		wantErr error
	}{
		{name: "shorter than the limit", body: "abc", limit: 5},
		{name: "exactly the limit", body: "abcde", limit: 5},
		{name: "longer than the limit", body: "abcdef", limit: 5, wantErr: errResponseTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &limitedBody{ReadCloser: io.NopCloser(bytes.NewBufferString(tt.body)), left: tt.limit}

			var got bytes.Buffer

			_, err := got.ReadFrom(b)
			require.ErrorIs(t, err, tt.wantErr)

			if tt.wantErr == nil {
				require.Equal(t, tt.body, got.String())
			}
		})
	}
}
