package rules

import (
	"github.com/nourtalaat/sidekick/internal/action"
)

// ActionPolicy is the action.Policy of the routes in effect: the routes of
// the file and the runtime routes, as the cache has them now. A route
// changed in the web UI applies to proposals that are waiting at the next
// reload.
type ActionPolicy struct {
	Cache *Cache
}

var _ action.Policy = ActionPolicy{}

// Route implements action.Policy.
func (p ActionPolicy) Route(name string) (action.RoutePolicy, bool) {
	for _, r := range p.Cache.Current().RouteConfigs() {
		if r.Name == name {
			return action.RoutePolicy{Mode: r.Mode, Actions: r.Actions}, true
		}
	}

	return action.RoutePolicy{}, false
}
