package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	temperature := 0.5
	declared := &DeclaredCapabilities{
		ToolCalling:     true,
		ThinkingModes:   []model.ThinkingMode{model.ThinkingAdaptive},
		EffortLevels:    []model.Effort{model.EffortLow, model.EffortHigh},
		ContextTokens:   200_000,
		MaxOutputTokens: 32_000,
	}

	tests := []struct {
		name string
		cfg  Config
		// wantErrs are the lines the error must hold; none means valid.
		wantErrs []string
	}{
		{
			name: "known model with settings it supports",
			cfg: Config{Settings: model.Settings{
				Model: "claude-opus-5-5", MaxTokens: 32_000, Effort: model.EffortXHigh,
				Thinking: model.Thinking{Mode: model.ThinkingAdaptive}, Retries: 3,
			}},
		},
		{
			name:     "model missing",
			cfg:      Config{},
			wantErrs: []string{"model: required"},
		},
		{
			name:     "unknown model without capabilities",
			cfg:      Config{Settings: model.Settings{Model: "claude-next"}},
			wantErrs: []string{`model: "claude-next" is not a model the anthropic provider knows; describe it in a capabilities block`},
		},
		{
			name: "unknown model with capabilities",
			cfg:  Config{Settings: model.Settings{Model: "claude-next", Effort: model.EffortHigh}, Declared: declared},
		},
		{
			name: "capabilities with values Anthropic does not have",
			cfg: Config{Settings: model.Settings{Model: "claude-next"}, Declared: &DeclaredCapabilities{
				ThinkingModes:   []model.ThinkingMode{"between_tools"},
				EffortLevels:    []model.Effort{model.EffortMinimal},
				ContextTokens:   -1,
				MaxOutputTokens: -1,
			}},
			wantErrs: []string{
				`capabilities.thinkingModes: "between_tools" is not an Anthropic thinking mode`,
				`capabilities.effortLevels: "minimal" is not an Anthropic effort level`,
				"capabilities.contextTokens: must not be negative",
				"capabilities.maxOutputTokens: must not be negative",
			},
		},
		{
			name:     "declared capabilities limit the settings",
			cfg:      Config{Settings: model.Settings{Model: "claude-next", Effort: model.EffortMax, MaxTokens: 64_000}, Declared: declared},
			wantErrs: []string{`effort: "max" is not supported by claude-next`, "maxTokens: 64000 is outside 0..32000"},
		},
		{
			name:     "effort none",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5-5", Effort: model.EffortNone}},
			wantErrs: []string{`effort: "none" is not supported by claude-opus-5-5`},
		},
		{
			name:     "effort minimal",
			cfg:      Config{Settings: model.Settings{Model: "claude-sonnet-5-5", Effort: model.EffortMinimal}},
			wantErrs: []string{`effort: "minimal" is not supported by claude-sonnet-5-5`},
		},
		{
			name:     "an effort level the model lacks",
			cfg:      Config{Settings: model.Settings{Model: "claude-sonnet-4-6", Effort: model.EffortXHigh}},
			wantErrs: []string{`effort: "xhigh" is not supported by claude-sonnet-4-6`},
		},
		{
			name:     "effort on a model without effort",
			cfg:      Config{Settings: model.Settings{Model: "claude-haiku-4-5", Effort: model.EffortLow}},
			wantErrs: []string{`effort: "low" is not supported by claude-haiku-4-5`},
		},
		{
			name:     "budgetTokens without mode enabled",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5-5", Thinking: model.Thinking{Mode: model.ThinkingAdaptive, BudgetTokens: 4096}}},
			wantErrs: []string{"thinking.budgetTokens: only valid with mode enabled"},
		},
		{
			name:     "a fixed budget on a model that only thinks adaptively",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5-5", Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096}}},
			wantErrs: []string{`thinking.mode: "enabled" is not supported by claude-opus-5-5`},
		},
		{
			name: "a fixed budget where it is supported",
			cfg:  Config{Settings: model.Settings{Model: "claude-haiku-4-5", MaxTokens: 16_000, Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096}}},
		},
		{
			name:     "a budget below the minimum",
			cfg:      Config{Settings: model.Settings{Model: "claude-sonnet-4-6", Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 512}}},
			wantErrs: []string{"thinking.budgetTokens: must be at least 1024 with mode enabled"},
		},
		{
			name:     "a budget that does not fit maxTokens",
			cfg:      Config{Settings: model.Settings{Model: "claude-sonnet-4-6", MaxTokens: 4096, Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096}}},
			wantErrs: []string{"thinking.budgetTokens: 4096 must be less than maxTokens (4096)"},
		},
		{
			name:     "thinking off on a model that always thinks",
			cfg:      Config{Settings: model.Settings{Model: "claude-fable-5-1", Thinking: model.Thinking{Mode: model.ThinkingOff}}},
			wantErrs: []string{"thinking.mode: claude-fable-5-1 always thinks and cannot turn thinking off"},
		},
		{
			name: "thinking left out on a model that always thinks",
			cfg:  Config{Settings: model.Settings{Model: "claude-fable-5-1", Effort: model.EffortLow}},
		},
		{
			name:     "thinking off with a high effort where off needs high or lower",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5", Effort: model.EffortMax, Thinking: model.Thinking{Mode: model.ThinkingOff}}},
			wantErrs: []string{"thinking.mode: off works on claude-opus-5 only with effort high or lower, not max"},
		},
		{
			name: "thinking off with effort high",
			cfg:  Config{Settings: model.Settings{Model: "claude-opus-5", Effort: model.EffortHigh, Thinking: model.Thinking{Mode: model.ThinkingOff}}},
		},
		{
			name:     "maxTokens above the model's maximum",
			cfg:      Config{Settings: model.Settings{Model: "claude-haiku-4-5", MaxTokens: 100_000}},
			wantErrs: []string{"maxTokens: 100000 is outside 0..64000"},
		},
		{
			name:     "temperature",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5-5", Temperature: &temperature}},
			wantErrs: []string{"temperature: not accepted; the Anthropic API version this connector uses (2026-10-21) rejects"},
		},
		{
			name:     "baseURL that is not absolute",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5-5"}, BaseURL: "proxy.internal/anthropic"},
			wantErrs: []string{`baseURL: "proxy.internal/anthropic" must be an absolute https URL`},
		},
		{
			name: "https baseURL of a proxy on another host",
			cfg:  Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef, BaseURL: "https://llm-proxy.internal"},
		},
		{
			name: "http baseURL on localhost",
			cfg:  Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef, BaseURL: "http://localhost:8080"},
		},
		{
			name: "http baseURL on 127.0.0.0/8",
			cfg:  Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef, BaseURL: "http://127.0.0.2:8080"},
		},
		{
			name: "http baseURL on ::1",
			cfg:  Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef, BaseURL: "http://[::1]:8080"},
		},
		{
			name:     "http baseURL on another host with a key",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef, BaseURL: "http://llm-proxy.internal"},
			wantErrs: []string{`baseURL: "http://llm-proxy.internal" must use https, since every call carries the API key`},
		},
		{
			name:     "http baseURL on another host without apiKeySecret, which New requires",
			cfg:      Config{Settings: model.Settings{Model: "claude-opus-5-5"}, BaseURL: "http://llm-proxy.internal"},
			wantErrs: []string{`baseURL: "http://llm-proxy.internal" must use https, since every call carries the API key`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, want := range tt.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestConfigCapabilities(t *testing.T) {
	t.Parallel()

	declared := &DeclaredCapabilities{ToolCalling: true, ToolChoice: true, ContextTokens: 1000, MaxOutputTokens: 100}

	tests := []struct {
		name string
		cfg  Config
		want model.Capabilities
	}{
		{
			name: "known model",
			cfg:  Config{Settings: model.Settings{Model: "claude-sonnet-5-5"}},
			want: model.Capabilities{
				ToolCalling:      true,
				ThinkingModes:    []model.ThinkingMode{model.ThinkingAdaptive},
				EffortLevels:     []model.Effort{model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortXHigh, model.EffortMax},
				StructuredOutput: true,
				PromptCaching:    true,
				ContextTokens:    1_000_000,
				MaxOutputTokens:  128_000,
			},
		},
		{
			name: "a declared block wins over the table",
			cfg:  Config{Settings: model.Settings{Model: "claude-sonnet-5-5"}, Declared: declared},
			want: model.Capabilities{ToolCalling: true, ToolChoice: true, ContextTokens: 1000, MaxOutputTokens: 100},
		},
		{
			name: "unknown model",
			cfg:  Config{Settings: model.Settings{Model: "claude-next"}},
			want: model.Capabilities{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.cfg.Capabilities())
		})
	}
}

func TestKnownModels(t *testing.T) {
	t.Parallel()

	for id, m := range knownModels {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			require.True(t, m.caps.ToolCalling, "every Claude model can call tools")
			require.NoError(t, model.CheckRole(model.RoleInvestigate, m.caps))
			require.Positive(t, m.caps.ContextTokens)
			require.Positive(t, m.caps.MaxOutputTokens)
			require.NotContains(t, m.caps.EffortLevels, model.EffortNone)
			require.NotContains(t, m.caps.EffortLevels, model.EffortMinimal)

			cfg := Config{Settings: model.Settings{Model: id}}
			require.NoError(t, cfg.Validate(), "a config with only the model is valid")
		})
	}
}
