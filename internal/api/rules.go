package api

import (
	"context"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// RuleStore reads and changes the runtime listeners and routes.
// *rules.Store is one. Every change is checked, audited and announced to
// the other replicas by the store, in one transaction.
type RuleStore interface {
	RuntimeRules

	AddListener(ctx context.Context, actor rules.Actor, name string, l config.Listener) error
	UpdateListener(ctx context.Context, actor rules.Actor, name string, version int, l config.Listener, enabled bool) (rules.Listener, error)
	RemoveListener(ctx context.Context, actor rules.Actor, name string) (shadowed bool, err error)
	CreateRoute(ctx context.Context, actor rules.Actor, r config.Route) (rules.RouteChange, error)
	ReplaceRoute(ctx context.Context, actor rules.Actor, version int, r config.Route, enabled bool) (rules.RouteChange, error)
	DeleteRoute(ctx context.Context, actor rules.Actor, name string) (shadowed bool, err error)
}

// ListListeners returns the listeners in effect and the runtime ones that
// are not.
func (s *server) ListListeners(ctx context.Context, _ apigen.ListListenersRequestObject) (apigen.ListListenersResponseObject, error) {
	listeners, err := s.listeners(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the listeners: %w", err)
	}

	return apigen.ListListeners200JSONResponse{Listeners: listeners}, nil
}

// CreateListener saves a new runtime listener.
func (s *server) CreateListener(ctx context.Context, req apigen.CreateListenerRequestObject) (apigen.CreateListenerResponseObject, error) {
	body := req.Body

	spec := apigen.ListenerSpec{Channel: body.Channel, Source: body.Source, Match: body.Match, Extract: body.Extract, Recovery: body.Recovery}

	if err := s.rules.AddListener(ctx, actorOf(ctx), body.Name, listenerOf(spec)); err != nil {
		return nil, ruleError(err, rules.KindListener, body.Name)
	}

	saved, err := s.runtimeListener(ctx, body.Name)
	if err != nil {
		return nil, fmt.Errorf("read the new listener: %w", err)
	}

	return apigen.CreateListener201JSONResponse(saved), nil
}

// UpdateListener replaces a runtime listener.
func (s *server) UpdateListener(ctx context.Context, req apigen.UpdateListenerRequestObject) (apigen.UpdateListenerResponseObject, error) {
	body := req.Body

	spec := apigen.ListenerSpec{Channel: body.Channel, Source: body.Source, Match: body.Match, Extract: body.Extract, Recovery: body.Recovery}

	if _, err := s.rules.UpdateListener(ctx, actorOf(ctx), req.Name, body.Version, listenerOf(spec), body.Enabled); err != nil {
		return nil, ruleError(err, rules.KindListener, req.Name)
	}

	saved, err := s.runtimeListener(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("read the updated listener: %w", err)
	}

	return apigen.UpdateListener200JSONResponse(saved), nil
}

// DeleteListener deletes a runtime listener, which may be one a file
// listener shadows.
func (s *server) DeleteListener(ctx context.Context, req apigen.DeleteListenerRequestObject) (apigen.DeleteListenerResponseObject, error) {
	if _, err := s.rules.RemoveListener(ctx, actorOf(ctx), req.Name); err != nil {
		return nil, ruleError(err, rules.KindListener, req.Name)
	}

	return apigen.DeleteListener204Response{}, nil
}

// ListRoutes returns the routes in effect, in the order they are tried,
// and the runtime ones that are not in effect.
func (s *server) ListRoutes(ctx context.Context, _ apigen.ListRoutesRequestObject) (apigen.ListRoutesResponseObject, error) {
	routes, err := s.routes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the routes: %w", err)
	}

	return apigen.ListRoutes200JSONResponse{Routes: routes}, nil
}

// CreateRoute saves a new runtime route.
func (s *server) CreateRoute(ctx context.Context, req apigen.CreateRouteRequestObject) (apigen.CreateRouteResponseObject, error) {
	body := req.Body

	spec := apigen.RouteSpec{
		Priority: body.Priority, Match: body.Match, Strategy: body.Strategy, Sources: body.Sources, Outputs: body.Outputs,
		Mode: body.Mode, Budgets: body.Budgets, Grouping: body.Grouping,
	}

	route, err := routeOf(body.Name, spec)
	if err != nil {
		return nil, fmt.Errorf("read the route body: %w", err)
	}

	if _, err := s.rules.CreateRoute(ctx, actorOf(ctx), route); err != nil {
		return nil, ruleError(err, rules.KindRoute, body.Name)
	}

	saved, err := s.runtimeRoute(ctx, body.Name)
	if err != nil {
		return nil, fmt.Errorf("read the new route: %w", err)
	}

	return apigen.CreateRoute201JSONResponse(saved), nil
}

// UpdateRoute replaces a runtime route.
func (s *server) UpdateRoute(ctx context.Context, req apigen.UpdateRouteRequestObject) (apigen.UpdateRouteResponseObject, error) {
	body := req.Body

	spec := apigen.RouteSpec{
		Priority: body.Priority, Match: body.Match, Strategy: body.Strategy, Sources: body.Sources, Outputs: body.Outputs,
		Mode: body.Mode, Budgets: body.Budgets, Grouping: body.Grouping,
	}

	route, err := routeOf(req.Name, spec)
	if err != nil {
		return nil, fmt.Errorf("read the route body: %w", err)
	}

	if _, err := s.rules.ReplaceRoute(ctx, actorOf(ctx), body.Version, route, body.Enabled); err != nil {
		return nil, ruleError(err, rules.KindRoute, req.Name)
	}

	saved, err := s.runtimeRoute(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("read the updated route: %w", err)
	}

	return apigen.UpdateRoute200JSONResponse(saved), nil
}

