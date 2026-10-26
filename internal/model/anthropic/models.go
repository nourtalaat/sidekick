package anthropic

import "github.com/nourtalaat/sidekick/internal/model"

// Context and output sizes of the known models, in tokens.
const (
	contextMillion = 1_000_000
	context200K    = 200_000
	output128K     = 128_000
	output64K      = 64_000
)

// offRule says what thinking mode off means for a model. A thinking mode
// that is not set at all always leaves the parameter out, so the model's
// own default applies.
type offRule int

// Off rules.
const (
	// offOmit leaves the thinking parameter out. These models do not think
	// unless asked.
	offOmit offRule = iota
	// offDisable sends thinking disabled.
	offDisable
	// offDisableUpToHigh sends thinking disabled, which these models accept
	// only at effort high or lower.
	offDisableUpToHigh
	// offRejected means the model always thinks and refuses to turn it off;
	// effort is its only control.
	offRejected
)

// knownModel is one entry of the table of known models.
type knownModel struct {
	caps model.Capabilities
	off  offRule
	// thinksByDefault is true for a model that thinks when the thinking
	// parameter is left out.
	thinksByDefault bool
}

// Effort level sets.
var (
	effortAll = []model.Effort{model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortXHigh, model.EffortMax}
	// effortNoXHigh is the set of the 4.6 models, which predate xhigh.
	effortNoXHigh = []model.Effort{model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortMax}
)

// Thinking mode sets.
var (
	thinkingAdaptive = []model.ThinkingMode{model.ThinkingAdaptive}
	thinkingBoth     = []model.ThinkingMode{model.ThinkingAdaptive, model.ThinkingEnabled}
	thinkingEnabled  = []model.ThinkingMode{model.ThinkingEnabled}
)

// knownModels are the Claude models this connector knows, by model ID,
// from Anthropic's model documentation as of 2026-10-26. A model that is
// not listed needs a capabilities block in config.
//
// Notes on the table:
//   - Claude Fable 5.1, Claude Mythos 5.1, Claude Opus 5.5 and Claude
//     Sonnet 5.5 refuse a forced tool choice, so ToolChoice is false.
//   - The 5.x models with offRejected always think. Explicit thinking mode
//     off is rejected for them; lower the effort instead.
//   - Fixed thinking budgets (thinking mode enabled) remain only on the
//     4.6 models, where they are deprecated, and on Claude Haiku 4.5, which
//     has no adaptive thinking and no effort setting.
var knownModels = map[string]knownModel{
	"claude-fable-5-1":  modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"claude-mythos-5-1": modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"claude-fable-5":    modelWith(true, thinkingAdaptive, effortAll, offRejected),
	"claude-mythos-5":   modelWith(true, thinkingAdaptive, effortAll, offRejected),
	"claude-opus-5-5":   modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"claude-opus-5":     modelWith(true, thinkingAdaptive, effortAll, offDisableUpToHigh),
	"claude-opus-4-8":   modelWith(true, thinkingAdaptive, effortAll, offOmit),
	"claude-opus-4-7":   modelWith(true, thinkingAdaptive, effortAll, offOmit),
	"claude-opus-4-6":   modelWith(true, thinkingBoth, effortNoXHigh, offOmit),
	"claude-sonnet-5-5": modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"claude-sonnet-5":   modelWith(true, thinkingAdaptive, effortAll, offDisable),
	"claude-sonnet-4-6": modelWith(true, thinkingBoth, effortNoXHigh, offOmit),
	"claude-haiku-5-5":  modelWith(true, thinkingAdaptive, effortAll, offDisableUpToHigh),
	"claude-haiku-4-5":  haiku45,
	// The dated ID of Claude Haiku 4.5, the only current model with one.
	"claude-haiku-4-5-20251001": haiku45,
}

// haiku45 is Claude Haiku 4.5: a smaller context and output, fixed
// thinking budgets only, and no effort setting.
var haiku45 = knownModel{
	caps: model.Capabilities{
		ToolCalling:      true,
		ToolChoice:       true,
		ThinkingModes:    thinkingEnabled,
		StructuredOutput: true,
		PromptCaching:    true,
		ContextTokens:    context200K,
		MaxOutputTokens:  output64K,
	},
	off: offOmit,
}

// modelWith returns a model with a 1M token context and 128K output that
// can call tools, return structured output and cache prompts.
func modelWith(toolChoice bool, thinking []model.ThinkingMode, effort []model.Effort, off offRule) knownModel {
	return knownModel{
		caps: model.Capabilities{
			ToolCalling:      true,
			ToolChoice:       toolChoice,
			ThinkingModes:    thinking,
			EffortLevels:     effort,
			StructuredOutput: true,
			PromptCaching:    true,
			ContextTokens:    contextMillion,
			MaxOutputTokens:  output128K,
		},
		off:             off,
		thinksByDefault: off != offOmit,
	}
}
