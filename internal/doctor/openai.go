package doctor

import (
	"github.com/nourtalaat/sidekick/internal/model/openai"
)

// openAIPreset returns the decoded config of an OpenAI-compatible connector
// that runs a local model preset, and false for any other connector.
func openAIPreset(decoded any) (*openai.Config, bool) {
	cfg, ok := decoded.(*openai.Config)
	if !ok || cfg.MinContextTokens() == 0 {
		return nil, false
	}

	return cfg, true
}
