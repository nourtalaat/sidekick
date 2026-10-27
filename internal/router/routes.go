package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// errNoStrategies means the config was not loaded with its strategies.
var errNoStrategies = errors.New("the config has no strategies loaded")

// route is one configured route with the settings its investigations get,
// worked out once when the router is made.
type route struct {
	name    string
	matcher matcher
	mode    action.Mode
	// strategy and strategyVersion name the exact strategy text.
	strategy        string
	strategyVersion string
	// connectors and budget are the JSON stored with each investigation.
	connectors []byte
	budget     []byte
	// outputs are the sinks the investigation's updates go to.
	outputs []string
	// rateLimit is the route's own rate limit, nil for none.
	rateLimit *config.RateLimit
	// grouping decides which group the route's events join.
	grouping grouper.Route
}

// newRoutes prepares the routes of cfg in the order they are tried.
// modelSettings are the shared settings of each built connector, for the
// model IDs.
func newRoutes(cfg *config.Config, modelSettings map[string]model.Settings) ([]route, error) {
	strategies := cfg.StrategySet()
	if strategies == nil {
		return nil, errNoStrategies
	}

	ordered := cfg.RoutesInOrder()
	routes := make([]route, 0, len(ordered))

	for _, rc := range ordered {
		r, err := newRoute(rc, cfg.Model.Roles, strategies, modelSettings)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", rc.Name, err)
		}

		routes = append(routes, r)
	}

	return routes, nil
}

// newRoute prepares one route. globalRoles are model.roles, which the
// route's own roles override.
func newRoute(rc config.Route, globalRoles map[model.Role]string, strategies *strategy.Set, modelSettings map[string]model.Settings) (route, error) {
	m, err := newMatcher(rc.Match)
	if err != nil {
		return route{}, fmt.Errorf("match: %w", err)
	}

	name := rc.Strategy
	if name == "" {
		name = config.DefaultStrategy
	}

	st, ok := strategies.Get(name)
	if !ok {
		return route{}, fmt.Errorf("strategy %q is not loaded", name)
	}

	roles := maps.Clone(globalRoles)
	if roles == nil {
		roles = map[model.Role]string{}
	}

	maps.Copy(roles, rc.Roles)

	connectors := make(investigator.Connectors, len(roles))
	for role, connector := range roles {
		connectors[role] = investigator.RoleConnector{Connector: connector, Model: modelSettings[connector].Model}
	}

	connectorsJSON, err := json.Marshal(connectors)
	if err != nil {
		return route{}, fmt.Errorf("encode the connectors: %w", err)
	}

	budgetJSON, err := json.Marshal(effectiveBudget(rc.Budgets.Limits, st.Budgets))
	if err != nil {
		return route{}, fmt.Errorf("encode the budget: %w", err)
	}

	return route{
		name:            rc.Name,
		matcher:         m,
		mode:            rc.Mode,
		strategy:        st.Name,
		strategyVersion: st.Version,
		connectors:      connectorsJSON,
		budget:          budgetJSON,
		outputs:         rc.Outputs,
		rateLimit:       rc.RateLimit,
		grouping: grouper.Route{
			Name:              rc.Name,
			Window:            rc.Grouping.Window,
			FingerprintLabels: rc.Grouping.FingerprintLabels,
			RelatedWindow:     rc.Grouping.RelatedWindow,
		},
	}, nil
}

// pick returns the first route that matches c. The default route is last
// and matches every event, so pick fails only on a config without one.
func pick(routes []route, c candidate) (route, bool) {
	for _, r := range routes {
		if r.matcher.matches(c) {
			return r, true
		}
	}

	return route{}, false
}

// effectiveBudget returns the lower of the route's and the strategy's limit
// for each budget. Zero means the strategy sets no limit; after the config
// is loaded, the route always has one.
func effectiveBudget(r config.Limits, s strategy.Budgets) investigator.Budget {
	return investigator.Budget{
		MaxTokens:     int64(lower(r.MaxTokens, s.MaxTokens)),
		MaxToolCalls:  lower(r.MaxToolCalls, s.MaxToolCalls),
		MaxWallTime:   lower(r.MaxWallTime, s.MaxWallTime),
		MaxCostMicros: lower(r.MaxCostMicros(), config.Limits{MaxCostUSD: s.MaxCostUSD}.MaxCostMicros()),
	}
}

// lower returns the lower of two limits, where zero means no limit.
func lower[T int | int64 | time.Duration](a, b T) T {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	default:
		return min(a, b)
	}
}
