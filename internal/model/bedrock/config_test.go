package bedrock

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	temperature := 0.5

	tests := []struct {
		name string
		cfg  Config
		// wantErr lists text each of the errors must contain; empty means
		// the config is valid.
		wantErr []string
	}{
		{
			name: "a known model by its bare ID",
			cfg:  Config{Settings: model.Settings{Model: "anthropic.claude-opus-5-5", Effort: model.EffortHigh}, Region: "eu-west-2"},
		},
		{
			name: "a known model by a geo inference profile",
			cfg:  Config{Settings: model.Settings{Model: "us.anthropic.claude-sonnet-5", Thinking: model.Thinking{Mode: model.ThinkingOff}}, Region: "us-east-1"},
		},
		{
			name: "a known model by the global inference profile",
			cfg:  Config{Settings: model.Settings{Model: "global.anthropic.claude-haiku-5-5", Thinking: model.Thinking{Mode: model.ThinkingAdaptive}}, Region: "ap-southeast-2"},
		},
		{
			name: "a GovCloud Region",
			cfg:  Config{Settings: model.Settings{Model: "us-gov.anthropic.claude-sonnet-4-6"}, Region: "us-gov-west-1"},
		},
		{
			name: "a fixed thinking budget where the model takes one",
			cfg: Config{
				Settings: model.Settings{Model: "anthropic.claude-haiku-4-5-20251001-v1:0", MaxTokens: 8000, Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 2048}},
				Region:   "us-east-1",
			},
		},
		{
			name: "temperature is passed to the model",
			cfg:  Config{Settings: model.Settings{Model: "anthropic.claude-opus-4-8", Temperature: &temperature}, Region: "us-east-1"},
		},
		{
			name: "an endpoint",
			cfg:  Config{Settings: model.Settings{Model: "anthropic.claude-opus-4-8"}, Region: "us-east-1", Endpoint: "https://vpce-0abc.bedrock-runtime.us-east-1.vpce.amazonaws.com"},
		},
		{
			name: "another model family with a capabilities block",
			cfg: Config{
				Settings: model.Settings{Model: "amazon.nova-pro-v1:0", MaxTokens: 5000},
				Region:   "us-east-1",
				Declared: &DeclaredCapabilities{ToolCalling: true, ToolChoice: true, ContextTokens: 300_000, MaxOutputTokens: 10_000},
			},
		},
		{
			name: "an Anthropic model by inference profile ARN with thinking declared",
			cfg: Config{
				Settings: model.Settings{Model: "arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-opus-9", Thinking: model.Thinking{Mode: model.ThinkingAdaptive}},
				Region:   "us-east-1",
				Declared: &DeclaredCapabilities{ToolCalling: true, ThinkingModes: []model.ThinkingMode{model.ThinkingAdaptive}},
			},
		},
		{
			name:    "region is required",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-opus-4-8"}},
			wantErr: []string{`region: "" is not an AWS Region code`},
		},
		{
			name:    "region must be a Region code",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-opus-4-8"}, Region: "Virginia"},
			wantErr: []string{`region: "Virginia"`},
		},
		{
			name:    "endpoint must be an absolute URL",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-opus-4-8"}, Region: "us-east-1", Endpoint: "bedrock.internal"},
			wantErr: []string{"endpoint: \"bedrock.internal\" must be an absolute http or https URL"},
		},
		{
			name:    "model is required",
			cfg:     Config{Region: "us-east-1"},
			wantErr: []string{"model: required"},
		},
		{
			name:    "an unknown model without a capabilities block",
			cfg:     Config{Settings: model.Settings{Model: "meta.llama4-maverick-17b-instruct-v1:0"}, Region: "us-east-1"},
			wantErr: []string{"is not a model the bedrock provider knows; describe it in a capabilities block"},
		},
		{
			name:    "an unknown inference profile prefix",
			cfg:     Config{Settings: model.Settings{Model: "mars.anthropic.claude-opus-5-5"}, Region: "us-east-1"},
			wantErr: []string{"is not a model the bedrock provider knows"},
		},
		{
			name:    "effort none is an OpenAI level",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-opus-5-5", Effort: model.EffortNone}, Region: "us-east-1"},
			wantErr: []string{`effort: "none" is not supported by anthropic.claude-opus-5-5`},
		},
		{
			name:    "xhigh on a 4.6 model",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-sonnet-4-6", Effort: model.EffortXHigh}, Region: "us-east-1"},
			wantErr: []string{`effort: "xhigh" is not supported`},
		},
		{
			name:    "a fixed budget on a model with adaptive thinking only",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-fable-5-1", Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096}}, Region: "us-east-1"},
			wantErr: []string{`thinking.mode: "enabled" is not supported`},
		},
		{
			name:    "thinking off on a model that always thinks",
			cfg:     Config{Settings: model.Settings{Model: "global.anthropic.claude-sonnet-5-5", Thinking: model.Thinking{Mode: model.ThinkingOff}}, Region: "us-east-1"},
			wantErr: []string{"always thinks and cannot turn thinking off"},
		},
		{
			name:    "thinking off with effort xhigh where effort is capped at high",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-haiku-5-5", Thinking: model.Thinking{Mode: model.ThinkingOff}, Effort: model.EffortXHigh}, Region: "us-east-1"},
			wantErr: []string{"off works on anthropic.claude-haiku-5-5 only with effort high or lower"},
		},
		{
			name: "a budget below the minimum",
			cfg: Config{
				Settings: model.Settings{Model: "anthropic.claude-sonnet-4-6", Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 512}},
				Region:   "us-east-1",
			},
			wantErr: []string{"thinking.budgetTokens: must be at least 1024"},
		},
		{
			name: "a budget at the output limit",
			cfg: Config{
				Settings: model.Settings{Model: "anthropic.claude-sonnet-4-6", MaxTokens: 4096, Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096}},
				Region:   "us-east-1",
			},
			wantErr: []string{"thinking.budgetTokens: 4096 must be less than maxTokens (4096)"},
		},
		{
			name:    "maxTokens above the model's maximum",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-haiku-4-5-20251001-v1:0", MaxTokens: 100_000}, Region: "us-east-1"},
			wantErr: []string{"maxTokens: 100000 is outside 0..64000"},
		},
		{
			name: "thinking and effort declared for another model family",
			cfg: Config{
				Settings: model.Settings{Model: "amazon.nova-pro-v1:0"},
				Region:   "us-east-1",
				Declared: &DeclaredCapabilities{
					ToolCalling:   true,
					ThinkingModes: []model.ThinkingMode{model.ThinkingAdaptive},
					EffortLevels:  []model.Effort{model.EffortLow},
				},
			},
			wantErr: []string{
				"capabilities.thinkingModes: only Anthropic models (anthropic.* IDs) take thinking settings on Bedrock",
				"capabilities.effortLevels: only Anthropic models (anthropic.* IDs) take effort on Bedrock",
			},
		},
		{
			name: "a capabilities block with values Claude does not have",
			cfg: Config{
				Settings: model.Settings{Model: "anthropic.claude-next"},
				Region:   "us-east-1",
				Declared: &DeclaredCapabilities{
					ToolCalling:     true,
					ThinkingModes:   []model.ThinkingMode{"deep"},
					EffortLevels:    []model.Effort{model.EffortMinimal},
					ContextTokens:   -1,
					MaxOutputTokens: -1,
				},
			},
			wantErr: []string{
				`capabilities.thinkingModes: "deep" is not a Claude thinking mode`,
				`capabilities.effortLevels: "minimal" is not a Claude effort level`,
				"capabilities.contextTokens: must not be negative",
				"capabilities.maxOutputTokens: must not be negative",
			},
		},
		{
			name: "several problems are reported together",
			cfg: Config{
				Settings: model.Settings{Model: "anthropic.claude-opus-5-5", Effort: model.EffortMinimal, Retries: -1},
				Endpoint: "ftp://x",
			},
			wantErr: []string{"region:", "endpoint:", `effort: "minimal"`, "retries: must not be negative"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()

	declared := &DeclaredCapabilities{ToolCalling: true, ContextTokens: 300_000, MaxOutputTokens: 10_000, PromptCaching: true}

	tests := []struct {
		name string
		cfg  Config
		want model.Capabilities
	}{
		{
			name: "a bare ID",
			cfg:  Config{Settings: model.Settings{Model: "anthropic.claude-opus-5-5"}},
			want: knownModels["anthropic.claude-opus-5-5"].caps,
		},
		{
			name: "each inference profile prefix finds the bare ID",
			cfg:  Config{Settings: model.Settings{Model: "jp.anthropic.claude-haiku-5-5"}},
			want: knownModels["anthropic.claude-haiku-5-5"].caps,
		},
		{
			name: "a declared block wins over the table",
			cfg:  Config{Settings: model.Settings{Model: "anthropic.claude-opus-5-5"}, Declared: declared},
			want: model.Capabilities{ToolCalling: true, ContextTokens: 300_000, MaxOutputTokens: 10_000, PromptCaching: true},
		},
		{
			name: "an unknown model has no capabilities",
			cfg:  Config{Settings: model.Settings{Model: "cohere.command-r-v1:0"}},
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

// TestKnownModelsCanInvestigate checks that every known model can call
// tools, so each can take the investigate role, and that every profile
// prefix resolves.
func TestKnownModelsCanInvestigate(t *testing.T) {
	t.Parallel()

	for id, m := range knownModels {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, model.CheckRole(model.RoleInvestigate, m.caps))
			require.True(t, isAnthropic(id))

			for _, prefix := range profilePrefixes {
				got, ok := lookupModel(prefix + id)
				require.True(t, ok, prefix+id)
				require.Equal(t, m, got)
			}
		})
	}
}

func TestMaxTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured int
		requested  int
		caps       model.Capabilities
		want       int
	}{
		{name: "the default for a model with a known maximum", caps: model.Capabilities{MaxOutputTokens: output128K}, want: defaultMaxTokens},
		{name: "the default is capped at the maximum", caps: model.Capabilities{MaxOutputTokens: 10_000}, want: 10_000},
		{name: "left to the model when its maximum is unknown", want: 0},
		{name: "the config's limit", configured: 4000, want: 4000},
		{name: "the request's limit wins", configured: 4000, requested: 2000, want: 2000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{Settings: model.Settings{MaxTokens: tt.configured}}
			require.Equal(t, tt.want, cfg.maxTokens(tt.requested, tt.caps))
		})
	}
}
