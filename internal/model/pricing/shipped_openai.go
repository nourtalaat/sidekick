package pricing

import (
	"maps"

	"github.com/nourtalaat/sidekick/internal/model"
)

// openAILongPromptTokens is the prompt length above which OpenAI bills
// GPT-6, GPT-5.6, GPT-5.5 and GPT-5.4 at their long-context prices.
const openAILongPromptTokens = 272_000

// shippedOpenAI holds the OpenAI prices of this release, checked on
// CheckedOn against the Standard tier at
// https://developers.openai.com/api/docs/pricing, in US dollars per million
// tokens. Cache writes use the default 30-minute price. GPT-5.5 and the
// GPT-5.4 models charge nothing extra for cache writes, so writes cost the
// input price. GPT-5.6 Sol's price is promotional until at least
// 2026-11-21. Regional processing costs 10% more; set it with model.prices
// in config.
var shippedOpenAI = map[string]model.Price{
	"gpt-6-astra":   longContext(perMTok(10, 50, 1, 12.50), perMTok(20, 75, 2, 25)),
	"gpt-6.1-sol":   longContext(perMTok(2, 10, 0.10, 2.50), perMTok(4, 15, 0.20, 5)),
	"gpt-6.1-terra": longContext(perMTok(1, 5, 0.05, 1.25), perMTok(2, 7.50, 0.10, 2.50)),
	"gpt-6-sol":     longContext(perMTok(2, 10, 0.20, 2.50), perMTok(4, 15, 0.40, 5)),
	"gpt-6-luna":    longContext(perMTok(0.10, 0.50, 0.01, 0.125), perMTok(0.20, 0.75, 0.02, 0.25)),
	// gpt-5.6 is an alias of gpt-5.6-sol.
	"gpt-5.6":                 longContext(perMTok(4, 20, 0.40, 5), perMTok(8, 30, 0.80, 10)),
	"gpt-5.6-sol":             longContext(perMTok(4, 20, 0.40, 5), perMTok(8, 30, 0.80, 10)),
	"gpt-5.6-terra":           longContext(perMTok(2, 12, 0.20, 2.50), perMTok(4, 18, 0.40, 5)),
	"gpt-5.6-luna":            longContext(perMTok(0.20, 1.20, 0.02, 0.25), perMTok(0.40, 1.80, 0.04, 0.50)),
	"gpt-5.5":                 longContext(perMTok(5, 30, 0.50, 5), perMTok(10, 45, 1, 10)),
	"gpt-5.5-2026-04-23":      longContext(perMTok(5, 30, 0.50, 5), perMTok(10, 45, 1, 10)),
	"gpt-5.4":                 longContext(perMTok(2.50, 15, 0.25, 2.50), perMTok(5, 22.50, 0.50, 5)),
	"gpt-5.4-2026-03-05":      longContext(perMTok(2.50, 15, 0.25, 2.50), perMTok(5, 22.50, 0.50, 5)),
	"gpt-5.4-mini":            perMTok(0.75, 4.50, 0.075, 0.75),
	"gpt-5.4-mini-2026-03-17": perMTok(0.75, 4.50, 0.075, 0.75),
	"gpt-5.4-nano":            perMTok(0.20, 1.25, 0.02, 0.20),
	"gpt-5.4-nano-2026-03-17": perMTok(0.20, 1.25, 0.02, 0.20),
}

// init adds the OpenAI prices to the shipped table. They live in a file of
// their own so each provider's prices can change without touching the
// others.
func init() {
	maps.Copy(shipped, shippedOpenAI)
}

// longContext returns base with above as its price for prompts longer than
// openAILongPromptTokens.
func longContext(base, above model.Price) model.Price {
	return withTier(base, openAILongPromptTokens, above)
}
