package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/watch"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// Config paths of watcher settings.
const (
	// watchPath is the path of the global watcher settings.
	watchPath = "watch"
	// strategyDefaultsPath is the path of a strategy's defaults inside
	// its file.
	strategyDefaultsPath = "watcherDefaults"
)

// allTypes are the check types a route allows when watcherLimits.types is
// empty.
var allTypes = []watch.Type{watch.TypeHTTP, watch.TypeTool, watch.TypeAlert}

// WatchPolicy returns what the config allows watchers, with each route's
// limits and the watchers that start with its groups. Call it on a config
// returned by Load or Parse.
func (c *Config) WatchPolicy() watch.Policy {
	p := watch.Policy{
		MaxPerGroup: c.Watch.MaxPerGroup,
		MaxTotal:    c.Watch.MaxTotal,
		Defaults:    watch.Level{Path: watchPath + ".defaults", Settings: c.Watch.Defaults},
		Bounds:      c.Watch.bounds(),
		Routes:      make(map[string]watch.RoutePolicy, len(c.Routes)),
	}

	for i, r := range c.Routes {
		w := watcherChecker{cfg: c, errs: &errorList{}, seen: map[string]bool{}}
		p.Routes[r.Name] = w.routePolicy(fmt.Sprintf("routes[%d]", i), r, c.routeStrategy(r))
	}

	return p
}

// routeStrategy returns the strategy a route names, or nil when the
// strategies are not loaded or do not have it.
func (c *Config) routeStrategy(r Route) *strategy.Strategy {
	return strategyOf(c.strategySet, r)
}

// bounds returns the global bounds.
func (w Watch) bounds() watch.Bounds {
	return watch.Bounds{Path: watchPath, MinInterval: w.MinInterval, MaxInterval: w.MaxInterval, MaxTimeout: w.MaxTimeout}
}

// checkWatchSettings checks the global watcher counts, bounds and
// defaults, and the watcher settings of every route.
func (v *validator) checkWatchSettings() {
	w := v.cfg.Watch
	v.positive(watchPath+".maxPerGroup", w.MaxPerGroup)
	v.positive(watchPath+".maxTotal", w.MaxTotal)
	v.positiveDuration(watchPath+".minInterval", w.MinInterval)
	v.positiveDuration(watchPath+".maxInterval", w.MaxInterval)
	v.positiveDuration(watchPath+".maxTimeout", w.MaxTimeout)

	if w.MinInterval > 0 && w.MaxInterval > 0 && w.MinInterval > w.MaxInterval {
		v.errs.add(watchPath+".minInterval", "%s is above %s.maxInterval %s", w.MinInterval, watchPath, w.MaxInterval)
	}

	d := w.Defaults
	v.positiveDuration(watchPath+".defaults.interval", d.Interval)
	v.positiveDuration(watchPath+".defaults.timeout", d.Timeout)
	v.positive(watchPath+".defaults.passesToRecover", d.PassesToRecover)
	v.positiveDuration(watchPath+".defaults.checkTimeout", d.CheckTimeout)

	checker := watcherChecker{cfg: v.cfg, errs: v.errs, seen: map[string]bool{}}

	_, problems := watch.Resolve([]watch.Level{{Path: watchPath + ".defaults", Settings: d}}, []watch.Bounds{w.bounds()})
	checker.add(problems)

	for i, r := range v.cfg.Routes {
		checker.routePolicy(fmt.Sprintf("routes[%d]", i), r, strategyOf(v.strategies, r))
	}
}

// watcherChecker checks the watcher settings of routes and builds their
// policies. Problems go to errs once each.
type watcherChecker struct {
	cfg  *Config
	errs *errorList
	seen map[string]bool
}

