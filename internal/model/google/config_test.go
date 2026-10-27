package google

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	withSettings := func(cfg Config, s model.Settings) Config {
		s.Model = cfg.Model
		cfg.Settings = s

		return cfg
	}

	tests := []struct {
		name string
		cfg  Config
		// wantErrs are parts of the error message; none means valid.
		wantErrs []string
	}{
		{name: "gemini", cfg: geminiConfig(flash)},
		{name: "gemini named as the backend", cfg: changed(geminiConfig(flash), func(c *Config) { c.Backend = BackendGemini })},
		{name: "vertex", cfg: vertexConfig(flash)},
		{name: "vertex in a region", cfg: changed(vertexConfig(flash), func(c *Config) { c.Location = "europe-west4" })},
		{name: "https base URL", cfg: changed(geminiConfig(flash), func(c *Config) { c.BaseURL = "https://llm-proxy.internal" })},
		{name: "http base URL on loopback", cfg: changed(geminiConfig(flash), func(c *Config) { c.BaseURL = "http://127.0.0.1:8080" })},
		{
			name:     "http base URL to another host",
			cfg:      changed(geminiConfig(flash), func(c *Config) { c.BaseURL = "http://llm-proxy.internal" }),
			wantErrs: []string{"baseURL: \"http://llm-proxy.internal\" must use https"},
		},
		{
			name:     "base URL that is not absolute",
			cfg:      changed(geminiConfig(flash), func(c *Config) { c.BaseURL = "llm-proxy.internal" }),
			wantErrs: []string{"must be an absolute https URL"},
		},
		{name: "gemini without a key", cfg: Config{Settings: model.Settings{Model: flash}}, wantErrs: []string{"apiKeySecret: required with backend gemini"}},
		{
			name:     "gemini with vertex settings",
			cfg:      changed(geminiConfig(flash), func(c *Config) { c.Project, c.Location = testProject, "global" }),
			wantErrs: []string{"project: only applies with backend vertex", "location: only applies with backend vertex"},
		},
		{
			name:     "vertex with a key",
			cfg:      changed(vertexConfig(flash), func(c *Config) { c.APIKeySecret = keyRef }),
			wantErrs: []string{"apiKeySecret: not accepted with backend vertex"},
		},
		{
			name:     "vertex without a project",
			cfg:      Config{Settings: model.Settings{Model: flash}, Backend: BackendVertex},
			wantErrs: []string{`project: "" is not a Google Cloud project ID`},
		},
		{
			name:     "vertex with a location that is not one",
			cfg:      changed(vertexConfig(flash), func(c *Config) { c.Location = "../evil" }),
			wantErrs: []string{`location: "../evil" is not a location`},
		},
		{
			name:     "unknown backend",
			cfg:      Config{Settings: model.Settings{Model: flash}, Backend: "aistudio"},
			wantErrs: []string{`backend: "aistudio" is not a backend; use gemini or vertex`},
		},
		{name: "no model", cfg: geminiConfig(""), wantErrs: []string{"model: required"}},
		{name: "unknown model", cfg: geminiConfig("gemini-9"), wantErrs: []string{`model: "gemini-9" is not a model the google provider knows`}},
		{name: "effort on a level model", cfg: withSettings(geminiConfig(flash), model.Settings{Effort: model.EffortHigh})},
		{name: "minimal effort where the model has it", cfg: withSettings(geminiConfig("gemini-3.6-flash"), model.Settings{Effort: model.EffortMinimal})},
		{
			name:     "minimal effort where the model lacks it",
			cfg:      withSettings(geminiConfig(flash), model.Settings{Effort: model.EffortMinimal}),
			wantErrs: []string{`effort: "minimal" is not supported by gemini-3.8-flash`},
		},
		{
			name:     "an OpenAI effort level",
			cfg:      withSettings(geminiConfig(flash), model.Settings{Effort: model.EffortXHigh}),
			wantErrs: []string{`effort: "xhigh" is not supported`},
		},
		{
			name:     "effort on a budget model",
			cfg:      withSettings(geminiConfig("gemini-2.5-pro"), model.Settings{Effort: model.EffortLow}),
			wantErrs: []string{`effort: "low" is not supported by gemini-2.5-pro`},
		},
		{
			name: "adaptive thinking on a level model",
			cfg:  withSettings(geminiConfig(flash), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingAdaptive}}),
		},
		{
			name:     "a fixed budget on a level model",
			cfg:      withSettings(geminiConfig(flash), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 2048}}),
			wantErrs: []string{`thinking.mode: "enabled" is not supported by gemini-3.8-flash`},
		},
		{
			name:     "thinking off on a level model",
			cfg:      withSettings(geminiConfig(flash), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingOff}}),
			wantErrs: []string{"thinking.mode: gemini-3.8-flash always thinks"},
		},
		{
			name: "thinking off on 2.5 Flash",
			cfg:  withSettings(geminiConfig("gemini-2.5-flash"), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingOff}}),
		},
		{
			name:     "thinking off on 2.5 Pro",
			cfg:      withSettings(geminiConfig("gemini-2.5-pro"), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingOff}}),
			wantErrs: []string{"thinking.mode: gemini-2.5-pro always thinks"},
		},
		{
			name: "a fixed budget in range",
			cfg:  withSettings(geminiConfig("gemini-2.5-pro"), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 32_768}}),
		},
		{
			name:     "a budget below the minimum",
			cfg:      withSettings(geminiConfig("gemini-2.5-flash-lite"), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 256}}),
			wantErrs: []string{"thinking.budgetTokens: must be at least 512 on gemini-2.5-flash-lite"},
		},
		{
			name:     "a budget above the maximum",
			cfg:      withSettings(geminiConfig("gemini-2.5-flash"), model.Settings{Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 30_000}}),
			wantErrs: []string{"thinking.budgetTokens: must be at most 24576 on gemini-2.5-flash"},
		},
		{
			name:     "an output limit above the model's",
			cfg:      withSettings(geminiConfig(flash), model.Settings{MaxTokens: 100_000}),
			wantErrs: []string{"maxTokens: 100000 is outside 0..65536"},
		},
		{
			name: "declared model with effort",
			cfg: func() Config {
				c := geminiConfig("tuned-flash")
				c.Effort = model.EffortLow
				c.Declared = &DeclaredCapabilities{ToolCalling: true, EffortLevels: []model.Effort{model.EffortLow, model.EffortHigh}}

				return c
			}(),
		},
		{
			name: "declared model with a budget",
			cfg: func() Config {
				c := geminiConfig("tuned-flash")
				c.Thinking = model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 1000}
				c.Declared = &DeclaredCapabilities{ToolCalling: true, ThinkingModes: []model.ThinkingMode{model.ThinkingEnabled}}

				return c
			}(),
		},
		{
			name: "declared model with effort and a budget",
			cfg: func() Config {
				c := geminiConfig("tuned-flash")
				c.Declared = &DeclaredCapabilities{
					ThinkingModes: []model.ThinkingMode{model.ThinkingEnabled},
					EffortLevels:  []model.Effort{model.EffortLow},
				}

				return c
			}(),
			wantErrs: []string{"capabilities: a Gemini model takes either effort levels"},
		},
		{
			name: "declared values Gemini does not have",
			cfg: func() Config {
				c := geminiConfig("tuned-flash")
				c.Declared = &DeclaredCapabilities{
					ThinkingModes:   []model.ThinkingMode{model.ThinkingOff},
					EffortLevels:    []model.Effort{model.EffortMax},
					ContextTokens:   -1,
					MaxOutputTokens: -1,
				}

				return c
			}(),
			wantErrs: []string{
				`capabilities.thinkingModes: "off" is not a Gemini thinking mode`,
				`capabilities.effortLevels: "max" is not a Gemini thinking level`,
				"capabilities.contextTokens: must not be negative",
				"capabilities.maxOutputTokens: must not be negative",
			},
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

func TestConfigBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "gemini", cfg: geminiConfig(flash), want: "https://generativelanguage.googleapis.com/"},
		{name: "vertex global", cfg: vertexConfig(flash), want: "https://aiplatform.googleapis.com/"},
		{name: "vertex region", cfg: Config{Backend: BackendVertex, Location: "europe-west4"}, want: "https://europe-west4-aiplatform.googleapis.com/"},
		{name: "vertex multi-region", cfg: Config{Backend: BackendVertex, Location: "eu"}, want: "https://aiplatform.eu.rep.googleapis.com/"},
		{name: "set", cfg: Config{Backend: BackendVertex, BaseURL: "https://psc.internal/"}, want: "https://psc.internal/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.cfg.baseURL())
		})
	}
}

// TestKnownModels checks the table: every known model can be the
// investigate model, and its effort and thinking settings are consistent.
func TestKnownModels(t *testing.T) {
	t.Parallel()

	for id, m := range knownModels {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, model.CheckRole(model.RoleInvestigate, m.caps))

			if m.budget != nil {
				require.Empty(t, m.caps.EffortLevels, "a budget model takes no effort")
				require.True(t, m.caps.SupportsThinking(model.ThinkingEnabled))

				return
			}

			require.NotEmpty(t, m.caps.EffortLevels)
			require.False(t, m.canTurnOff, "level models always think")
		})
	}
}
