// Package rules holds the runtime listeners and routes that people add from
// Slack, and merges them with those of the config file into the rules in
// effect. The file wins on name clashes. Runtime rules live in Postgres, so
// every replica sees the same ones: a change notifies rules_changed, and
// each replica reloads on the notification and every minute.
package rules

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/nourtalaat/sidekick/internal/config"
)

// Layer says where a rule comes from.
type Layer string

// The layers, in the order routes of the same priority are tried.
const (
	LayerFile    Layer = "file"
	LayerRuntime Layer = "runtime"
)

// The kinds of rule, as the audit log names them.
const (
	KindListener = "listener"
	KindRoute    = "route"
)

// Listener is a runtime listener as stored.
type Listener struct {
	Name      string
	Config    config.Listener
	Enabled   bool
	Version   int
	UpdatedBy string
	UpdatedAt time.Time
}

// Route is a runtime route as stored. Config.Name is its name.
type Route struct {
	Config    config.Route
	Enabled   bool
	Version   int
	UpdatedBy string
	UpdatedAt time.Time
}

// Runtime is every stored runtime rule.
type Runtime struct {
	Listeners []Listener
	Routes    []Route
}

// EffectiveListener is a listener in effect.
type EffectiveListener struct {
	Name   string
	Config config.Listener
	Layer  Layer
}

// EffectiveRoute is a route in effect.
type EffectiveRoute struct {
	Config config.Route
	Layer  Layer
}

// Skipped is a runtime rule that is stored but not in effect, and why.
type Skipped struct {
	Kind   string
	Name   string
	Reason string
}

// Effective is the rules in effect: the file's and the runtime ones that
// passed the checks.
type Effective struct {
	// Listeners are in name order, the order messages are matched in.
	Listeners []EffectiveListener
	// Routes are in the order they are tried: by priority, lower first,
	// then file before runtime, then by name, with the default route last.
	Routes []EffectiveRoute
	// Shadowed are runtime rules with the name of a file rule, which wins.
	Shadowed []Skipped
	// Invalid are runtime rules that no longer pass the checks, for
	// example because the file no longer defines a source they name.
	Invalid []Skipped
	// Disabled are runtime routes that are turned off.
	Disabled []Skipped
	// LoadedAt is when the runtime rules were read, or zero for the file
	// rules alone.
	LoadedAt time.Time
}

// Reasons for skipping a rule.
const (
	reasonShadowed = "a rule in the config file has the same name"
	reasonDisabled = "disabled"
)

// Merge returns the rules in effect from the file config and the stored
// runtime rules. Runtime rules are checked with the file's checks; those
// that fail are left out and listed in Invalid.
func Merge(ctx context.Context, file *config.Config, rt Runtime) *Effective {
	e := &Effective{}
	runtimeListeners := e.mergeListeners(ctx, file, rt.Listeners)
	e.mergeRoutes(ctx, file, rt.Routes, runtimeListeners)

	return e
}

// ListenerConfigs returns the listeners in effect, by name.
func (e *Effective) ListenerConfigs() map[string]config.Listener {
	out := make(map[string]config.Listener, len(e.Listeners))
	for _, l := range e.Listeners {
		out[l.Name] = l.Config
	}

	return out
}

// RouteConfigs returns the routes in effect, in the order they are tried.
func (e *Effective) RouteConfigs() []config.Route {
	out := make([]config.Route, len(e.Routes))
	for i, r := range e.Routes {
		out[i] = r.Config
	}

	return out
}

// Listener returns the listener in effect named name.
func (e *Effective) Listener(name string) (EffectiveListener, bool) {
	i := slices.IndexFunc(e.Listeners, func(l EffectiveListener) bool { return l.Name == name })
	if i < 0 {
		return EffectiveListener{}, false
	}

	return e.Listeners[i], true
}

