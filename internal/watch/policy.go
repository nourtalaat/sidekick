package watch

import (
	"fmt"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// Global limits of watchers, used when watch sets none.
const (
	DefaultMaxPerGroup = 3
	DefaultMaxTotal    = 100
)

// Policy is what the config allows watchers: the counts, the global
// defaults and bounds, and each route's limits. Build it from the config
// once at start-up.
type Policy struct {
	// MaxPerGroup is the most watchers one group may have.
	MaxPerGroup int
	// MaxTotal is the most watchers that run at once in the deployment.
	MaxTotal int
	// Defaults are the global defaults, at watch.defaults.
	Defaults Level
	// Bounds are the global bounds.
	Bounds Bounds
	// Routes are the limits of each route, by name.
	Routes map[string]RoutePolicy
}

// RoutePolicy is what one route allows its watchers.
type RoutePolicy struct {
	// Path is the route's config path, such as routes[0].
	Path string
	// AllowModel says whether investigations of the route may call
	// start_watcher.
	AllowModel bool
	// Types are the check types start_watcher may use.
	Types []Type
	// Tools are the qualified tool names tool checks of start_watcher may
	// call.
	Tools []string
	// Hosts are the host patterns HTTP checks of start_watcher may reach,
	// inside the global watch.http.allowHosts.
	Hosts []string
	// Defaults are the route's watcherDefaults.
	Defaults Level
	// Bounds are the route's bounds, inside the global ones.
	Bounds Bounds
	// Definitions are the route's watchers and its strategy's, which start
	// when a group of the route opens.
	Definitions []Definition
}

// Route returns the policy of the route with name, and whether it exists.
func (p Policy) Route(name string) (RoutePolicy, bool) {
	r, ok := p.Routes[name]

	return r, ok
}

// Resolve resolves a watcher's own settings and its strategy's defaults
// with the route's and the global defaults, inside the route's and the
// global bounds.
func (p Policy) Resolve(r RoutePolicy, own, strategyDefaults Level) (Settings, []Problem) {
	s, problems := Resolve([]Level{own, strategyDefaults, r.Defaults, p.Defaults}, []Bounds{r.Bounds, p.Bounds})

	return Complete(s), problems
}

// hostProblem returns why the route's hosts do not take an HTTP check's
// URL, naming the limit, or "" when they do or the check is not an HTTP
// check.
func (r RoutePolicy) hostProblem(check Check) string {
	h, ok := check.(HTTPCheck)
	if !ok {
		return ""
	}

	u, err := url.Parse(h.URL)
	if err != nil {
		return "url: the URL does not parse"
	}

	if !safehttp.MatchesHost(r.Hosts, u.Hostname()) {
		return fmt.Sprintf("url: host %q is not allowed; %s.watcherLimits.hosts allows %v", u.Hostname(), r.Path, r.Hosts)
	}

	return ""
}
