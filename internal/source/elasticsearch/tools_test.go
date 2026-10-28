package elasticsearch

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// update rewrites the tool snapshot from the source's tools.
var update = flag.Bool("update", false, "rewrite bench/tools/elasticsearch.json from the source's tools")

// snapshotPath is the benchmark's tool snapshot of the source.
const snapshotPath = "../../../bench/tools/elasticsearch.json"

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
		{name: "bench/tools/elasticsearch.json matches the tools"},
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
			require.Equal(t, string(want), string(got), "run go test ./internal/source/elasticsearch -run TestToolSnapshot -update")
		})
	}
}
