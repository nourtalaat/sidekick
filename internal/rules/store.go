package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The audit actions of rule changes.
const (
	ActionListenerCreated = "listener.created"
	ActionListenerUpdated = "listener.updated"
	ActionListenerDeleted = "listener.deleted"
	ActionRouteCreated    = "route.created"
	ActionRouteUpdated    = "route.updated"
	ActionRouteDeleted    = "route.deleted"
)

// uniqueViolation is the Postgres error code of a duplicate key.
const uniqueViolation = "23505"

var (
	// ErrExists means a runtime rule with that name exists already.
	ErrExists = errors.New("a runtime rule with that name exists")
	// ErrNotFound means no runtime rule has that name.
	ErrNotFound = errors.New("no runtime rule has that name")
	// ErrFileRule means the rule is defined in the config file, which
	// runtime changes cannot alter.
	ErrFileRule = errors.New("the rule is defined in the config file")
	// ErrConflict means another change to the same rule was saved at the
	// same moment. Trying again works.
	ErrConflict = errors.New("the rule was changed at the same moment by someone else")
	// ErrStale means the rule was changed since the version the caller
	// read, so the caller's copy is out of date.
	ErrStale = errors.New("the rule was changed since it was read")
)

// Actor is who makes a change. The audit log records it, and the rule's
// updated_by holds its ID.
type Actor struct {
	// Kind is audit.ActorUser or audit.ActorToken.
	Kind string
	// ID is a Slack user ID, or the ID of the web UI user or API token.
	ID string
}

// SlackUser returns the Actor for the Slack user with id.
func SlackUser(id string) Actor {
	return Actor{Kind: audit.ActorUser, ID: id}
}

// RouteChange is the result of SetRoute.
type RouteChange struct {
	Old, New Route
	// Created is true when the route did not exist before.
	Created bool
	// Changed is false when the change left the route as it was, in which
	// case nothing was written.
	Changed bool
}

// Store saves runtime rules. Each change is checked against the file
// config, and written in one transaction with its audit entry and the
// rules_changed notification. It is safe for concurrent use.
type Store struct {
	pool  *pgxpool.Pool
	file  *config.Config
	audit *audit.Writer
	now   func() time.Time
}

// NewStore returns a Store on pool that checks changes against file, the
// loaded config.
func NewStore(pool *pgxpool.Pool, file *config.Config) *Store {
	return &Store{pool: pool, file: file, audit: audit.NewWriter(), now: time.Now}
}

// Load returns every stored runtime rule.
func (s *Store) Load(ctx context.Context) (Runtime, error) {
	rt, err := load(ctx, s.pool)
	if err != nil {
		return Runtime{}, fmt.Errorf("load runtime rules: %w", err)
	}

	return rt, nil
}

// AddListener saves a new runtime listener for actor. The error wraps
// config.Errors when the listener fails the checks, ErrFileRule when the
// file has a listener of that name, and ErrExists when a runtime one has
// it.
func (s *Store) AddListener(ctx context.Context, actor Actor, name string, l config.Listener) error {
	if _, ok := s.file.Listeners[name]; ok {
		return fmt.Errorf("add listener %s: %w", name, ErrFileRule)
	}

	if err := s.file.CheckRuntimeListener(ctx, name, l); err != nil {
		return fmt.Errorf("add listener %s: %w", name, err)
	}

	added := Listener{Name: name, Config: l, Enabled: true}
	added.Config.Trusted = false

	match, err := json.Marshal(matchOfListener(added.Config))
	if err != nil {
		return fmt.Errorf("encode listener %s: %w", name, err)
	}

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)

		if _, err := q.LockListener(ctx, name); err == nil {
			return ErrExists
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read listener: %w", err)
		}

		err := q.InsertListener(ctx, db.InsertListenerParams{
			Name: name, SlackChannel: l.Channel, Match: match, UpdatedBy: optional(actor.ID), UpdatedAt: s.now(),
		})
		if err != nil {
			return fmt.Errorf("insert listener: %w", err)
		}

		return s.record(ctx, tx, actor, ActionListenerCreated, KindListener, name, nil, added.view())
	})
	if err != nil {
		return fmt.Errorf("add listener %s: %w", name, err)
	}

	return nil
}

