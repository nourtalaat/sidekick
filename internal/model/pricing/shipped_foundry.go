package pricing

import (
	"maps"

	"github.com/nourtalaat/sidekick/internal/model"
)

// shippedFoundry holds the prices of the Microsoft Foundry models from
// other providers that the azure connector knows, checked on 2026-10-28
// against the Global Standard meters of the "Foundry Models" service in the
// Azure Retail Prices API (https://prices.azure.com/api/retail/prices), in
// US dollars per million tokens. Caching is automatic and has no write
// charge, so cache writes cost the input price. Data Zone deployments cost
// 10% more; set them with model.prices in config.
//
// The Grok models are left out: their meters have a long-context price
// whose prompt length Azure does not publish. DeepSeek-V3.2 is left out
// because it has no cached-input price.
var shippedFoundry = map[string]model.Price{
	"DeepSeek-V4-Pro":   perMTok(1.74, 3.48, 0.145, 1.74),
	"DeepSeek-V4-Flash": perMTok(0.19, 0.51, 0.028, 0.19),
	"Kimi-K2.6":         perMTok(0.95, 4, 0.16, 0.95),
	"Kimi-K2.5":         perMTok(0.60, 3, 0.10, 0.60),
	"MAI-Thinking-1":    perMTok(2, 8, 0.20, 2),
}

// init adds the Foundry prices to the shipped table.
func init() {
	maps.Copy(shipped, shippedFoundry)
}
