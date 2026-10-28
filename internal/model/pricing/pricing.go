// Package pricing holds the model price table that ships with Sidekick and
// merges the model.prices overrides from config into it. Connectors report
// token usage only; callers price it here, so the cost of every step comes
// from one table.
package pricing

import (
	"cmp"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Table maps model IDs to prices. The zero Table has no prices. A Table is
// not changed after it is made, so it is safe to share between goroutines.
type Table struct {
	prices map[string][]period
}

// period is a price and the time from which it applies. The periods of a
// model are sorted by from, and the first one starts at the zero time.
type period struct {
	from  time.Time
	price model.Price
}

// Unpriced is a configured connector whose model has no price.
type Unpriced struct {
	Connector string
	Model     string
}

// Lookup returns the price of the model with the exact ID id that applies
// now. A caller prices a step right after the model call, so now is the time
// the step is saved.
func (t Table) Lookup(id string) (model.Price, bool) {
	return t.LookupAt(id, time.Now())
}

// LookupAt returns the price of the model with the exact ID id that applies
// at the given time. A shipped price can start on a date, such as a price
// rise that its provider announced; a price from the config applies at every
// time.
func (t Table) LookupAt(id string, at time.Time) (model.Price, bool) {
	periods := t.prices[id]

	i := sort.Search(len(periods), func(i int) bool { return periods[i].from.After(at) })
	if i == 0 {
		return model.Price{}, false
	}

	return periods[i-1].price, true
}

// Cost returns the cost of usage on model id in micro-dollars at the current
// price, and false when the model has no price.
func (t Table) Cost(id string, usage model.Usage) (int64, bool) {
	return t.CostAt(id, usage, time.Now())
}

// CostAt returns the cost of usage on model id in micro-dollars at the price
// that applies at the given time, and false when the model has no price.
func (t Table) CostAt(id string, usage model.Usage, at time.Time) (int64, bool) {
	p, ok := t.LookupAt(id, at)
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
// replaces the shipped price of its model entirely, tiers and start dates
// included, and can add a model the shipped table does not have. Pass the
// result of config.Model.PriceTable as overrides.
func New(overrides map[string]model.Price) Table {
	base := maps.Clone(shipped)
	maps.Copy(base, bedrockShipped)

	return build(base, shippedChanges, overrides)
}

// build makes a Table from the prices of base, the later prices in changes
// and overrides, which replace both.
func build(base map[string]model.Price, changes map[string][]period, overrides map[string]model.Price) Table {
	prices := make(map[string][]period, len(base)+len(overrides))

	for id, p := range base {
		prices[id] = append([]period{{price: p}}, changes[id]...)
	}

	for id, p := range overrides {
		prices[id] = []period{{price: p}}
	}

	return Table{prices: prices}
}