// UpdateListener replaces the channel, match, extract and recovery of the
// runtime listener named name, and turns it on or off, for actor. version
// is the version the caller read; a different stored one means someone
// changed the listener since, and the error wraps ErrStale. The error
// wraps config.Errors when the listener fails the checks, ErrFileRule when
// the file has a listener of that name, and ErrNotFound when no runtime
// listener has it. A change that leaves the listener as it was writes
// nothing. The result is the listener as stored after the call.
func (s *Store) UpdateListener(ctx context.Context, actor Actor, name string, version int, l config.Listener, enabled bool) (Listener, error) {
	if _, ok := s.file.Listeners[name]; ok {
		return Listener{}, fmt.Errorf("update listener %s: %w", name, ErrFileRule)
	}

	if err := s.file.CheckRuntimeListener(ctx, name, l); err != nil {
		return Listener{}, fmt.Errorf("update listener %s: %w", name, err)
	}

	var res Listener

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)

		row, err := q.LockListener(ctx, name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}

		if err != nil {
			return fmt.Errorf("read listener: %w", err)
		}

		old, err := listenerOfRow(row)
		if err != nil {
			return fmt.Errorf("read listener: %w", err)
		}

		if old.Version != version {
			return ErrStale
		}

		updated := old
		updated.Config = l
		updated.Config.Trusted = old.Config.Trusted
		updated.Enabled = enabled
		res = old

		changed, err := differ(old.view(), updated.view())
		if err != nil {
			return fmt.Errorf("compare listener: %w", err)
		}

		if !changed {
			return nil
		}

		match, err := json.Marshal(matchOfListener(updated.Config))
		if err != nil {
			return fmt.Errorf("encode listener: %w", err)
		}

		now := s.now()

		err = q.UpdateListener(ctx, db.UpdateListenerParams{
			Name: name, SlackChannel: updated.Config.Channel, Match: match, Enabled: enabled, UpdatedBy: optional(actor.ID), UpdatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("update listener: %w", err)
		}

		updated.Version, updated.UpdatedBy, updated.UpdatedAt = old.Version+1, actor.ID, now
		res = updated

		return s.record(ctx, tx, actor, ActionListenerUpdated, KindListener, name, old.view(), updated.view())
	})
	if err != nil {
		return Listener{}, fmt.Errorf("update listener %s: %w", name, err)
	}

	return res, nil
}

// RemoveListener deletes a runtime listener for actor. It also deletes a
// runtime listener that a file listener of the same name shadows, which
// nothing else can remove; shadowed says that was the case. The error
// wraps ErrFileRule for a file listener that no runtime listener
// shadows, and ErrNotFound when neither has the name.
func (s *Store) RemoveListener(ctx context.Context, actor Actor, name string) (shadowed bool, err error) {
	_, shadowed = s.file.Listeners[name]

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)

		row, err := q.LockListener(ctx, name)

		switch {
		case errors.Is(err, pgx.ErrNoRows) && shadowed:
			return ErrFileRule
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read listener: %w", err)
		}

		old, err := listenerOfRow(row)
		if err != nil {
			return fmt.Errorf("read listener: %w", err)
		}

		if _, err := q.DeleteListener(ctx, name); err != nil {
			return fmt.Errorf("delete listener: %w", err)
		}

		return s.record(ctx, tx, actor, ActionListenerDeleted, KindListener, name, old.view(), nil)
	})
	if err != nil {
		return false, fmt.Errorf("remove listener %s: %w", name, err)
	}

	return shadowed, nil
}

// SetRoute changes the runtime route named name for actor, or creates it
// from config.NewRoute when it does not exist. change edits the route; an
// error from it is returned as it is, wrapped. The result must pass the
// runtime route checks, or the error wraps config.Errors. A change that
// leaves the route as it was writes nothing.
func (s *Store) SetRoute(ctx context.Context, actor Actor, name string, change func(r *Route) error) (RouteChange, error) {
	return s.editRoute(ctx, actor, name, routeEdit{rule: routeUpsert}, change)
}

// CreateRoute saves a new runtime route for actor. The error wraps
// config.Errors when the route fails the checks, ErrFileRule when the file
// has a route of that name, and ErrExists when a runtime one has it.
func (s *Store) CreateRoute(ctx context.Context, actor Actor, r config.Route) (RouteChange, error) {
	return s.editRoute(ctx, actor, r.Name, routeEdit{rule: routeCreateOnly}, func(rt *Route) error {
		rt.Config = cloneRoute(r)

		return nil
	})
}

// ReplaceRoute replaces the runtime route named r.Name with r, and turns
// it on or off, for actor. version is the version the caller read; a
// different stored one means someone changed the route since, and the
// error wraps ErrStale. The error wraps config.Errors when the route
// fails the checks, ErrFileRule when the file has a route of that name,
// and ErrNotFound when no runtime route has it.
func (s *Store) ReplaceRoute(ctx context.Context, actor Actor, version int, r config.Route, enabled bool) (RouteChange, error) {
	return s.editRoute(ctx, actor, r.Name, routeEdit{rule: routeUpdateOnly, version: version}, func(rt *Route) error {
		rt.Config = cloneRoute(r)
		rt.Enabled = enabled

		return nil
	})
}

