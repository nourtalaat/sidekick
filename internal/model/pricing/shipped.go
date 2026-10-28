package pricing

import (
	"time"

	"github.com/nourtalaat/sidekick/internal/model"
)

// CheckedOn is the date the shipped prices were last checked against
// https://platform.claude.com/docs/en/about-claude/pricing and the
// providers' pricing pages named next to their prices, and the Amazon
// Bedrock prices in bedrock.go against the AWS Price List API.
const CheckedOn = "2026-10-26"

// usd is one US dollar in micro-dollars, so prices below read as they do on
// the pricing page.
const usd = 1_000_000

// haikuLongPromptTokens is the prompt length above which Claude Haiku 5.5
// is billed at its higher tier. The prompt counts input, cache read and
// cache write tokens.
const haikuLongPromptTokens = 100_000

// roundHalf rounds a positive float to the nearest integer when added
// before truncation.
const roundHalf = 0.5

// shipped is the price table of this release, checked on CheckedOn against
// the Claude API (first-party) prices at
// https://platform.claude.com/docs/en/about-claude/pricing, in US dollars
// per million tokens. Cache writes use the 5-minute price, the cache
// lifetime the Anthropic connector asks for. Amazon Bedrock model IDs have
// their own prices in bedrockShipped. Prices on Google Cloud differ; set
// them with model.prices in config.
var shipped = map[string]model.Price{
	"claude-fable-5-1":  perMTok(10, 50, 0.25, 12.50),
	"claude-mythos-5-1": perMTok(10, 50, 0.25, 12.50),
	"claude-fable-5":    perMTok(10, 50, 1, 12.50),
	"claude-mythos-5":   perMTok(10, 50, 1, 12.50),
	"claude-opus-5-5":   perMTok(4, 20, 0.20, 5),
	"claude-opus-5":     perMTok(5, 25, 0.50, 6.25),
	"claude-opus-4-8":   perMTok(5, 25, 0.50, 6.25),
	"claude-opus-4-7":   perMTok(5, 25, 0.50, 6.25),
	"claude-opus-4-6":   perMTok(5, 25, 0.50, 6.25),
	"claude-sonnet-5-5": perMTok(2, 10, 0.10, 2.50),
	"claude-sonnet-5":   perMTok(2, 10, 0.20, 2.50),
	"claude-sonnet-4-6": perMTok(3, 15, 0.30, 3.75),
	"claude-haiku-5-5": withTier(
		perMTok(0.10, 0.50, 0.01, 0.125),
		haikuLongPromptTokens,
		perMTok(0.50, 2.50, 0.05, 0.625),
	),
	"claude-haiku-4-5":          perMTok(1, 5, 0.10, 1.25),
	"claude-haiku-4-5-20251001": perMTok(1, 5, 0.10, 1.25),
}

// shippedChanges holds the prices that a provider has announced for a later
// date, by model ID, sorted by date. Each applies from its date in place of
// the price in shipped, which applies until the first one.
var shippedChanges = map[string][]period{}

// from makes the period of a price that starts on the given UTC date.
func from(year int, month time.Month, day int, price model.Price) period {
	return period{from: time.Date(year, month, day, 0, 0, 0, 0, time.UTC), price: price}
}

// perMTok makes a Price from US dollars per million tokens.
func perMTok(input, output, cacheRead, cacheWrite float64) model.Price {
	return model.Price{
		InputPerMTok:      dollars(input),
		OutputPerMTok:     dollars(output),
		CacheReadPerMTok:  dollars(cacheRead),
		CacheWritePerMTok: dollars(cacheWrite),
	}
}

// withTier returns base with above as its price for prompts longer than
// threshold tokens.
func withTier(base model.Price, threshold int64, above model.Price) model.Price {
	base.TierAboveInputTokens = threshold
	base.Above = &above

	return base
}

// dollars converts US dollars to micro-dollars, rounded to the nearest
// micro-dollar so that 0.125 and 12.50 are exact.
func dollars(v float64) int64 {
	return int64(v*usd + roundHalf)
}