// mergeListeners adds the file listeners and the runtime ones in effect,
// and returns the names of the runtime ones.
func (e *Effective) mergeListeners(ctx context.Context, file *config.Config, runtime []Listener) []string {
	for _, name := range slices.Sorted(maps.Keys(file.Listeners)) {
		e.Listeners = append(e.Listeners, EffectiveListener{Name: name, Config: file.Listeners[name], Layer: LayerFile})
	}

	var names []string

	for _, l := range runtime {
		if _, clash := file.Listeners[l.Name]; clash {
			e.Shadowed = append(e.Shadowed, Skipped{Kind: KindListener, Name: l.Name, Reason: reasonShadowed})

			continue
		}

		if !l.Enabled {
			e.Disabled = append(e.Disabled, Skipped{Kind: KindListener, Name: l.Name, Reason: reasonDisabled})

			continue
		}

		if err := file.CheckRuntimeListener(ctx, l.Name, l.Config); err != nil {
			e.Invalid = append(e.Invalid, Skipped{Kind: KindListener, Name: l.Name, Reason: err.Error()})

			continue
		}

		e.Listeners = append(e.Listeners, EffectiveListener{Name: l.Name, Config: l.Config, Layer: LayerRuntime})
		names = append(names, l.Name)
	}

	slices.SortFunc(e.Listeners, func(a, b EffectiveListener) int { return cmp.Compare(a.Name, b.Name) })

	return names
}

// mergeRoutes adds the file routes and the runtime ones in effect, in the
// order they are tried. Runtime routes get the global budgets they leave
// unset, as file routes do when the file is loaded.
func (e *Effective) mergeRoutes(ctx context.Context, file *config.Config, runtime []Route, runtimeListeners []string) {
	for _, r := range file.Routes {
		e.Routes = append(e.Routes, EffectiveRoute{Config: r, Layer: LayerFile})
	}

	for _, r := range runtime {
		name := r.Config.Name

		switch {
		case slices.ContainsFunc(file.Routes, func(f config.Route) bool { return f.Name == name }):
			e.Shadowed = append(e.Shadowed, Skipped{Kind: KindRoute, Name: name, Reason: reasonShadowed})
		case !r.Enabled:
			e.Disabled = append(e.Disabled, Skipped{Kind: KindRoute, Name: name, Reason: reasonDisabled})
		default:
			if err := file.CheckRuntimeRoute(ctx, r.Config, runtimeListeners); err != nil {
				e.Invalid = append(e.Invalid, Skipped{Kind: KindRoute, Name: name, Reason: err.Error()})

				continue
			}

			e.Routes = append(e.Routes, EffectiveRoute{Config: withGlobalBudgets(r.Config, file.Budgets.PerInvestigation), Layer: LayerRuntime})
		}
	}

	slices.SortStableFunc(e.Routes, compareRoutes)
}

// compareRoutes orders routes by priority, lower first, then file before
// runtime, then by name, with the default route last.
func compareRoutes(a, b EffectiveRoute) int {
	aDefault, bDefault := a.Config.Name == config.DefaultRouteName, b.Config.Name == config.DefaultRouteName
	if aDefault != bDefault {
		if aDefault {
			return 1
		}

		return -1
	}

	if c := cmp.Compare(a.Config.Priority, b.Config.Priority); c != 0 {
		return c
	}

	if a.Layer != b.Layer {
		if a.Layer == LayerFile {
			return -1
		}

		return 1
	}

	return cmp.Compare(a.Config.Name, b.Config.Name)
}

// withGlobalBudgets fills the limits r leaves unset with the global ones.
func withGlobalBudgets(r config.Route, global config.Limits) config.Route {
	if r.Budgets.MaxToolCalls == 0 {
		r.Budgets.MaxToolCalls = global.MaxToolCalls
	}

	if r.Budgets.MaxWallTime == 0 {
		r.Budgets.MaxWallTime = global.MaxWallTime
	}

	return r
}
