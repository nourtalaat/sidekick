package openai

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// ollamaURL is the address of a local Ollama server's OpenAI API.
const ollamaURL = "http://localhost:11434/v1"

// keyRef is a secret reference for configs that need an API key.
var keyRef = registry.SecretRef{Name: "sidekick-openai", Key: "apiKey"}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		// wantErrs are the lines the error must hold; none means valid.
		wantErrs []string
	}{
		{
			name: "known model with settings it supports",
			cfg:  Config{Settings: model.Settings{Model: "gpt-6-astra", MaxTokens: 32_000, Effort: model.EffortMax, Retries: 3}, APIKeySecret: keyRef},
		},
		{
			name: "effort none on a model that has it",
			cfg:  Config{Settings: model.Settings{Model: "gpt-5.4", Effort: model.EffortNone}, APIKeySecret: keyRef},
		},
		{
			name:     "effort max on a model that predates it",
			cfg:      Config{Settings: model.Settings{Model: "gpt-5.5", Effort: model.EffortMax}, APIKeySecret: keyRef},
			wantErrs: []string{`effort: "max" is not supported by gpt-5.5`},
		},
		{
			name:     "effort none on a model that always reasons",
			cfg:      Config{Settings: model.Settings{Model: "gpt-6.1-sol", Effort: model.EffortNone}, APIKeySecret: keyRef},
			wantErrs: []string{`effort: "none" is not supported by gpt-6.1-sol`},
		},
		{
			name:     "effort minimal, which no current model has",
			cfg:      Config{Settings: model.Settings{Model: "gpt-6-luna", Effort: model.EffortMinimal}, APIKeySecret: keyRef},
			wantErrs: []string{`effort: "minimal" is not supported by gpt-6-luna`},
		},
		{
			name:     "adaptive thinking",
			cfg:      Config{Settings: model.Settings{Model: "gpt-6-sol", Thinking: model.Thinking{Mode: model.ThinkingAdaptive}}, APIKeySecret: keyRef},
			wantErrs: []string{"thinking: not accepted by the openai provider"},
		},
		{
			name: "thinking with a budget",
			cfg: Config{
				Settings:     model.Settings{Model: "gpt-6-sol", Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 2048}},
				APIKeySecret: keyRef,
			},
			wantErrs: []string{"thinking: not accepted by the openai provider"},
		},
		{
			name:     "maxTokens above the model's output",
			cfg:      Config{Settings: model.Settings{Model: "gpt-6-sol", MaxTokens: 200_000}, APIKeySecret: keyRef},
			wantErrs: []string{"maxTokens: 200000 is outside 0..128000"},
		},
		{
			name:     "api.openai.com without a key",
			cfg:      Config{Settings: model.Settings{Model: "gpt-6-sol"}},
			wantErrs: []string{"apiKeySecret: required without baseURL"},
		},
		{
			name:     "server without baseURL",
			cfg:      Config{Settings: model.Settings{Model: "gpt-6-sol"}, APIKeySecret: keyRef, Server: ServerVLLM},
			wantErrs: []string{"server: only applies with baseURL"},
		},
		{
			name: "unknown api and server",
			cfg:  Config{Settings: model.Settings{Model: "qwen3.8:27b"}, BaseURL: ollamaURL, API: "completions", Server: "lmstudio"},
			wantErrs: []string{
				`api: "completions" is not an API; use responses or chat_completions`,
				`server: "lmstudio" is not a server kind; use ollama, vllm or llamacpp`,
			},
		},
		{
			name:     "relative baseURL",
			cfg:      Config{Settings: model.Settings{Model: "qwen3.8:27b"}, BaseURL: "localhost:11434/v1"},
			wantErrs: []string{`baseURL: "localhost:11434/v1" must be an absolute http or https URL`},
		},
		{
			name: "https baseURL on another host with a key",
			cfg:  Config{Settings: model.Settings{Model: "gpt-6-sol"}, APIKeySecret: keyRef, BaseURL: "https://llm-gateway.internal/v1"},
		},
		{
			name: "http baseURL on localhost with a key",
			cfg:  Config{Settings: model.Settings{Model: "qwen3.8:27b"}, APIKeySecret: keyRef, BaseURL: ollamaURL},
		},
		{
			name: "http baseURL on 127.0.0.0/8 with a key",
			cfg:  Config{Settings: model.Settings{Model: "qwen3.8:27b"}, APIKeySecret: keyRef, BaseURL: "http://127.0.0.2:8000/v1"},
		},
		{
			name: "http baseURL on ::1 with a key",
			cfg:  Config{Settings: model.Settings{Model: "qwen3.8:27b"}, APIKeySecret: keyRef, BaseURL: "http://[::1]:8000/v1"},
		},
		{
			name:     "http baseURL on another host with a key",
			cfg:      Config{Settings: model.Settings{Model: "qwen3.8:27b"}, APIKeySecret: keyRef, BaseURL: "http://vllm.ml.svc:8000/v1"},
			wantErrs: []string{`baseURL: "http://vllm.ml.svc:8000/v1" must use https when apiKeySecret is set`},
		},
		{
			name:     "http baseURL on a host that only starts with localhost, with a key",
			cfg:      Config{Settings: model.Settings{Model: "qwen3.8:27b"}, APIKeySecret: keyRef, BaseURL: "http://localhost.example.com/v1"},
			wantErrs: []string{`baseURL: "http://localhost.example.com/v1" must use https when apiKeySecret is set`},
		},
		{
			name: "http baseURL on another host without a key",
			cfg:  Config{Settings: model.Settings{Model: "qwen3.8:27b"}, BaseURL: "http://vllm.ml.svc:8000/v1"},
		},
		{
			name: "preset on Ollama with no key",
			cfg:  Config{Settings: model.Settings{Model: "qwen3.8:27b", Effort: model.EffortLow}, BaseURL: ollamaURL},
		},
		{
			name:     "effort the preset does not take",
			cfg:      Config{Settings: model.Settings{Model: "gemma4:31b", Effort: model.EffortLow}, BaseURL: ollamaURL},
			wantErrs: []string{`effort: "low" is not supported by gemma4:31b`},
		},
		{
			name:     "preset model without baseURL",
			cfg:      Config{Settings: model.Settings{Model: "qwen3.8:27b"}, APIKeySecret: keyRef},
			wantErrs: []string{`model: "qwen3.8:27b" is not a model the openai provider knows; describe it in a capabilities block`},
		},
		{
			name:     "model missing",
			cfg:      Config{BaseURL: ollamaURL},
			wantErrs: []string{"model: required"},
		},
		{
			name:     "unknown model on a compatible server",
			cfg:      Config{Settings: model.Settings{Model: "llama4:70b"}, BaseURL: ollamaURL},
			wantErrs: []string{`model: "llama4:70b" is not a model the openai provider knows; describe it in a capabilities block`},
		},
		{
			name: "unknown model with capabilities",
			cfg: Config{
				Settings: model.Settings{Model: "llama4:70b", Effort: model.EffortHigh},
				BaseURL:  ollamaURL,
				Declared: &DeclaredCapabilities{ToolCalling: true, EffortLevels: []model.Effort{model.EffortHigh}, ContextTokens: 128_000},
			},
		},
		{
			name: "capabilities with values OpenAI does not have",
			cfg: Config{
				Settings: model.Settings{Model: "llama4:70b"},
				BaseURL:  ollamaURL,
				Declared: &DeclaredCapabilities{EffortLevels: []model.Effort{"ultra"}, ContextTokens: -1, MaxOutputTokens: -1},
			},
			wantErrs: []string{
				`capabilities.effortLevels: "ultra" is not an OpenAI effort level`,
				"capabilities.contextTokens: must not be negative",
				"capabilities.maxOutputTokens: must not be negative",
			},
		},
		{
			name:     "negative retries",
			cfg:      Config{Settings: model.Settings{Model: "gpt-6-sol", Retries: -1}, APIKeySecret: keyRef},
			wantErrs: []string{"retries: must not be negative"},
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
				require.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestConfigCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want model.Capabilities
		// wantMinContext is what MinContextTokens returns.
		wantMinContext int
	}{
		{
			name: "known OpenAI model",
			cfg:  Config{Settings: model.Settings{Model: "gpt-5.4-mini"}},
			want: openAIModel(context400K, effortNoneToXHigh),
		},
		{
			name: "known OpenAI model behind a proxy",
			cfg:  Config{Settings: model.Settings{Model: "gpt-6-astra"}, BaseURL: "https://llm-gateway.internal/v1", API: APIResponses},
			want: openAIModel(context1050K, effortLowToMax),
		},
		{
			name:           "preset on Ollama has no forced tool choice",
			cfg:            Config{Settings: model.Settings{Model: "qwen3.8:27b-q4_K_M"}, BaseURL: ollamaURL},
			want:           model.Capabilities{ToolCalling: true, EffortLevels: presets[0].effort, ContextTokens: presetContextTokens, Temperature: true},
			wantMinContext: presetMinContextTokens,
		},
		{
			name:           "preset on vLLM has forced tool choice",
			cfg:            Config{Settings: model.Settings{Model: "google/gemma-4-31B-it"}, BaseURL: "http://vllm:8000/v1", Server: ServerVLLM},
			want:           model.Capabilities{ToolCalling: true, ToolChoice: true, ContextTokens: presetContextTokens, Temperature: true},
			wantMinContext: presetMinContextTokens,
		},
		{
			name:           "preset on an unknown server has no forced tool choice",
			cfg:            Config{Settings: model.Settings{Model: "google/gemma-4-31B-it"}, BaseURL: "http://vllm:8000/v1"},
			want:           model.Capabilities{ToolCalling: true, ContextTokens: presetContextTokens, Temperature: true},
			wantMinContext: presetMinContextTokens,
		},
		{
			name:           "preset on llama.cpp has no forced tool choice",
			cfg:            Config{Settings: model.Settings{Model: "Qwen/Qwen3.8-27B"}, BaseURL: "http://llama:8080/v1", Server: ServerLlamaCPP},
			want:           model.Capabilities{ToolCalling: true, EffortLevels: presets[0].effort, ContextTokens: presetContextTokens, Temperature: true},
			wantMinContext: presetMinContextTokens,
		},
		{
			name: "declared block replaces the preset",
			cfg: Config{
				Settings: model.Settings{Model: "qwen3.8:27b"},
				BaseURL:  ollamaURL,
				Declared: &DeclaredCapabilities{ToolCalling: true, ToolChoice: true, ContextTokens: 32_768},
			},
			want: model.Capabilities{ToolCalling: true, ToolChoice: true, ContextTokens: 32_768, Temperature: true},
		},
		{
			name: "declared block on api.openai.com takes no temperature",
			cfg: Config{
				Settings: model.Settings{Model: "gpt-9"},
				Declared: &DeclaredCapabilities{ToolCalling: true, ToolChoice: true, ContextTokens: 32_768},
			},
			want: model.Capabilities{ToolCalling: true, ToolChoice: true, ContextTokens: 32_768},
		},
		{
			name: "unknown model",
			cfg:  Config{Settings: model.Settings{Model: "gpt-9"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.cfg.Capabilities())
			require.Equal(t, tt.wantMinContext, tt.cfg.MinContextTokens())
		})
	}
}

func TestResolvedServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		want    Server
		wantSet bool
	}{
		{name: "set", cfg: Config{Settings: model.Settings{Model: "qwen3.8:27b"}, BaseURL: ollamaURL, Server: ServerVLLM}, want: ServerVLLM, wantSet: true},
		{name: "Ollama tag", cfg: Config{Settings: model.Settings{Model: "llama4:70b"}, BaseURL: "http://models:8000/v1"}, want: ServerOllama},
		{name: "preset Ollama name without a tag", cfg: Config{Settings: model.Settings{Model: "qwen3.8"}, BaseURL: "http://models:8000/v1"}, want: ServerOllama},
		{name: "Ollama port", cfg: Config{Settings: model.Settings{Model: "Qwen/Qwen3.8-27B"}, BaseURL: ollamaURL}, want: ServerOllama},
		{name: "nothing to go on", cfg: Config{Settings: model.Settings{Model: "Qwen/Qwen3.8-27B"}, BaseURL: "http://vllm:8000/v1"}},
		{name: "fine-tune ID is not a tag", cfg: Config{Settings: model.Settings{Model: "ft:gpt-6-luna:acme::abc123"}, BaseURL: "http://proxy:8000/v1"}},
		{name: "two colons is not a tag", cfg: Config{Settings: model.Settings{Model: "a:b:c"}, BaseURL: "http://models:8000/v1"}},
		{name: "empty tag is not a tag", cfg: Config{Settings: model.Settings{Model: "qwen3.8:"}, BaseURL: "http://models:8000/v1"}},
		{name: "no baseURL", cfg: Config{Settings: model.Settings{Model: "qwen3.8:27b"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, set := tt.cfg.ResolvedServer()
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantSet, set)
		})
	}
}

func TestPresetFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id string
		// want is the index of the preset in presets, or -1 for none.
		want          int
		wantViaOllama bool
	}{
		{id: "qwen3.8", want: 0, wantViaOllama: true},
		{id: "qwen3.8:latest", want: 0, wantViaOllama: true},
		{id: "qwen3.8:27b", want: 0, wantViaOllama: true},
		{id: "qwen3.8:27b-q8_0", want: 0, wantViaOllama: true},
		{id: "qwen3.8:27b-mlx-bf16", want: 0, wantViaOllama: true},
		{id: "Qwen/Qwen3.8-27B", want: 0},
		{id: "Qwen/Qwen3.8-27B-FP8", want: 0},
		{id: "gemma4:31b", want: 1, wantViaOllama: true},
		{id: "gemma4:31b-it-qat", want: 1, wantViaOllama: true},
		{id: "google/gemma-4-31B-it", want: 1},
		{id: "gemma4", want: -1},
		{id: "gemma4:latest", want: -1},
		{id: "gemma4:26b", want: -1},
		{id: "qwen3.8:27", want: -1},
		{id: "qwen3.8:270b", want: -1},
		{id: "Qwen/Qwen3.8-27B:latest", want: -1},
		{id: "gpt-6-sol", want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()

			p, viaOllama, ok := presetFor(tt.id)
			if tt.want < 0 {
				require.False(t, ok)
				require.Nil(t, p)

				return
			}

			require.True(t, ok)
			require.Same(t, &presets[tt.want], p)
			require.Equal(t, tt.wantViaOllama, viaOllama)
		})
	}
}