// DeleteRoute deletes a runtime route, which may be one a file route
// shadows.
func (s *server) DeleteRoute(ctx context.Context, req apigen.DeleteRouteRequestObject) (apigen.DeleteRouteResponseObject, error) {
	if _, err := s.rules.DeleteRoute(ctx, actorOf(ctx), req.Name); err != nil {
		return nil, ruleError(err, rules.KindRoute, req.Name)
	}

	return apigen.DeleteRoute204Response{}, nil
}

// listeners returns every listener: those in effect in name order, then
// the runtime ones that are not in effect.
func (s *server) listeners(ctx context.Context) ([]apigen.Listener, error) {
	rt, effective, err := s.currentRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the rules for the listeners: %w", err)
	}

	out := make([]apigen.Listener, 0, len(effective.Listeners)+len(rt.Listeners))

	for _, l := range effective.Listeners {
		item := listenerView(l.Name, l.Config)
		item.Layer, item.Status, item.Enabled = apigen.RuleLayer(l.Layer), apigen.Active, true

		if l.Layer == rules.LayerFile {
			item.ReadOnly = true
		} else if i := slices.IndexFunc(rt.Listeners, func(r rules.Listener) bool { return r.Name == l.Name }); i >= 0 {
			item = withListenerStore(item, rt.Listeners[i])
		}

		out = append(out, item)
	}

	for _, l := range rt.Listeners {
		if effective.Layer(rules.KindListener, l.Name) == rules.LayerRuntime {
			continue
		}

		item := withListenerStore(listenerView(l.Name, l.Config), l)
		item.Layer = apigen.Runtime
		item.Status, item.StatusReason = skippedStatus(effective, rules.KindListener, l.Name)
		out = append(out, item)
	}

	return out, nil
}

// routes returns every route: those in effect in the order they are
// tried, then the runtime ones that are not in effect.
func (s *server) routes(ctx context.Context) ([]apigen.Route, error) {
	rt, effective, err := s.currentRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the rules for the routes: %w", err)
	}

	out := make([]apigen.Route, 0, len(effective.Routes)+len(rt.Routes))

	for i, r := range effective.Routes {
		item := routeView(r.Config)
		item.Layer, item.Status, item.Enabled, item.Position = apigen.RuleLayer(r.Layer), apigen.Active, true, new(i+1)

		if r.Layer == rules.LayerFile {
			item.ReadOnly = true
		} else if j := slices.IndexFunc(rt.Routes, func(x rules.Route) bool { return x.Config.Name == r.Config.Name }); j >= 0 {
			item = withRouteStore(routeView(rt.Routes[j].Config), rt.Routes[j], item)
		}

		out = append(out, item)
	}

	for _, r := range rt.Routes {
		if effective.Layer(rules.KindRoute, r.Config.Name) == rules.LayerRuntime {
			continue
		}

		item := withRouteStore(routeView(r.Config), r, apigen.Route{})
		item.Layer = apigen.Runtime
		item.Status, item.StatusReason = skippedStatus(effective, rules.KindRoute, r.Config.Name)
		out = append(out, item)
	}

	return out, nil
}

// runtimeListener returns the runtime listener named name as it is
// stored now.
func (s *server) runtimeListener(ctx context.Context, name string) (apigen.Listener, error) {
	all, err := s.listeners(ctx)
	if err != nil {
		return apigen.Listener{}, fmt.Errorf("list the listeners: %w", err)
	}

	i := slices.IndexFunc(all, func(l apigen.Listener) bool { return l.Name == name && l.Layer == apigen.Runtime })
	if i < 0 {
		return apigen.Listener{}, fmt.Errorf("listener %s is gone after it was saved", name)
	}

	return all[i], nil
}

// runtimeRoute returns the runtime route named name as it is stored now.
func (s *server) runtimeRoute(ctx context.Context, name string) (apigen.Route, error) {
	all, err := s.routes(ctx)
	if err != nil {
		return apigen.Route{}, fmt.Errorf("list the routes: %w", err)
	}

	i := slices.IndexFunc(all, func(r apigen.Route) bool { return r.Name == name && r.Layer == apigen.Runtime })
	if i < 0 {
		return apigen.Route{}, fmt.Errorf("route %s is gone after it was saved", name)
	}

	return all[i], nil
}

// currentRules reads the runtime rules and merges them with the file's, so
// an answer shows a change at once, without waiting for the cache of the
// replica to reload.
func (s *server) currentRules(ctx context.Context) (rules.Runtime, *rules.Effective, error) {
	rt, err := s.rules.Load(ctx)
	if err != nil {
		return rules.Runtime{}, nil, fmt.Errorf("read the runtime rules: %w", err)
	}

	return rt, rules.Merge(ctx, s.cfg, rt), nil
}

// actorOf is who makes the change that ctx's request asks for.
func actorOf(ctx context.Context) rules.Actor {
	p, _ := auth.PrincipalFrom(ctx)

	return rules.Actor{Kind: actorKindOf(p), ID: p.ID.String()}
}
