package openai

import "github.com/nourtalaat/sidekick/internal/model"

// Context and output sizes of the known Foundry models, in tokens.
const (
	context1M   = 1_000_000
	context500K = 500_000
	context262K = 262_144
	context256K = 256_000
	context200K = 200_000
	context128K = 128_000
	output384K  = 384_000
	output64K   = 64_000
	output8K    = 8_192
)

// foundryModels are models from other providers that Azure sells in
// Microsoft Foundry, by the model ID Foundry gives them, from the Foundry
// Models page as of 2026-10-28. They answer on Chat Completions on the v1
// API. Foundry does not say whether they honour a forced tool choice, so
// none is assumed, the safe default: the investigation reminds the model
// to submit its verdict instead. They take no effort setting.
//
// MAI-Thinking-1 is not here: Foundry serves it under /mai/v1, not the v1
// API this connector calls.
var foundryModels = map[string]model.Capabilities{
	"DeepSeek-V4-Pro":   withoutTools(foundryModel(context1M, output384K, true, true)),
	"DeepSeek-V4-Flash": withoutTools(foundryModel(context1M, output384K, true, true)),
	"DeepSeek-V3.2":     withoutTools(foundryModel(context128K, context128K, true, false)),
	"Kimi-K2.6":         foundryModel(context262K, context262K, false, true),
	"Kimi-K2.5":         foundryModel(context262K, context262K, false, true),
	"grok-4.7":          foundryModel(context500K, context500K, true, true),
	"grok-4.6":          foundryModel(context200K, context128K, true, true),
	"grok-4.3":          foundryModel(context200K, output8K, false, true),
}

// foundryModel returns a Foundry model that calls tools without a forced
// tool choice.
func foundryModel(contextTokens, outputTokens int, structuredOutput, promptCaching bool) model.Capabilities {
	return model.Capabilities{
		ToolCalling:      true,
		StructuredOutput: structuredOutput,
		PromptCaching:    promptCaching,
		ContextTokens:    contextTokens,
		MaxOutputTokens:  outputTokens,
	}
}

// withoutTools marks a model whose tool calls the connector cannot carry:
// DeepSeek's thinking models answer a tool call turn with a 400 error
// unless the reasoning_content of earlier turns is sent back, and Chat
// Completions in this connector does not send reasoning back. Such a model
// cannot be the investigate model; it can still triage or summarize.
func withoutTools(caps model.Capabilities) model.Capabilities {
	caps.ToolCalling = false

	return caps
}
