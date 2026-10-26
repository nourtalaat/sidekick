package echo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestGenerate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		messages []model.Message
		want     string
		wantKind model.ErrorKind
	}{
		{
			name:     "echoes the last user text",
			messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "first"}}}, {Role: model.User, Parts: []model.Part{model.Text{Text: "second"}}}},
			want:     "> second",
		},
		{
			name: "skips messages without text",
			messages: []model.Message{
				{Role: model.User, Parts: []model.Part{model.Text{Text: "question"}}},
				{Role: model.Assistant, Parts: []model.Part{model.ToolCall{ID: "1", Name: "t"}}},
				{Role: model.User, Parts: []model.Part{model.ToolResult{CallID: "1", Content: "result"}}},
			},
			want: "> question",
		},
		{
			name:     "no user text is an invalid request",
			messages: []model.Message{{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "hello"}}}},
			wantKind: model.ErrInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := New(t.Context(), "echo", Config{Settings: model.Settings{Model: "echo-1"}, Prefix: "> "}, registry.Deps{})
			require.NoError(t, err)

			resp, err := c.Generate(t.Context(), model.Request{Messages: tt.messages})
			if tt.wantKind != "" {
				var merr *model.Error
				require.ErrorAs(t, err, &merr)
				require.Equal(t, tt.wantKind, merr.Kind)

				return
			}

			require.NoError(t, err)
			require.Equal(t, []model.Part{model.Text{Text: tt.want}}, resp.Message.Parts)
			require.Equal(t, model.StopEndTurn, resp.StopReason)
		})
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "valid", cfg: Config{Settings: model.Settings{Model: "echo-1"}, Prefix: ">"}},
		{name: "model missing", cfg: Config{}, wantErr: "model: required"},
		{name: "effort the model lacks", cfg: Config{Settings: model.Settings{Model: "echo-1", Effort: model.EffortHigh}}, wantErr: `effort: "high"`},
		{name: "prefix with spaces", cfg: Config{Settings: model.Settings{Model: "echo-1"}, Prefix: " >"}, wantErr: "prefix: must not"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Models.Types(), Provider)
}
