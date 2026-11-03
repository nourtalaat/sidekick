package gitlab

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
)

// update rewrites the tool snapshot from the source's tools.
var update = flag.Bool("update", false, "rewrite bench/tools/gitlab.json from the source's tools")

// snapshotPath is the benchmark's tool snapshot of the source.
const snapshotPath = "../../../bench/tools/gitlab.json"

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
		{name: "bench/tools/gitlab.json matches the tools"},
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
			require.Equal(t, string(want), string(got), "run go test ./internal/source/gitlab -run TestToolSnapshot -update")
		})
	}
}

// TestThroughTheGuard shows what the model sees: every tool with the source
// prefix, input the schema refuses never reaching GitLab, and a result
// wrapped as untrusted data with its evidence.
func TestThroughTheGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        string
		wantIsError  bool
		wantEvidence int
	}{
		{name: "a call that reads commits", input: `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z"}`, wantEvidence: 2},
		{
			name:        "input that fails the schema never reaches GitLab",
			input:       `{"project":"acme/checkout","since":"2026-05-04T09:00:00Z","author":"ada"}`,
			wantIsError: true,
		},
		{
			name:        "a project outside the allowed list is a tool error and never reaches GitLab",
			input:       `{"project":"other/secret","since":"2026-05-04T09:00:00Z"}`,
			wantIsError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			if !tt.wantIsError {
				api.Handle(http.MethodGet, commitsPath, page("commits-page-1.json", 0))
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api, 0)

			r, err := redact.New(redact.Options{HashKey: []byte("test")})
			require.NoError(t, err)

			g, err := tools.New([]tools.Source{{Name: "gitlab", Source: s}}, tools.Options{Redactor: r})
			require.NoError(t, err)

			run := g.Start(t.Context(), "inv-1", nil)

			var names []string
			for _, spec := range run.Tools() {
				names = append(names, spec.Name)
			}

			require.Equal(t, []string{
				"gitlab__list_commits", "gitlab__get_commit", "gitlab__list_merge_requests", "gitlab__list_releases",
				"gitlab__list_pipelines", "gitlab__get_file", "gitlab__compare", "gitlab__list_deployments",
			}, names)

			res := run.Call(t.Context(), model.ToolCall{ID: "call-1", Name: "gitlab__list_commits", Input: json.RawMessage(tt.input)})
			require.Equal(t, tt.wantIsError, res.ToolResult.IsError)
			require.Len(t, res.Evidence, tt.wantEvidence)

			if !tt.wantIsError {
				require.Contains(t, res.ToolResult.Content, "Lower DB pool size to 5")
			}
		})
	}
}
