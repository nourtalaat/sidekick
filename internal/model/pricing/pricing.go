// Package pricing holds the model price table that ships with Sidekick and
// merges the model.prices overrides from config into it. Connectors report
// token usage only; callers price it here, so the cost of every step comes
// from one table.
package pricing

import (
	"cmp"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Table maps model IDs to prices. The zero Table has no prices. A Table is
// not changed after it is made, so it is safe to share between goroutines.
type Table struct {
	prices map[string]model.Price
}

// Unpriced is a configured connector whose model has no price.
type Unpriced struct {
	Connector string
	Model     string
}

// Lookup returns the price of the model with the exact ID id.
func (t Table) Lookup(id string) (model.Price, bool) {
	p, ok := t.prices[id]

	return p, ok
}

// Cost returns the cost of usage on model id in micro-dollars, and false
// when the model has no price.
func (t Table) Cost(id string, usage model.Usage) (int64, bool) {
	p, ok := t.Lookup(id)
	if !ok {
		return 0, false
	}

	return p.Cost(usage), true
}

// Unpriced returns the connectors, from a map of connector name to model
// ID, whose model has no price, sorted by connector name. sidekick doctor
// warns about them, since their steps would cost nothing in the budgets.
func (t Table) Unpriced(models map[string]string) []Unpriced {
	var out []Unpriced

	for connector, id := range models {
		if _, ok := t.Lookup(id); !ok {
			out = append(out, Unpriced{Connector: connector, Model: id})
		}
	}

	slices.SortFunc(out, func(a, b Unpriced) int {
		return cmp.Compare(a.Connector, b.Connector)
	})

	return out
}

// IDs returns the model IDs that have a price, sorted.
func (t Table) IDs() []string {
	return slices.Sorted(maps.Keys(t.prices))
}

// Shipped returns the table that ships with this release, without overrides.
func Shipped() Table {
	return New(nil)
}

// New returns the shipped table with overrides merged in. An override
// replaces the shipped price of its model entirely, tiers included, and can
// add a model the shipped table does not have. Pass the result of
// config.Model.PriceTable as overrides.
func New(overrides map[string]model.Price) Table {
	prices := maps.Clone(shipped)
	maps.Copy(prices, overrides)

	return Table{prices: prices}
}
