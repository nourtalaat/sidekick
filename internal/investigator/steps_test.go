package investigator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// testStart is the start time of the steps in the unit tests.
var testStart = time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC)

func TestPartsRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		parts []model.Part
		want  []model.Part
	}{
		{
			name: "every part type",
			parts: []model.Part{
				model.Text{Text: "I will search.", Cache: true},
				model.Reasoning{Provider: "anthropic", ID: "r1", Text: "Think.", Signature: "sig", Redacted: []byte{1, 2}},
				model.ToolCall{ID: "c1", Name: searchTool, Input: json.RawMessage(`{"query":"x"}`)},
				model.ToolResult{CallID: "c1", Content: "found", IsError: true},
			},
		},
		{
			name:  "a NUL character in text",
			parts: []model.Part{model.Text{Text: "a\x00b"}},
			want:  []model.Part{model.Text{Text: "a�b"}},
		},
		{
			name:  "tool input that is not JSON",
			parts: []model.Part{model.ToolCall{ID: "c1", Name: searchTool, Input: json.RawMessage(`{`)}},
			want:  []model.Part{model.ToolCall{ID: "c1", Name: searchTool, Input: json.RawMessage(`{}`)}},
		},
		{name: "no parts", parts: nil, want: []model.Part{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := encodeParts(tt.parts)
			require.NoError(t, err)
			require.NotContains(t, string(raw), `\u0000`, "jsonb cannot hold it")

			got, err := decodeParts(raw)
			require.NoError(t, err)

			want := tt.want
			if want == nil {
				want = tt.parts
			}

			require.Equal(t, want, got)
		})
	}
}

func TestDecodePartsRejectsUnknownTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown type", raw: `[{"type": "image"}]`},
		{name: "not JSON", raw: `[`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeParts(json.RawMessage(tt.raw))
			require.Error(t, err)
		})
	}
}

func TestHistory(t *testing.T) {
	t.Parallel()

	prompt := model.Request{
		System:   []model.Text{{Text: "system"}, {Text: "strategy", Cache: true}},
		Tools:    []model.ToolSpec{{Name: searchTool, Description: "Search.", InputSchema: json.RawMessage(`{"type":"object"}`), Cache: true}},
		Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "event"}}}},
	}

	c1 := model.ToolCall{ID: "c1", Name: searchTool, Input: json.RawMessage(`{}`)}
	c2 := model.ToolCall{ID: "c2", Name: searchTool, Input: json.RawMessage(`{}`)}
	r1 := model.ToolResult{CallID: "c1", Content: "one"}
	r2 := model.ToolResult{CallID: "c2", Content: "two"}
	note := model.Text{Text: "Call submit_verdict."}

	tests := []struct {
		name  string
		steps []db.Step
		// want are the messages after the prompt's.
		want           []model.Message
		wantUnanswered []model.ToolCall
		wantReminder   bool
	}{
		{name: "only the prompt"},
		{
			name:           "a turn with no results yet",
			steps:          []db.Step{partsStep(t, kindModelTurn, c1, c2)},
			want:           []model.Message{{Role: model.Assistant, Parts: []model.Part{c1, c2}}},
			wantUnanswered: []model.ToolCall{c1, c2},
		},
		{
			name:           "a turn with one result of two",
			steps:          []db.Step{partsStep(t, kindModelTurn, c1, c2), resultStep(t, r1)},
			want:           []model.Message{{Role: model.Assistant, Parts: []model.Part{c1, c2}}, {Role: model.User, Parts: []model.Part{r1}}},
			wantUnanswered: []model.ToolCall{c2},
		},
		{
			name:  "results join one user message",
			steps: []db.Step{partsStep(t, kindModelTurn, c1, c2), resultStep(t, r1), resultStep(t, r2)},
			want:  []model.Message{{Role: model.Assistant, Parts: []model.Part{c1, c2}}, {Role: model.User, Parts: []model.Part{r1, r2}}},
		},
		{
			name:         "a turn without a tool call",
			steps:        []db.Step{partsStep(t, kindModelTurn, model.Text{Text: "fine"})},
			want:         []model.Message{{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "fine"}}}},
			wantReminder: true,
		},
		{
			name:  "a reminder",
			steps: []db.Step{partsStep(t, kindModelTurn, model.Text{Text: "fine"}), partsStep(t, kindNote, note)},
			want: []model.Message{
				{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "fine"}}},
				{Role: model.User, Parts: []model.Part{note}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := encodePrompt(prompt)
			require.NoError(t, err)

			steps := append([]db.Step{{Kind: kindPrompt, Parts: raw, Status: statusOK, StartedAt: testStart}}, tt.steps...)
			for i := range steps {
				steps[i].Seq = int32(i) //nolint:gosec // A handful of steps.
			}

			h, err := newHistory(steps)
			require.NoError(t, err)

			req := h.request()
			require.Equal(t, prompt.System, req.System)
			require.Equal(t, prompt.Tools, req.Tools)
			require.Equal(t, append(prompt.Messages, tt.want...), req.Messages)
			require.Equal(t, tt.wantUnanswered, h.unanswered())
			require.Equal(t, tt.wantReminder, h.needsReminder())

			req.Messages[0].Parts[0] = model.Text{Text: "changed"}
			require.Equal(t, prompt.Messages[0], h.request().Messages[0], "request returns copies")
		})
	}
}

func TestNewHistoryRejectsBadSteps(t *testing.T) {
	t.Parallel()

	raw, err := encodePrompt(model.Request{})
	require.NoError(t, err)

	prompt := db.Step{Kind: kindPrompt, Parts: raw}
	turn := partsStep(t, kindModelTurn, model.Text{Text: "x"})

	tests := []struct {
		name  string
		steps []db.Step
	}{
		{name: "a gap", steps: []db.Step{prompt, withSeq(turn, 2)}},
		{name: "no prompt first", steps: []db.Step{withSeq(turn, 0)}},
		{name: "a second prompt", steps: []db.Step{prompt, withSeq(prompt, 1)}},
		{name: "broken parts", steps: []db.Step{prompt, {Seq: 1, Kind: kindModelTurn, Parts: json.RawMessage(`{`)}}},
		{name: "broken prompt", steps: []db.Step{{Kind: kindPrompt, Parts: json.RawMessage(`[`)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newHistory(tt.steps)
			require.Error(t, err)
		})
	}
}

// partsStep returns a step of kind with parts.
func partsStep(t *testing.T, kind string, parts ...model.Part) db.Step {
	t.Helper()

	raw, err := encodeParts(parts)
	require.NoError(t, err)

	return db.Step{Kind: kind, Parts: raw, Status: statusOK, StartedAt: testStart}
}

// resultStep returns the tool result step of a signoz search.
func resultStep(t *testing.T, res model.ToolResult) db.Step {
	t.Helper()

	s := partsStep(t, kindToolResult, res)
	s.Source, s.Tool, s.ToolCallID = new("signoz"), new("search_logs"), new(res.CallID)

	return s
}

// withSeq returns s with seq.
func withSeq(s db.Step, seq int32) db.Step {
	s.Seq = seq

	return s
}
