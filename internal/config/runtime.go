package config

import (
	"context"
	"slices"
)

// The path prefixes of problems in runtime rules, as people see them in
// Slack.
const (
	runtimeRoutesPath    = "routes"
	runtimeListenersPath = "listeners"
)

// NewRoute returns a route named name with the defaults a file route gets.
// Runtime routes start from it.
func NewRoute(name string) Route {
	r := Route{Name: name}
	r.setDefaults()

	return r
}

// CheckRuntimeRoute checks a route saved at runtime, from Slack or the web
// UI, against c, the loaded file config. It runs the checks file routes
// get, and the runtime ceilings: the route may not take the name of a file
// route or be the default route, and it may name only the connectors,
// sources, outputs and strategies of the file, with budgets at or below
// the file's ceilings. Its match.intake may also name runtimeListeners.
// The error wraps Errors, with paths below routes.<name>.
func (c *Config) CheckRuntimeRoute(ctx context.Context, r Route, runtimeListeners []string) error {
	v := c.runtimeValidator(ctx, runtimeListeners)
	at := joinPath(runtimeRoutesPath, r.Name)

	switch {
	case r.Name == DefaultRouteName:
		v.errs.add(at, "the %s route is set only in the config file", DefaultRouteName)
	case !namePattern.MatchString(r.Name):
		v.errs.add(at, "route name %q %s", r.Name, nameRule)
	case slices.ContainsFunc(c.Routes, func(f Route) bool { return f.Name == r.Name }):
		v.errs.add(at, "route %q is defined in the config file, which only a config change can alter", r.Name)
	}

	if r.Match == nil {
		v.errs.add(at+".match", "required: a runtime route needs a match, or it would take every event before the default route")
	}

	v.checkRoute(at, r)

	return v.errs.err()
}

// CheckRuntimeListener checks a listener saved at runtime against c, the
// loaded file config, with the checks file listeners get. It may not take
// the name of a file listener. The error wraps Errors, with paths below
// listeners.<name>.
func (c *Config) CheckRuntimeListener(ctx context.Context, name string, l Listener) error {
	v := c.runtimeValidator(ctx, nil)
	at := joinPath(runtimeListenersPath, name)

	if _, clash := c.Listeners[name]; clash {
		v.errs.add(at, "listener %q is defined in the config file, which only a config change can alter", name)
	}

	if !c.Slack.Connects() {
		v.errs.add(at, "listeners need the Slack app to receive events: set slack.appTokenSecret, or slack.mode: events with slack.signingSecret")
	}

	v.checkListener(at, name, l)

	return v.errs.err()
}

// runtimeValidator returns a validator for runtime rules on c, with the
// strategies c was loaded with, and runtimeListeners as further intakes.
func (c *Config) runtimeValidator(ctx context.Context, runtimeListeners []string) *validator {
	v := newValidator(ctx, c, LoadOptions{})
	v.strategies = c.strategySet
	v.extraIntakes = runtimeListeners

	return v
}
