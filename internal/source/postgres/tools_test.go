package postgres

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// update rewrites the tool snapshot from the source's tools.
var update = flag.Bool("update", false, "rewrite bench/tools/postgres.json from the source's tools")

// snapshotPath is the benchmark's tool snapshot of the source.
const snapshotPath = "../../../bench/tools/postgres.json"

// snapshotTool is one tool in a snapshot, in the shape of an MCP tools/list
// answer, so a server's list can be saved as a snapshot as it is.
type snapshotTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// TestToolSnapshot keeps the snapshot equal to the tools of a source with
// no databases, whose schemas name none, so a benchmark scenario can use
// any database name.
func TestToolSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "bench/tools/postgres.json matches the tools"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			snapshot := struct {
				Tools []snapshotTool `json:"tools"`
			}{}

			for _, tool := range toolList(nil) {
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
			require.Equal(t, string(want), string(got), "run go test ./internal/source/postgres -run TestToolSnapshot -update")
		})
	}
}

func TestToolSchemas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		databases []string
		wantEnum  []any
	}{
		{name: "the configured databases are the only choices", databases: []string{"orders", "billing"}, wantEnum: []any{"orders", "billing"}},
		{name: "no databases, no choices", databases: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			list := toolList(tt.databases)
			require.NotEmpty(t, list)

			for _, tool := range list {
				var s struct {
					Required   []string `json:"required"`
					Properties map[string]struct {
						Enum []any `json:"enum"`
					} `json:"properties"`
				}

				require.NoError(t, json.Unmarshal(tool.InputSchema, &s), tool.Name)
				require.Equal(t, []string{"database"}, s.Required, tool.Name)
				require.Equal(t, tt.wantEnum, s.Properties["database"].Enum, tool.Name)
			}
		})
	}
}

func TestInputLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "none gives the default", limit: 0, want: defaultLimit},
		{name: "a limit inside the range", limit: 5, want: 5},
		{name: "a limit over the maximum", limit: maxLimit + 1, want: maxLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, input{Limit: tt.limit}.limit())
		})
	}
}
