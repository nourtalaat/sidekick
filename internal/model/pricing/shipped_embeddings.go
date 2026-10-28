package pricing

import (
	"maps"

	"github.com/nourtalaat/sidekick/internal/model"
)

// shippedEmbeddings holds the prices of the embedding models of this
// release, in US dollars per million input tokens. An embedding call has no
// output, cache or reasoning tokens, so only the input price is set.
//
//   - OpenAI: the Standard tier at https://developers.openai.com/api/docs/pricing,
//     checked on 2026-10-28.
//   - Voyage AI: https://docs.voyageai.com/docs/pricing, checked on
//     2026-10-28. The free token allowance of the current models is not
//     counted.
//   - Amazon Bedrock: the AWS Price List API, standard on-demand tier,
//     us-east-1, offers AmazonBedrock (Titan, publication 2026-10-28) and
//     AmazonBedrockFoundationModels (Cohere Embed v4, publication
//     2026-10-12). The geo inference profiles bill at the price of the
//     Region the call comes from, so they carry the same price. The Cohere
//     price is the Marketplace usage price per million input tokens.
var shippedEmbeddings = bedrockEmbeddings(map[string]float64{
	"amazon.titan-embed-text-v2:0": 0.02,
	"cohere.embed-v4:0":            0.12,
})

// init adds the embedding prices to the shipped table, with the OpenAI and
// Voyage AI ones.
func init() {
	maps.Copy(shippedEmbeddings, openAIEmbeddings())
	maps.Copy(shippedEmbeddings, voyageEmbeddings())
	maps.Copy(shipped, shippedEmbeddings)
}

// openAIEmbeddings returns the prices of the OpenAI embedding models.
func openAIEmbeddings() map[string]model.Price {
	return map[string]model.Price{
		"text-embedding-3-small": perMTok(0.02, 0, 0, 0),
		"text-embedding-3-large": perMTok(0.13, 0, 0, 0),
		"text-embedding-ada-002": perMTok(0.10, 0, 0, 0),
	}
}

// voyageEmbeddings returns the prices of the Voyage AI embedding models.
func voyageEmbeddings() map[string]model.Price {
	return map[string]model.Price{
		"voyage-4-large":  perMTok(0.12, 0, 0, 0),
		"voyage-4":        perMTok(0.06, 0, 0, 0),
		"voyage-4-lite":   perMTok(0.02, 0, 0, 0),
		"voyage-3-large":  perMTok(0.18, 0, 0, 0),
		"voyage-3.5":      perMTok(0.06, 0, 0, 0),
		"voyage-3.5-lite": perMTok(0.02, 0, 0, 0),
		"voyage-code-3":   perMTok(0.18, 0, 0, 0),
	}
}

// bedrockEmbeddings lists each model, given by its bare ID and its price in
// US dollars per million input tokens, under its bare ID and every geo
// inference profile ID.
func bedrockEmbeddings(models map[string]float64) map[string]model.Price {
	out := make(map[string]model.Price)

	for id, usdPerMTok := range models {
		out[id] = perMTok(usdPerMTok, 0, 0, 0)

		for _, prefix := range bedrockGeoPrefixes {
			out[prefix+id] = perMTok(usdPerMTok, 0, 0, 0)
		}
	}

	return out
}
