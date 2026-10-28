package google

import "github.com/nourtalaat/sidekick/internal/model"

// Context and output sizes of the known models, in tokens.
const (
	contextTokens = 1_048_576
	outputTokens  = 65_536
)

// Thinking budget ranges of the Gemini 2.5 models, in tokens.
const (
	budgetMinPro       = 128
	budgetMaxPro       = 32_768
	budgetMinFlash     = 1
	budgetMinFlashLite = 512
	budgetMaxFlash     = 24_576
)

// Effort level sets. Gemini 3 and later models take a thinking level, and
// the connector maps effort to it one to one.
var (
	effortLowToHigh     = []model.Effort{model.EffortLow, model.EffortMedium, model.EffortHigh}
	effortMinimalToHigh = []model.Effort{model.EffortMinimal, model.EffortLow, model.EffortMedium, model.EffortHigh}
	// effortAll is every effort level the Gemini API has.
	effortAll = effortMinimalToHigh
)

// Thinking mode sets.
var (
	thinkingAdaptive = []model.ThinkingMode{model.ThinkingAdaptive}
	thinkingBoth     = []model.ThinkingMode{model.ThinkingAdaptive, model.ThinkingEnabled}
)

// budgetRange is the thinking budget a Gemini 2.5 model accepts.
type budgetRange struct {
	min int
	max int
}

// knownModel is one entry of the table of known models.
type knownModel struct {
	caps model.Capabilities
	// budget is set for a model that takes a thinking budget (Gemini 2.5)
	// rather than a thinking level (Gemini 3 and later).
	budget *budgetRange
	// canTurnOff is true for a model whose thinking can be turned off, with
	// a budget of zero. Gemini 3 and later models always think; their
	// lowest level still thinks a little.
	canTurnOff bool
}

// knownModels are the Gemini models this connector knows, by model ID,
// from the Gemini API model pages and the Gemini Enterprise Agent Platform
// thinking page as of 2026-10-27. The same IDs work on both backends. Any
// other model needs a capabilities block in config.
var knownModels = map[string]knownModel{
	"gemini-4-argon":         levelModel(effortLowToHigh),
	"gemini-3.8-flash":       levelModel(effortLowToHigh),
	"gemini-3.6-flash":       levelModel(effortMinimalToHigh),
	"gemini-3.5-flash-lite":  levelModel(effortMinimalToHigh),
	"gemini-3.1-flash-lite":  levelModel(effortMinimalToHigh),
	"gemini-3.1-pro-preview": levelModel(effortLowToHigh),
	"gemini-3-flash-preview": levelModel(effortMinimalToHigh),
	"gemini-2.5-pro":         budgetModel(budgetRange{min: budgetMinPro, max: budgetMaxPro}, false),
	"gemini-2.5-flash":       budgetModel(budgetRange{min: budgetMinFlash, max: budgetMaxFlash}, true),
	"gemini-2.5-flash-lite":  budgetModel(budgetRange{min: budgetMinFlashLite, max: budgetMaxFlash}, true),
}

// levelModel returns a Gemini 3 or later model that takes effort as its
// thinking level and always thinks.
func levelModel(effort []model.Effort) knownModel {
	caps := geminiModel()
	caps.ThinkingModes = thinkingAdaptive
	caps.EffortLevels = effort

	return knownModel{caps: caps}
}

// budgetModel returns a Gemini 2.5 model that takes a thinking budget in
// budget, and can turn thinking off when canTurnOff is true.
func budgetModel(budget budgetRange, canTurnOff bool) knownModel {
	caps := geminiModel()
	caps.ThinkingModes = thinkingBoth

	return knownModel{caps: caps, budget: &budget, canTurnOff: canTurnOff}
}

// geminiModel returns the capabilities every known model shares: a 1M
// token context and 64K output, tool calling with a forced tool choice,
// structured output and implicit prompt caching.
func geminiModel() model.Capabilities {
	return model.Capabilities{
		ToolCalling:      true,
		ToolChoice:       true,
		StructuredOutput: true,
		PromptCaching:    true,
		ContextTokens:    contextTokens,
		MaxOutputTokens:  outputTokens,
	}
}
