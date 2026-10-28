package pricing

import (
	"maps"
	"time"

	"github.com/nourtalaat/sidekick/internal/model"
)

// geminiLongPromptTokens is the prompt length above which Google bills
// Gemini 3.1 Pro and Gemini 2.5 Pro at their long-context prices.
const geminiLongPromptTokens = 200_000

// shippedGoogle holds the Gemini prices of this release, checked on
// 2026-10-28 against the Standard paid tier at
// https://ai.google.dev/gemini-api/docs/pricing, in US dollars per million
// tokens, for text input. Output includes thinking. Caching is implicit and
// has no write charge, so cache writes cost the input price; the google
// connector reports no cache writes. Cache storage per hour, used only by
// explicit caching, is not priced.
//
// The same model IDs are used on Vertex AI, where these are the global
// endpoint prices. Regional endpoints, the Gemini API free tier and
// Flex or Priority processing cost something else; set them with
// model.prices in config.
//
// Google announced price rises for three models; they are in
// shippedGoogleChanges and apply from their dates.
var shippedGoogle = map[string]model.Price{
	"gemini-4-argon":         perMTok(2, 10, 0.10, 2),
	"gemini-3.8-flash":       perMTok(0.75, 3.75, 0.075, 0.75),
	"gemini-3.6-flash":       perMTok(0.75, 3.75, 0.075, 0.75),
	"gemini-3.5-flash-lite":  perMTok(0.30, 2.50, 0.03, 0.30),
	"gemini-3.1-flash-lite":  perMTok(0.25, 1.50, 0.025, 0.25),
	"gemini-3.1-pro-preview": longPrompt(perMTok(2, 12, 0.20, 2), perMTok(4, 18, 0.40, 4)),
	"gemini-3-flash-preview": perMTok(0.50, 3, 0.05, 0.50),
	"gemini-2.5-pro":         longPrompt(perMTok(1.25, 10, 0.125, 1.25), perMTok(2.50, 15, 0.25, 2.50)),
	"gemini-2.5-flash":       perMTok(0.30, 2.50, 0.03, 0.30),
	"gemini-2.5-flash-lite":  perMTok(0.10, 0.40, 0.01, 0.10),
}

// shippedGoogleChanges holds the price rises that Google announced on the
// pricing page named above, with the dates they start. Cache writes follow
// the input price, as in shippedGoogle.
var shippedGoogleChanges = map[string][]period{
	"gemini-4-argon":   {from(2026, time.December, 1, perMTok(4, 20, 0.20, 4))},
	"gemini-3.8-flash": {from(2027, time.January, 1, perMTok(1.50, 7.50, 0.15, 1.50))},
	"gemini-3.6-flash": {from(2027, time.January, 1, perMTok(1.50, 7.50, 0.15, 1.50))},
}

// init adds the Gemini prices to the shipped table.
func init() {
	maps.Copy(shipped, shippedGoogle)
	maps.Copy(shippedChanges, shippedGoogleChanges)
}

// longPrompt returns base with above as its price for prompts longer than
// geminiLongPromptTokens.
func longPrompt(base, above model.Price) model.Price {
	return withTier(base, geminiLongPromptTokens, above)
}
