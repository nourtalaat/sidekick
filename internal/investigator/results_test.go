package investigator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestToolResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		steps   []db.Step
		want    []ToolResult
		wantErr bool
	}{
		{
			name: "only tool results, in order",
			steps: []db.Step{
				step(t, 0, kindPrompt, "", "", statusOK, model.Text{Text: "the prompt"}),
				step(t, 1, kindModelTurn, "", "", statusOK, model.ToolCall{ID: "c1", Name: "signoz__search_logs", Input: json.RawMessage(`{}`)}),
				step(t, 2, kindToolResult, "signoz", "search_logs", statusOK, model.ToolResult{CallID: "c1", Content: "error rate 5%"}),
				step(t, 3, kindToolResult, "k8s", "get_pod", statusUnavailable, model.ToolResult{CallID: "c2", Content: "down", IsError: true}),
				step(t, 4, kindToolResult, "", "submit_verdict", statusOK, model.ToolResult{CallID: "v1", Content: "accepted"}),
			},
			want: []ToolResult{
				{CallID: "c1", Source: "signoz", Tool: "search_logs", Status: statusOK, Content: "error rate 5%"},
				{CallID: "c2", Source: "k8s", Tool: "get_pod", Status: statusUnavailable, Content: "down", IsError: true},
				{CallID: "v1", Tool: "submit_verdict", Status: statusOK, Content: "accepted"},
			},
		},
		{name: "no steps"},
		{
			name:    "parts that do not decode",
			steps:   []db.Step{{Seq: 2, Kind: kindToolResult, Parts: json.RawMessage(`[{"type": "image"}]`)}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ToolResults(tt.steps)
			if tt.wantErr {
				require.ErrorContains(t, err, "step 2")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// step returns a saved step with one part. Empty source and tool are
// stored as NULL.
func step(t *testing.T, seq int32, kind, src, tool, status string, part model.Part) db.Step {
	t.Helper()

	parts, err := encodeParts([]model.Part{part})
	require.NoError(t, err)

	s := db.Step{Seq: seq, Kind: kind, Status: status, Parts: parts}

	if src != "" {
		s.Source = &src
	}

	if tool != "" {
		s.Tool = &tool
	}

	if tr, ok := part.(model.ToolResult); ok {
		s.ToolCallID = &tr.CallID
	}

	return s
}
