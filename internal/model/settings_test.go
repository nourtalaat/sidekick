package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettingsValidate(t *testing.T) {
	t.Parallel()

	caps := Capabilities{
		ThinkingModes:   []ThinkingMode{ThinkingAdaptive, ThinkingEnabled},
		EffortLevels:    []Effort{EffortLow, EffortHigh},
		MaxOutputTokens: 64_000,
	}

	tests := []struct {
		name     string
		settings Settings
		caps     Capabilities
		wantErrs []string
	}{
		{
			name:     "only the model set",
			settings: Settings{Model: "claude-x"},
			caps:     caps,
		},
		{
			name: "every setting within what the model supports",
			settings: Settings{
				Model:     "claude-x",
				MaxTokens: 64_000,
				Retries:   3,
				Thinking:  Thinking{Mode: ThinkingEnabled, BudgetTokens: 8_000},
				Effort:    EffortHigh,
			},
			caps: caps,
		},
		{
			name:     "no output limit known allows any max tokens",
			settings: Settings{Model: "local", MaxTokens: 1_000_000},
			caps:     Capabilities{},
		},
		{
			name:     "model missing",
			settings: Settings{},
			caps:     caps,
			wantErrs: []string{"model: required"},
		},
		{
			name:     "negative max tokens",
			settings: Settings{Model: "claude-x", MaxTokens: -1},
			caps:     caps,
			wantErrs: []string{"maxTokens: -1 is outside 0..64000"},
		},
		{
			name:     "max tokens above the model limit",
			settings: Settings{Model: "claude-x", MaxTokens: 64_001},
			caps:     caps,
			wantErrs: []string{"maxTokens: 64001 is outside 0..64000"},
		},
		{
			name:     "thinking mode not supported",
			settings: Settings{Model: "small", Thinking: Thinking{Mode: ThinkingAdaptive}},
			caps:     Capabilities{},
			wantErrs: []string{`thinking.mode: "adaptive" is not supported by small`},
		},
		{
			name:     "budget tokens without enabled mode",
			settings: Settings{Model: "claude-x", Thinking: Thinking{Mode: ThinkingAdaptive, BudgetTokens: 1_000}},
			caps:     caps,
			wantErrs: []string{"thinking.budgetTokens: only valid with mode enabled"},
		},
		{
			name:     "effort not supported",
			settings: Settings{Model: "claude-x", Effort: EffortMax},
			caps:     caps,
			wantErrs: []string{`effort: "max" is not supported by claude-x`},
		},
		{
			name:     "negative retries",
			settings: Settings{Model: "claude-x", Retries: -1},
			caps:     caps,
			wantErrs: []string{"retries: must not be negative"},
		},
		{
			name:     "every problem is reported at once",
			settings: Settings{MaxTokens: -1, Retries: -1, Effort: EffortMax},
			caps:     caps,
			wantErrs: []string{"model: required", "maxTokens:", "effort:", "retries:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.settings.Validate(tt.caps)
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}
