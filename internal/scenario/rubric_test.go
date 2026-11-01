package scenario

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

func TestDraftRubric(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		answer  string
		callErr error
		want    []bench.RubricItem
		wantErr string
		wantIs  error
	}{
		{
			name:   "items with weights",
			answer: `{"items":[{"text":" Names pool exhaustion ","weight":2,"required":true},{"text":"Proposes a rollback","weight":1,"required":false}]}`,
			want: []bench.RubricItem{
				{Text: "Names pool exhaustion", Weight: 2, Required: true},
				{Text: "Proposes a rollback", Weight: 1},
			},
		},
		{
			name:   "weights are held to 1 to 3",
			answer: `{"items":[{"text":"a","weight":0,"required":false},{"text":"b","weight":9,"required":false}]}`,
			want:   []bench.RubricItem{{Text: "a", Weight: 1}, {Text: "b", Weight: 3}},
		},
		{name: "no items", answer: `{"items":[]}`, wantErr: "0 items"},
		{name: "too many items", answer: `{"items":[{"text":"a","weight":1},{"text":"b","weight":1},{"text":"c","weight":1},{"text":"d","weight":1},{"text":"e","weight":1},{"text":"f","weight":1}]}`, wantErr: "6 items"},
		{name: "an item without text", answer: `{"items":[{"text":"  ","weight":1}]}`, wantErr: "item 1 has no text"},
		{name: "not JSON", answer: `nope`, wantErr: "not usable"},
		{name: "the call fails", callErr: summarize.ErrOverCap, wantIs: summarize.ErrOverCap},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			caller := NewMockToolCaller(gomock.NewController(t))
			caller.EXPECT().Enabled().Return(true)
			caller.EXPECT().CallTool(gomock.Any(), gomock.Nil(), gomock.Any()).DoAndReturn(
				func(_ any, _ any, req summarize.Request) (json.RawMessage, error) {
					require.Equal(t, "default", req.Route)
					require.Equal(t, rubricSystem, req.System)
					require.Equal(t, rubricToolName, req.Tool.Name)
					require.JSONEq(t, rubricSchema, string(req.Tool.InputSchema))
					require.Len(t, req.Messages, 1)

					text, ok := req.Messages[0].Parts[0].(model.Text)
					require.True(t, ok)
					require.Contains(t, text.Text, "Verdict: true_positive")
					require.Contains(t, text.Text, "<untrusted_data source=\"confirmed-cause\">\nthe cause\n</untrusted_data>")

					return json.RawMessage(tt.answer), tt.callErr
				})

			drafter := NewRubricDrafter(caller)
			require.True(t, drafter.Enabled())

			got, err := drafter.Draft(t.Context(), nil, "default", "true_positive", "the cause")
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)

				return
			}

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseRubricErrorIsBadRubric(t *testing.T) {
	t.Parallel()

	_, err := parseRubric(json.RawMessage(`{"items":[]}`))
	require.ErrorIs(t, err, errBadRubric)
	require.NotErrorIs(t, err, summarize.ErrOverCap)
}