// routePolicy checks the watcher settings of the route r at path and
// returns its policy. st is the route's strategy, or nil.
func (w watcherChecker) routePolicy(path string, r Route, st *strategy.Strategy) watch.RoutePolicy {
	limitsPath := path + ".watcherLimits"
	l := r.WatcherLimits
	global := w.cfg.Watch

	p := watch.RoutePolicy{
		Path:       path,
		AllowModel: global.AllowModel && (l.AllowModel == nil || *l.AllowModel),
		Types:      l.Types,
		Tools:      l.Tools,
		Hosts:      l.Hosts,
		Defaults:   watch.Level{Path: path + ".watcherDefaults", Settings: r.WatcherDefaults},
		Bounds:     watch.Bounds{Path: limitsPath, MinInterval: l.MinInterval, MaxInterval: l.MaxInterval, MaxTimeout: l.MaxTimeout},
	}

	if len(p.Types) == 0 {
		p.Types = allTypes
	}

	if len(p.Hosts) == 0 {
		p.Hosts = global.HTTP.AllowHosts
	}

	w.checkLimits(limitsPath, r)
	w.add(watch.Within(p.Bounds, global.bounds()))

	globalDefaults := watch.Level{Path: watchPath + ".defaults", Settings: global.Defaults}
	bounds := []watch.Bounds{p.Bounds, global.bounds()}

	_, problems := watch.Resolve([]watch.Level{p.Defaults, globalDefaults}, bounds)
	w.add(problems)

	names := map[string]string{}

	for j, def := range r.Watchers {
		at := fmt.Sprintf("%s.watchers[%d]", path, j)
		w.checkName(at, def.Name, names)

		levels := []watch.Level{{Path: at, Settings: def.WatcherSettings}, p.Defaults, globalDefaults}
		if d, ok := w.definition(at, def, watch.OriginRoute, levels, bounds, r); ok {
			p.Definitions = append(p.Definitions, d)
		}
	}

	if st == nil {
		return p
	}

	p.Definitions = append(p.Definitions, w.strategyDefinitions(path, r, st, p, bounds)...)

	return p
}

// strategyDefinitions checks the watchers of the route's strategy with the
// route's defaults and bounds, and returns them. Problems are reported at
// the route's strategy field.
func (w watcherChecker) strategyDefinitions(path string, r Route, st *strategy.Strategy, p watch.RoutePolicy, bounds []watch.Bounds) []watch.Definition {
	at := path + ".strategy"
	sub := watcherChecker{cfg: w.cfg, errs: &errorList{}, seen: map[string]bool{}}

	var defaults strategy.WatcherSettings
	if st.WatcherDefaults != nil {
		defaults = *st.WatcherDefaults
	}

	globalDefaults := watch.Level{Path: watchPath + ".defaults", Settings: w.cfg.Watch.Defaults}
	strategyDefaults := watch.Level{Path: strategyDefaultsPath, Settings: defaults}

	var out []watch.Definition

	for j, def := range st.Watchers {
		levels := []watch.Level{{Path: fmt.Sprintf("watchers[%d]", j), Settings: def.WatcherSettings}, strategyDefaults, p.Defaults, globalDefaults}
		if d, ok := sub.definition(fmt.Sprintf("watchers[%d]", j), def, watch.OriginStrategy, levels, bounds, r); ok {
			out = append(out, d)
		}
	}

	for _, e := range sub.errs.items {
		if strings.HasPrefix(e.Path, "watchers[") || strings.HasPrefix(e.Path, strategyDefaultsPath) {
			w.addOnce(at, fmt.Sprintf("strategy %q %s", st.Name, e.String()))
		}
	}

	return out
}

// definition checks one watcher definition at path and returns it with
// its settings resolved. It reports false when the definition has a
// problem.
func (w watcherChecker) definition(at string, def strategy.Watcher, origin watch.Origin, levels []watch.Level, bounds []watch.Bounds, r Route) (watch.Definition, bool) {
	before := len(w.errs.items)

	match, err := watch.MatchOf(def.Match)
	if err != nil {
		w.addOnce(at+".match", err.Error())
	}

	check, err := watch.CheckOf(def.Check)
	if err != nil {
		w.addOnce(at+".check", err.Error())
	} else {
		w.checkCheck(at+".check", check, r)
	}

	settings, problems := watch.Resolve(levels, bounds)

	for _, p := range problems {
		if strings.HasPrefix(p.Path, at+".") || strings.HasPrefix(p.Path, strategyDefaultsPath+".") {
			w.addOnce(p.Path, p.Message)
		}
	}

	if len(w.errs.items) > before {
		return watch.Definition{}, false
	}

	return watch.Definition{Name: def.Name, Origin: origin, Match: match, Check: check, Settings: watch.Complete(settings)}, true
}

