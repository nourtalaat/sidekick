package kubernetes

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// update rewrites the tool snapshot: go test ./internal/source/kubernetes -update.
var update = flag.Bool("update", false, "rewrite the tool snapshot in bench/tools/")

// snapshotPath is the benchmark's snapshot of this source's tools.
var snapshotPath = filepath.Join("..", "..", "..", "bench", "tools", "kubernetes.json")

// TestSnapshot keeps the benchmark's tool snapshot the same as the tools
// the source lists with logs on.
func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "logs on, every namespace", cfg: Config{Logs: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			list, err := newTestSource(t, tt.cfg, nil).Tools(t.Context())
			require.NoError(t, err)

			type snapshotTool struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			}

			var snapshot struct {
				Tools []snapshotTool `json:"tools"`
			}

			for _, tool := range list {
				snapshot.Tools = append(snapshot.Tools, snapshotTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
			}

			got, err := json.MarshalIndent(snapshot, "", "  ")
			require.NoError(t, err)

			got = append(got, '\n')

			if *update {
				require.NoError(t, os.WriteFile(snapshotPath, got, 0o600))
			}

			want, err := os.ReadFile(snapshotPath) //nolint:gosec // A fixed path chosen by the test.
			require.NoError(t, err, "run go test ./internal/source/kubernetes -update to create the snapshot")
			require.Equal(t, string(want), string(got))
		})
	}
}
