package openai

import (
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Context and output sizes of the known models, in tokens.
const (
	context1050K = 1_050_000
	context400K  = 400_000
	output128K   = 128_000
)

// Effort level sets of the known models.
var (
	// effortLowToMax is GPT-6 Astra and GPT-6.1 Sol, which always reason.
	effortLowToMax = []model.Effort{model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortXHigh, model.EffortMax}
	// effortNoneToMax is the other GPT-6 and the GPT-5.6 models.
	effortNoneToMax = []model.Effort{model.EffortNone, model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortXHigh, model.EffortMax}
	// effortNoneToXHigh is GPT-5.5 and the GPT-5.4 models, which predate max.
	effortNoneToXHigh = []model.Effort{model.EffortNone, model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortXHigh}
	// effortAll is every effort level the OpenAI API has.
	effortAll = []model.Effort{model.EffortNone, model.EffortMinimal, model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortXHigh, model.EffortMax}
)

// knownModels are the OpenAI models this connector knows, by model ID and
// snapshot, from OpenAI's model pages as of 2026-10-26. A model that is not
// listed, and is not a local model preset, needs a capabilities block.
//
// No current model takes effort minimal. GPT-5.4 nano is deprecated but
// still served.
var knownModels = map[string]model.Capabilities{
	"gpt-6-astra":             openAIModel(context1050K, effortLowToMax),
	"gpt-6.1-sol":             openAIModel(context1050K, effortLowToMax),
	"gpt-6.1-terra":           openAIModel(context1050K, effortNoneToMax),
	"gpt-6-sol":               openAIModel(context1050K, effortNoneToMax),
	"gpt-6-luna":              openAIModel(context1050K, effortNoneToMax),
	"gpt-5.6":                 openAIModel(context1050K, effortNoneToMax),
	"gpt-5.6-sol":             openAIModel(context1050K, effortNoneToMax),
	"gpt-5.6-terra":           openAIModel(context1050K, effortNoneToMax),
	"gpt-5.6-luna":            openAIModel(context1050K, effortNoneToMax),
	"gpt-5.5":                 openAIModel(context1050K, effortNoneToXHigh),
	"gpt-5.5-2026-04-23":      openAIModel(context1050K, effortNoneToXHigh),
	"gpt-5.4":                 openAIModel(context1050K, effortNoneToXHigh),
	"gpt-5.4-2026-03-05":      openAIModel(context1050K, effortNoneToXHigh),
	"gpt-5.4-mini":            openAIModel(context400K, effortNoneToXHigh),
	"gpt-5.4-mini-2026-03-17": openAIModel(context400K, effortNoneToXHigh),
	"gpt-5.4-nano":            openAIModel(context400K, effortNoneToXHigh),
	"gpt-5.4-nano-2026-03-17": openAIModel(context400K, effortNoneToXHigh),
}

// presetContextTokens is the native context of both preset models.
const presetContextTokens = 262_144

// presetMinContextTokens is the smallest context a preset model should run
// with for an investigation: room for the prompt, several large tool
// results and the model's reasoning. Ollama's default is far below it.
const presetMinContextTokens = 65_536

// ollamaLatestTag is the tag Ollama uses when a model name has none.
const ollamaLatestTag = "latest"

// preset is a local open-weight model whose capabilities ship with
// Sidekick, so it needs no capabilities block.
type preset struct {
	// minContextTokens is the smallest context the server should run the
	// model with.
	minContextTokens int
	// ids are model IDs as vLLM and llama.cpp serve them.
	ids []string
	// ollamaName is the model's name in the Ollama library.
	ollamaName string
	// ollamaTags reports whether an Ollama tag of ollamaName is this model.
	ollamaTags func(tag string) bool
	// effort is what the model takes as reasoning effort.
	effort []model.Effort
}

// presets are the local models with shipped capabilities.
var presets = []preset{
	{
		minContextTokens: presetMinContextTokens,
		ids:              []string{"Qwen/Qwen3.8-27B", "Qwen/Qwen3.8-27B-FP8"},
		ollamaName:       "qwen3.8",
		// The library's latest tag is the 27B model.
		ollamaTags: func(tag string) bool { return tag == ollamaLatestTag || isSizeTag(tag, "27b") },
		effort:     []model.Effort{model.EffortLow, model.EffortMedium, model.EffortXHigh},
	},
	{
		minContextTokens: presetMinContextTokens,
		ids:              []string{"google/gemma-4-31B-it"},
		ollamaName:       "gemma4",
		// The library's latest tag is a smaller model, so it is not a preset.
		ollamaTags: func(tag string) bool { return isSizeTag(tag, "31b") },
	},
}

// capabilities returns the preset's capabilities on a server. Only
// servers known to honour a forced tool choice get ToolChoice.
func (p *preset) capabilities(s Server) model.Capabilities {
	return model.Capabilities{
		ToolCalling:   true,
		ToolChoice:    s == ServerVLLM,
		EffortLevels:  p.effort,
		ContextTokens: presetContextTokens,
	}
}

// presetFor returns the preset of a model ID, and whether the ID named it
// by its Ollama library name.
func presetFor(id string) (p *preset, viaOllama, ok bool) {
	name, tag, hasTag := strings.Cut(id, ":")
	if !hasTag {
		tag = ollamaLatestTag
	}

	for i := range presets {
		switch candidate := &presets[i]; {
		case !hasTag && slices.Contains(candidate.ids, id):
			return candidate, false, true
		case name == candidate.ollamaName && candidate.ollamaTags(tag):
			return candidate, true, true
		}
	}

	return nil, false, false
}

// isSizeTag reports whether an Ollama tag is size, or size followed by a
// variant such as -q4_K_M or -it-qat.
func isSizeTag(tag, size string) bool {
	return tag == size || strings.HasPrefix(tag, size+"-")
}

// openAIModel returns an OpenAI model with 128K output that can call tools,
// accepts a forced tool choice and returns structured output. Prompt
// caching is automatic.
func openAIModel(contextTokens int, effort []model.Effort) model.Capabilities {
	return model.Capabilities{
		ToolCalling:      true,
		ToolChoice:       true,
		EffortLevels:     effort,
		StructuredOutput: true,
		PromptCaching:    true,
		ContextTokens:    contextTokens,
		MaxOutputTokens:  output128K,
	}
}