// DeleteRoute deletes a runtime route for actor. It also deletes a
// runtime route that a file route of the same name shadows, which nothing
// else can remove; shadowed says that was the case. The error wraps
// ErrFileRule for a file route that no runtime route shadows, and
// ErrNotFound when neither has the name.
func (s *Store) DeleteRoute(ctx context.Context, actor Actor, name string) (shadowed bool, err error) {
	shadowed = name == config.DefaultRouteName || s.fileRoute(name)

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)

		row, err := q.LockRoute(ctx, name)

		switch {
		case errors.Is(err, pgx.ErrNoRows) && shadowed:
			return ErrFileRule
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read route: %w", err)
		}

		old, err := routeOfRow(row)
		if err != nil {
			return fmt.Errorf("read route: %w", err)
		}

		if _, err := q.DeleteRoute(ctx, name); err != nil {
			return fmt.Errorf("delete route: %w", err)
		}

		return s.record(ctx, tx, actor, ActionRouteDeleted, KindRoute, name, old.view(), nil)
	})
	if err != nil {
		return false, fmt.Errorf("delete route %s: %w", name, err)
	}

	return shadowed, nil
}

// routeRule says whether editRoute may create the route, change it, or
// either.
type routeRule int

const (
	routeUpsert routeRule = iota
	routeCreateOnly
	routeUpdateOnly
)

// routeEdit says what editRoute may do.
type routeEdit struct {
	rule routeRule
	// version is the version the caller read, for routeUpdateOnly.
	version int
}

// editRoute reads the route, applies change, checks the result and saves
// it with its audit entry and notification in one transaction.
func (s *Store) editRoute(ctx context.Context, actor Actor, name string, edit routeEdit, change func(r *Route) error) (RouteChange, error) {
	if name == config.DefaultRouteName || s.fileRoute(name) {
		return RouteChange{}, fmt.Errorf("set route %s: %w", name, ErrFileRule)
	}

	var res RouteChange

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)

		row, err := q.LockRoute(ctx, name)

		switch {
		case errors.Is(err, pgx.ErrNoRows) && edit.rule == routeUpdateOnly:
			return ErrNotFound
		case errors.Is(err, pgx.ErrNoRows):
			res.Created = true
			res.Old = Route{Config: config.NewRoute(name), Enabled: true}
		case err != nil:
			return fmt.Errorf("read route: %w", err)
		case edit.rule == routeCreateOnly:
			return ErrExists
		default:
			if res.Old, err = routeOfRow(row); err != nil {
				return fmt.Errorf("read route: %w", err)
			}
		}

		if edit.rule == routeUpdateOnly && res.Old.Version != edit.version {
			return ErrStale
		}

		res.New = res.Old
		res.New.Config = cloneRoute(res.Old.Config)

		if err := change(&res.New); err != nil {
			return fmt.Errorf("change route: %w", err)
		}

		changed, err := differ(res.Old.view(), res.New.view())
		if err != nil {
			return fmt.Errorf("compare route: %w", err)
		}

		res.Changed = res.Created || changed
		if !res.Changed {
			return nil
		}

		if err := s.checkRoute(ctx, q, res.New.Config); err != nil {
			return fmt.Errorf("check route: %w", err)
		}

		if err := s.writeRoute(ctx, q, actor, &res); err != nil {
			return fmt.Errorf("save route: %w", err)
		}

		action, old := ActionRouteUpdated, any(res.Old.view())
		if res.Created {
			action, old = ActionRouteCreated, nil
		}

		return s.record(ctx, tx, actor, action, KindRoute, name, old, res.New.view())
	})
	if err != nil {
		return RouteChange{}, fmt.Errorf("set route %s: %w", name, err)
	}

	return res, nil
}

// checkRoute runs the runtime route checks, with the runtime listeners as
// further intakes.
func (s *Store) checkRoute(ctx context.Context, q *db.Queries, r config.Route) error {
	rows, err := q.ListListeners(ctx)
	if err != nil {
		return fmt.Errorf("list listeners: %w", err)
	}

	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = row.Name
	}

	if err := s.file.CheckRuntimeRoute(ctx, r, names); err != nil {
		return fmt.Errorf("runtime route checks: %w", err)
	}

	return nil
}

