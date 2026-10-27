package pricing

import (
	"maps"

	"github.com/nourtalaat/sidekick/internal/model"
)

// geminiLongPromptTokens is the prompt length above which Google bills
// Gemini 3.1 Pro and Gemini 2.5 Pro at their long-context prices.
const geminiLongPromptTokens = 200_000

// shippedGoogle holds the Gemini prices of this release, checked on
// 2026-10-27 against the Standard paid tier at
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
var shippedGoogle = map[string]model.Price{
	// TODO: [SK-74] Gemini 4 Argon doubles to 4, 20 and 0.40 on 2026-12-01.
	"gemini-4-argon": perMTok(2, 10, 0.10, 2),
	// TODO: [SK-74] Gemini 3.8 Flash doubles to 1.50, 7.50 and 0.15 on 2027-01-01.
	"gemini-3.8-flash": perMTok(0.75, 3.75, 0.075, 0.75),
	// TODO: [SK-74] Gemini 3.6 Flash doubles to 1.50, 7.50 and 0.15 on 2027-01-01.
	"gemini-3.6-flash":       perMTok(0.75, 3.75, 0.075, 0.75),
	"gemini-3.5-flash-lite":  perMTok(0.30, 2.50, 0.03, 0.30),
	"gemini-3.1-flash-lite":  perMTok(0.25, 1.50, 0.025, 0.25),
	"gemini-3.1-pro-preview": longPrompt(perMTok(2, 12, 0.20, 2), perMTok(4, 18, 0.40, 4)),
	"gemini-3-flash-preview": perMTok(0.50, 3, 0.05, 0.50),
	"gemini-2.5-pro":         longPrompt(perMTok(1.25, 10, 0.125, 1.25), perMTok(2.50, 15, 0.25, 2.50)),
	"gemini-2.5-flash":       perMTok(0.30, 2.50, 0.03, 0.30),
	"gemini-2.5-flash-lite":  perMTok(0.10, 0.40, 0.01, 0.10),
}

// init adds the Gemini prices to the shipped table.
func init() {
	maps.Copy(shipped, shippedGoogle)
}

// longPrompt returns base with above as its price for prompts longer than
// geminiLongPromptTokens.
func longPrompt(base, above model.Price) model.Price {
	return withTier(base, geminiLongPromptTokens, above)
}