// checkCheck checks a check's own settings and that the deployment can
// run it: an HTTP check's host is in watch.http.allowHosts, and a tool
// check's source is one of the route's.
func (w watcherChecker) checkCheck(at string, check watch.Check, r Route) {
	if err := check.Validate(); err != nil {
		for line := range strings.SplitSeq(err.Error(), "\n") {
			field, message, ok := strings.Cut(line, ": ")
			if !ok {
				w.addOnce(at, line)

				continue
			}

			w.addOnce(at+"."+field, message)
		}
	}

	switch c := check.(type) {
	case watch.HTTPCheck:
		u, err := url.Parse(c.URL)
		if err == nil && u.Hostname() != "" && !safehttp.MatchesHost(w.cfg.Watch.HTTP.AllowHosts, u.Hostname()) {
			w.addOnce(at+".url", fmt.Sprintf("host %q is not in watch.http.allowHosts", u.Hostname()))
		}
	case watch.ToolCheck:
		if c.Source != "" && !routeHasSource(w.cfg, r, c.Source) {
			w.addOnce(at+".source", fmt.Sprintf("source %q is not one of the route's sources", c.Source))
		}
	}
}

// checkLimits checks a route's watcherLimits: known types, tools of the
// route's sources, and hosts inside watch.http.allowHosts.
func (w watcherChecker) checkLimits(at string, r Route) {
	l := r.WatcherLimits

	for i, t := range l.Types {
		if !slices.Contains(allTypes, t) {
			w.addOnce(fmt.Sprintf("%s.types[%d]", at, i), fmt.Sprintf("unknown check type %q; expected one of %v", t, allTypes))
		}
	}

	for i, name := range l.Tools {
		src, _, ok := strings.Cut(name, source.Separator)
		if !ok || !routeHasSource(w.cfg, r, src) {
			w.addOnce(fmt.Sprintf("%s.tools[%d]", at, i), fmt.Sprintf("%q is not the qualified name of a tool of the route's sources, such as signoz%squery_metrics", name, source.Separator))
		}
	}

	for i, h := range l.Hosts {
		path := fmt.Sprintf("%s.hosts[%d]", at, i)

		if err := safehttp.CheckHostPattern(h); err != nil {
			w.addOnce(path, err.Error())

			continue
		}

		if !safehttp.Covers(w.cfg.Watch.HTTP.AllowHosts, h) {
			w.addOnce(path, fmt.Sprintf("host pattern %q is not inside watch.http.allowHosts", h))
		}
	}
}

// checkName checks the name of a route watcher.
func (w watcherChecker) checkName(at, name string, seen map[string]string) {
	switch {
	case name == "":
		w.addOnce(at+".name", "required")
	case !watch.NamePattern.MatchString(name):
		w.addOnce(at+".name", fmt.Sprintf("watcher name %q %s", name, watch.NameRule))
	case seen[name] != "":
		w.addOnce(at+".name", fmt.Sprintf("watcher %q is already defined at %s", name, seen[name]))
	default:
		seen[name] = at
	}
}

// add records problems once each.
func (w watcherChecker) add(problems []watch.Problem) {
	for _, p := range problems {
		w.addOnce(p.Path, p.Message)
	}
}

// addOnce records a problem unless the same one is recorded already.
func (w watcherChecker) addOnce(path, message string) {
	key := path + "\x00" + message
	if w.seen[key] {
		return
	}

	w.seen[key] = true
	w.errs.add(path, "%s", message)
}

// strategyOf returns the strategy a route names in set, or nil.
func strategyOf(set *strategy.Set, r Route) *strategy.Strategy {
	if set == nil {
		return nil
	}

	st, ok := set.Get(r.Strategy)
	if !ok {
		return nil
	}

	return &st
}

// routeHasSource reports whether name is a configured source the route
// may use: one of its sources, or any source when it lists none.
func routeHasSource(cfg *Config, r Route, name string) bool {
	if _, ok := cfg.Sources[name]; !ok {
		return false
	}

	return len(r.Sources) == 0 || slices.Contains(r.Sources, name)
}
