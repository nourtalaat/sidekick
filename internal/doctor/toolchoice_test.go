package doctor

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
)

func TestCheckToolChoice(t *testing.T) {
	t.Parallel()

	const message = "the connector fills the investigate role but cannot be forced to call a tool, so each investigation relies on a reminder to end with submit_verdict"

	presetWarning := Result{
		Check: "tool choice local", Status: StatusWarn, Message: message,
		Fix: "serve the model with vLLM, started with --enable-auto-tool-choice, and set `server: vllm` on the connector; investigations still work without it",
	}

	tests := []struct {
		name    string
		replace []string
		want    []Result
	}{
		{
			name:    "an Anthropic model without forced tool choice investigates",
			replace: []string{"INVESTIGATE", "claude"},
		},
		{
			name:    "a preset on vLLM investigates",
			replace: []string{"LOCAL_MODEL", "Qwen/Qwen3.8-27B", "LOCAL_EXTRA", ", server: vllm"},
		},
		{
			name: "a preset without forced tool choice only triages",
			replace: []string{
				"INVESTIGATE", "claude\n    triage: local",
				"ROUTE_ROLES", "\n    roles: { triage: local }",
			},
		},
		{
			name: "a preset on Ollama investigates",
			want: []Result{presetWarning},
		},
		{
			name:    "a preset on an unknown server investigates",
			replace: []string{"LOCAL_MODEL", "Qwen/Qwen3.8-27B"},
			want:    []Result{presetWarning},
		},
		{
			name:    "a preset investigates for one route",
			replace: []string{"INVESTIGATE", "claude", "ROUTE_ROLES", "\n    roles: { investigate: local }"},
			want:    []Result{presetWarning},
		},
		{
			name:    "a declared model without forced tool choice investigates",
			replace: []string{"LOCAL_MODEL", "my-model", "LOCAL_EXTRA", ", capabilities: { toolCalling: true }"},
			want: []Result{{
				Check: "tool choice local", Status: StatusWarn, Message: message,
				Fix: "if the model and its server honour a forced tool choice, set `capabilities.toolChoice: true` on the connector; otherwise use a model that does for the investigate role",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := parseConfig(t, localConfig, defaultSecrets, config.Registries{}, localReplacements(tt.replace...)...)

			got := newDoctor(Options{Config: cfg}).checkToolChoice(t.Context())

			require.Equal(t, tt.want, got)
		})
	}
}
