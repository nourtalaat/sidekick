package bedrock

import (
	"strings"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Context and output sizes of the known models, in tokens.
const (
	contextMillion = 1_000_000
	context200K    = 200_000
	output128K     = 128_000
	output64K      = 64_000
)

// anthropicPrefix starts the Bedrock ID of every Anthropic model. Thinking
// and effort are sent in Anthropic's request shape, so only these models
// may use them.
const anthropicPrefix = "anthropic."

// profilePrefixes are the prefixes of the system-defined inference profiles
// of the known models: geographic (US, GovCloud, EU, Asia Pacific,
// Australia, Japan and India) and global. A profile runs the same model as
// its bare ID.
var profilePrefixes = []string{"us.", "us-gov.", "eu.", "apac.", "au.", "jp.", "in.", "global."}

// offRule says what thinking mode off means for a model. A thinking mode
// that is not set at all always leaves the thinking field out, so the
// model's own default applies.
type offRule int

// Off rules.
const (
	// offOmit leaves the thinking field out. These models do not think
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
	// field is left out.
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

// knownModels are the Claude models on Bedrock this connector knows, by
// their bare Bedrock model ID, from the Bedrock model cards and the
// adaptive and extended thinking pages as of 2026-10-26. An inference
// profile ID such as us.anthropic.claude-opus-5-5 or
// global.anthropic.claude-opus-5-5 finds the same entry. Any other model
// needs a capabilities block in config.
//
// Notes on the table:
//   - Claude Fable 5.1, Claude Mythos 5.1, Claude Opus 5.5 and Claude
//     Sonnet 5.5 take tool choice auto only, so ToolChoice is false.
//   - The models with offRejected always think. Thinking mode off is
//     rejected for them; lower the effort instead.
//   - Fixed thinking budgets (thinking mode enabled) remain only on the
//     4.6 models, where they are deprecated, and on Claude Haiku 4.5, which
//     has no adaptive thinking and no effort setting.
var knownModels = map[string]knownModel{
	"anthropic.claude-fable-5-1":               modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"anthropic.claude-mythos-5-1":              modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"anthropic.claude-fable-5":                 modelWith(true, thinkingAdaptive, effortAll, offRejected),
	"anthropic.claude-mythos-5":                modelWith(true, thinkingAdaptive, effortAll, offRejected),
	"anthropic.claude-opus-5-5":                modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"anthropic.claude-opus-5":                  modelWith(true, thinkingAdaptive, effortAll, offDisableUpToHigh),
	"anthropic.claude-opus-4-8":                modelWith(true, thinkingAdaptive, effortAll, offOmit),
	"anthropic.claude-opus-4-7":                modelWith(true, thinkingAdaptive, effortAll, offOmit),
	"anthropic.claude-opus-4-6-v1":             modelWith(true, thinkingBoth, effortNoXHigh, offOmit),
	"anthropic.claude-sonnet-5-5":              modelWith(false, thinkingAdaptive, effortAll, offRejected),
	"anthropic.claude-sonnet-5":                modelWith(true, thinkingAdaptive, effortAll, offDisable),
	"anthropic.claude-sonnet-4-6":              modelWith(true, thinkingBoth, effortNoXHigh, offOmit),
	"anthropic.claude-haiku-5-5":               modelWith(true, thinkingAdaptive, effortAll, offDisableUpToHigh),
	"anthropic.claude-haiku-4-5-20251001-v1:0": haiku45,
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

// lookupModel returns the table entry of a model ID or inference profile
// ID, and false when the model is not in the table.
func lookupModel(id string) (knownModel, bool) {
	m, ok := knownModels[baseModelID(id)]

	return m, ok
}

// baseModelID strips one inference profile prefix from id, if it has one.
func baseModelID(id string) string {
	for _, p := range profilePrefixes {
		if rest, ok := strings.CutPrefix(id, p); ok {
			return rest
		}
	}

	return id
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
