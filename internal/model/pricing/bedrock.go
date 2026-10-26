package pricing

import "github.com/nourtalaat/sidekick/internal/model"

// Inference profile prefixes of Claude models on Amazon Bedrock. A geo
// profile and the bare model ID cost the regional price; the global
// profile costs the global price. GovCloud (us-gov.) is billed in its own
// partition at other prices, so it has no shipped entry.
var (
	bedrockGeoPrefixes  = []string{"us.", "eu.", "apac.", "au.", "jp.", "in."}
	bedrockGlobalPrefix = "global."
)

// bedrockClaude is the Bedrock price of one Claude model: the regional
// price, for the bare model ID and the geo profiles, and the global price,
// for the global profile. Global is nil when the model has no global
// profile.
type bedrockClaude struct {
	regional model.Price
	global   *model.Price
}

// bedrockShipped is the Bedrock part of the shipped price table, for the
// Claude models the bedrock connector knows, in US dollars per million
// tokens, checked on CheckedOn.
//
// The prices come from the AWS Price List API, offer
// AmazonBedrockFoundationModels, publication 2026-10-12, at
// https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrockFoundationModels/current/us-east-1/index.json,
// standard on-demand tier. The global profile costs the same as the Claude
// API; in-Region and geo profiles cost 10% more. The Region files for
// us-east-1, eu-west-1, eu-west-2, ap-northeast-1 and ap-southeast-2 list
// the same prices. Cache writes use the 5-minute price, the cache lifetime
// the bedrock connector asks for. The public pricing page at
// https://aws.amazon.com/bedrock/pricing/ shows the same tables in a
// browser.
var bedrockShipped = bedrockPrices(map[string]bedrockClaude{
	"anthropic.claude-fable-5-1":   regionalAndGlobal(perMTok(11, 55, 0.275, 13.75), perMTok(10, 50, 0.25, 12.50)),
	"anthropic.claude-mythos-5-1":  regionalAndGlobal(perMTok(11, 55, 0.275, 13.75), perMTok(10, 50, 0.25, 12.50)),
	"anthropic.claude-fable-5":     regionalAndGlobal(perMTok(11, 55, 1.10, 13.75), perMTok(10, 50, 1, 12.50)),
	"anthropic.claude-mythos-5":    {regional: perMTok(11, 55, 1.10, 13.75)},
	"anthropic.claude-opus-5-5":    regionalAndGlobal(perMTok(4.40, 22, 0.22, 5.50), perMTok(4, 20, 0.20, 5)),
	"anthropic.claude-opus-5":      regionalAndGlobal(perMTok(5.50, 27.50, 0.55, 6.875), perMTok(5, 25, 0.50, 6.25)),
	"anthropic.claude-opus-4-8":    regionalAndGlobal(perMTok(5.50, 27.50, 0.55, 6.875), perMTok(5, 25, 0.50, 6.25)),
	"anthropic.claude-opus-4-7":    regionalAndGlobal(perMTok(5.50, 27.50, 0.55, 6.875), perMTok(5, 25, 0.50, 6.25)),
	"anthropic.claude-opus-4-6-v1": regionalAndGlobal(perMTok(5.50, 27.50, 0.55, 6.875), perMTok(5, 25, 0.50, 6.25)),
	"anthropic.claude-sonnet-5-5":  regionalAndGlobal(perMTok(2.20, 11, 0.11, 2.75), perMTok(2, 10, 0.10, 2.50)),
	"anthropic.claude-sonnet-5":    regionalAndGlobal(perMTok(2.20, 11, 0.22, 2.75), perMTok(2, 10, 0.20, 2.50)),
	"anthropic.claude-sonnet-4-6":  regionalAndGlobal(perMTok(3.30, 16.50, 0.33, 4.125), perMTok(3, 15, 0.30, 3.75)),
	"anthropic.claude-haiku-5-5": regionalAndGlobal(
		withTier(perMTok(0.11, 0.55, 0.011, 0.1375), haikuLongPromptTokens, perMTok(0.55, 2.75, 0.055, 0.6875)),
		withTier(perMTok(0.10, 0.50, 0.01, 0.125), haikuLongPromptTokens, perMTok(0.50, 2.50, 0.05, 0.625)),
	),
	"anthropic.claude-haiku-4-5-20251001-v1:0": regionalAndGlobal(perMTok(1.10, 5.50, 0.11, 1.375), perMTok(1, 5, 0.10, 1.25)),
})

// bedrockPrices lists each model under its bare ID, every geo profile ID
// and, when it has one, its global profile ID.
func bedrockPrices(models map[string]bedrockClaude) map[string]model.Price {
	out := make(map[string]model.Price)

	for id, p := range models {
		out[id] = p.regional
		for _, prefix := range bedrockGeoPrefixes {
			out[prefix+id] = p.regional
		}

		if p.global != nil {
			out[bedrockGlobalPrefix+id] = *p.global
		}
	}

	return out
}

// regionalAndGlobal returns the price of a model with a global profile.
func regionalAndGlobal(regional, global model.Price) bedrockClaude {
	return bedrockClaude{regional: regional, global: &global}
}
