package google

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestPartParams(t *testing.T) {
	t.Parallel()

	sig := []byte("signature-bytes")
	encoded := base64.StdEncoding.EncodeToString(sig)

	tests := []struct {
		name  string
		parts []model.Part
		want  []*genai.Part
	}{
		{
			name:  "signature goes on the next part",
			parts: []model.Part{model.Reasoning{Provider: Provider, ID: reasoningNext, Signature: encoded}, model.Text{Text: "answer"}},
			want:  []*genai.Part{{Text: "answer", ThoughtSignature: sig}},
		},
		{
			name: "only the next part gets the signature",
			parts: []model.Part{
				model.Reasoning{Provider: Provider, ID: reasoningNext, Signature: encoded},
				model.ToolCall{ID: "c1", Name: "a"},
				model.ToolCall{ID: "c2", Name: "b"},
			},
			want: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "a", Args: map[string]any{}}, ThoughtSignature: sig},
				{FunctionCall: &genai.FunctionCall{ID: "c2", Name: "b", Args: map[string]any{}}},
			},
		},
		{
			name:  "thought part",
			parts: []model.Part{model.Reasoning{Provider: Provider, ID: reasoningThought, Text: "thinking", Signature: encoded}, model.Text{Text: "answer"}},
			want:  []*genai.Part{{Text: "thinking", Thought: true, ThoughtSignature: sig}, {Text: "answer"}},
		},
		{
			name:  "part with only a signature",
			parts: []model.Part{model.Text{Text: "answer"}, model.Reasoning{Provider: Provider, ID: reasoningPart, Signature: encoded}},
			want:  []*genai.Part{{Text: "answer"}, {ThoughtSignature: sig}},
		},
		{
			name: "reasoning from another provider is dropped",
			parts: []model.Part{
				model.Reasoning{Provider: "anthropic", Text: "thinking", Signature: "c2ln"},
				model.Text{Text: "answer"},
			},
			want: []*genai.Part{{Text: "answer"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := partParams(model.Message{Role: model.Assistant, Parts: tt.parts}, nil)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestPartParamsRejectsABadSignature checks that a google signature that
// is not base64 fails the request instead of being sent half read.
func TestPartParamsRejectsABadSignature(t *testing.T) {
	t.Parallel()

	_, err := partParams(model.Message{Role: model.Assistant, Parts: []model.Part{
		model.Reasoning{Provider: Provider, ID: reasoningNext, Signature: "not base64!"},
		model.Text{Text: "answer"},
	}}, nil)
	require.Error(t, err)
}