func (s *Store) writeRoute(ctx context.Context, q *db.Queries, actor Actor, res *RouteChange) error {
	r := res.New
	now := s.now()

	spec, err := json.Marshal(specOfRoute(r.Config))
	if err != nil {
		return fmt.Errorf("encode route: %w", err)
	}

	if res.Created {
		err = q.InsertRoute(ctx, db.InsertRouteParams{
			Name: r.Config.Name, Priority: int32(r.Config.Priority), Spec: spec, Enabled: r.Enabled, //nolint:gosec // Priorities are small; config checks bound them.
			UpdatedBy: optional(actor.ID), UpdatedAt: now,
		})
	} else {
		err = q.UpdateRoute(ctx, db.UpdateRouteParams{
			Name: r.Config.Name, Priority: int32(r.Config.Priority), Spec: spec, Enabled: r.Enabled, //nolint:gosec // Priorities are small; config checks bound them.
			UpdatedBy: optional(actor.ID), UpdatedAt: now,
		})
	}

	if err != nil {
		return fmt.Errorf("write route: %w", err)
	}

	res.New.Version, res.New.UpdatedBy, res.New.UpdatedAt = res.Old.Version+1, actor.ID, now

	return nil
}

// record writes the audit entry of a change and the notification that
// makes every replica reload.
func (s *Store) record(ctx context.Context, tx pgx.Tx, actor Actor, action, kind, name string, before, after any) error {
	err := s.audit.Write(ctx, tx, audit.Entry{
		ActorKind: actor.Kind, ActorID: actor.ID, Action: action, TargetKind: kind, TargetID: name, Old: before, New: after,
	})
	if err != nil {
		return fmt.Errorf("audit the change: %w", err)
	}

	if err := db.New(tx).NotifyRulesChanged(ctx, kind+":"+name); err != nil {
		return fmt.Errorf("notify rules_changed: %w", err)
	}

	return nil
}

// inTx runs fn in a transaction and commits it. A duplicate key, which
// means a concurrent change created the same rule, becomes ErrConflict.
func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, s.pool, fn)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: %w", ErrConflict, err)
	}

	if err != nil {
		return fmt.Errorf("run transaction: %w", err)
	}

	return nil
}

func (s *Store) fileRoute(name string) bool {
	return slices.ContainsFunc(s.file.Routes, func(r config.Route) bool { return r.Name == name })
}

// load reads every runtime rule through q.
func load(ctx context.Context, q db.DBTX) (Runtime, error) {
	queries := db.New(q)

	listenerRows, err := queries.ListListeners(ctx)
	if err != nil {
		return Runtime{}, fmt.Errorf("list listeners: %w", err)
	}

	routeRows, err := queries.ListRoutes(ctx)
	if err != nil {
		return Runtime{}, fmt.Errorf("list routes: %w", err)
	}

	rt := Runtime{Listeners: make([]Listener, 0, len(listenerRows)), Routes: make([]Route, 0, len(routeRows))}

	for _, row := range listenerRows {
		l, err := listenerOfRow(row)
		if err != nil {
			return Runtime{}, fmt.Errorf("load listeners: %w", err)
		}

		rt.Listeners = append(rt.Listeners, l)
	}

	for _, row := range routeRows {
		r, err := routeOfRow(row)
		if err != nil {
			return Runtime{}, fmt.Errorf("load routes: %w", err)
		}

		rt.Routes = append(rt.Routes, r)
	}

	return rt, nil
}

// cloneRoute copies r deeply enough that changing its lists, labels and
// match does not change r.
func cloneRoute(r config.Route) config.Route {
	out := r
	out.Sources = slices.Clone(r.Sources)
	out.Outputs = slices.Clone(r.Outputs)
	out.Actions = slices.Clone(r.Actions)
	out.Roles = maps.Clone(r.Roles)
	out.Grouping.FingerprintLabels = slices.Clone(r.Grouping.FingerprintLabels)

	if r.Match != nil {
		m := *r.Match
		m.Intake = slices.Clone(m.Intake)
		m.Labels = maps.Clone(m.Labels)
		m.Severity = slices.Clone(m.Severity)
		out.Match = &m
	}

	return out
}

// differ reports whether two views encode to different JSON, so lists
// that are nil in one and empty in the other count as the same.
func differ(a, b any) (bool, error) {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false, fmt.Errorf("encode: %w", err)
	}

	bJSON, err := json.Marshal(b)
	if err != nil {
		return false, fmt.Errorf("encode: %w", err)
	}

	return !bytes.Equal(aJSON, bJSON), nil
}
