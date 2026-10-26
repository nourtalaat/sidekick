package openai

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model/pricing"
)

// TestKnownModelsArePriced checks that every known OpenAI model has a
// shipped price, so its steps count in the budgets.
func TestKnownModelsArePriced(t *testing.T) {
	t.Parallel()

	prices := pricing.Shipped()

	for id := range knownModels {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			_, ok := prices.Lookup(id)
			require.True(t, ok, "no shipped price for %s", id)
		})
	}
}
