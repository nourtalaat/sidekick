package action

import "slices"

// Policy says what the deployment allows right now. Sidekick asks it when
// the model proposes, when someone approves and when a proposal runs, so a
// config change takes effect on proposals that are waiting.
type Policy interface {
	// Route returns the action mode and the actions of the route as they
	// are configured now. It reports false for a route that does not exist.
	Route(name string) (RoutePolicy, bool)
}

// RoutePolicy is what one route allows.
type RoutePolicy struct {
	Mode    Mode
	Actions []string
}

// Allows reports whether the route is not read-only and lists the action.
func (p RoutePolicy) Allows(actionName string) bool {
	return p.Mode != ReadOnly && p.Mode != "" && slices.Contains(p.Actions, actionName)
}

// StaticPolicy is a Policy with fixed routes, by name.
type StaticPolicy map[string]RoutePolicy

var _ Policy = StaticPolicy(nil)

// Route implements Policy.
func (p StaticPolicy) Route(name string) (RoutePolicy, bool) {
	r, ok := p[name]

	return r, ok
}
