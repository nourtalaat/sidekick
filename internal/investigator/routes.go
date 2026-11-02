package investigator

import (
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/config"
)

// RouteSource gives the settings of the routes in effect. A run asks it
// once, when it starts, so a change to a route is used by the next
// investigation and never by a run that is going.
type RouteSource interface {
	// Route returns the route named name, or false when no route in
	// effect has that name.
	Route(name string) (Route, bool)
	// Names returns the names of the routes in effect, sorted.
	Names() []string
}

// StaticRoutes is a fixed set of routes, by name.
type StaticRoutes map[string]Route

// Route returns the route named name.
func (s StaticRoutes) Route(name string) (Route, bool) {
	r, ok := s[name]

	return r, ok
}

// Names returns the route names, sorted.
func (s StaticRoutes) Names() []string {
	return slices.Sorted(maps.Keys(s))
}

// ConfigRoutes returns a RouteSource that reads the routes in effect from
// current on every call, such as rules.Effective.RouteConfigs of a rules
// cache.
func ConfigRoutes(current func() []config.Route) RouteSource {
	return configRoutes{current: current}
}

// configRoutes reads the routes in effect on every call.
type configRoutes struct {
	current func() []config.Route
}

// Route returns the route in effect named name.
func (c configRoutes) Route(name string) (Route, bool) {
	routes := c.current()

	i := slices.IndexFunc(routes, func(r config.Route) bool { return r.Name == name })
	if i < 0 {
		return Route{}, false
	}

	return routeOf(routes[i]), true
}

// Names returns the names of the routes in effect, sorted.
func (c configRoutes) Names() []string {
	routes := c.current()

	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = r.Name
	}

	slices.Sort(names)

	return names
}

// routeOf returns what a run needs to know about r.
func routeOf(r config.Route) Route {
	return Route{
		Sources:            r.Sources,
		Outputs:            r.Outputs,
		DailyMaxCostMicros: r.Budgets.Daily.MaxCostMicros(),
		DailySkip:          r.Budgets.Daily.OnDailyCap == config.OnDailyCapSkip,
		Details:            maps.Clone(r.Details),
	}
}
