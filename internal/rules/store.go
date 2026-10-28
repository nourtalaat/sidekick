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
	ActionListenerDeleted = "listener.deleted"
	ActionRouteCreated    = "route.created"
	ActionRouteUpdated    = "route.updated"
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
)

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

// AddListener saves a new runtime listener for the Slack user actor. The
// error wraps config.Errors when the listener fails the checks, ErrFileRule
// when the file has a listener of that name, and ErrExists when a runtime
// one has it.
func (s *Store) AddListener(ctx context.Context, actor, name string, l config.Listener) error {
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
			Name: name, SlackChannel: l.Channel, Match: match, UpdatedBy: optional(actor), UpdatedAt: s.now(),
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

// RemoveListener deletes a runtime listener for the Slack user actor. The
// error wraps ErrFileRule for a file listener, and ErrNotFound when no
// runtime listener has the name.
func (s *Store) RemoveListener(ctx context.Context, actor, name string) error {
	if _, ok := s.file.Listeners[name]; ok {
		return fmt.Errorf("remove listener %s: %w", name, ErrFileRule)
	}

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

		if _, err := q.DeleteListener(ctx, name); err != nil {
			return fmt.Errorf("delete listener: %w", err)
		}

		return s.record(ctx, tx, actor, ActionListenerDeleted, KindListener, name, old.view(), nil)
	})
	if err != nil {
		return fmt.Errorf("remove listener %s: %w", name, err)
	}

	return nil
}

// SetRoute changes the runtime route named name for the Slack user actor,
// or creates it from config.NewRoute when it does not exist. change edits
// the route; an error from it is returned as it is, wrapped. The result
// must pass the runtime route checks, or the error wraps config.Errors. A
// change that leaves the route as it was writes nothing.
func (s *Store) SetRoute(ctx context.Context, actor, name string, change func(r *Route) error) (RouteChange, error) {
	if name == config.DefaultRouteName || s.fileRoute(name) {
		return RouteChange{}, fmt.Errorf("set route %s: %w", name, ErrFileRule)
	}

	var res RouteChange

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)

		row, err := q.LockRoute(ctx, name)

		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Created = true
			res.Old = Route{Config: config.NewRoute(name), Enabled: true}
		case err != nil:
			return fmt.Errorf("read route: %w", err)
		default:
			if res.Old, err = routeOfRow(row); err != nil {
				return fmt.Errorf("read route: %w", err)
			}
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

		if err := s.writeRoute(ctx, q, actor, res); err != nil {
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

func (s *Store) writeRoute(ctx context.Context, q *db.Queries, actor string, res RouteChange) error {
	r := res.New

	spec, err := json.Marshal(specOfRoute(r.Config))
	if err != nil {
		return fmt.Errorf("encode route: %w", err)
	}

	if res.Created {
		err = q.InsertRoute(ctx, db.InsertRouteParams{
			Name: r.Config.Name, Priority: int32(r.Config.Priority), Spec: spec, Enabled: r.Enabled, //nolint:gosec // Priorities are small; config checks bound them.
			UpdatedBy: optional(actor), UpdatedAt: s.now(),
		})
	} else {
		err = q.UpdateRoute(ctx, db.UpdateRouteParams{
			Name: r.Config.Name, Priority: int32(r.Config.Priority), Spec: spec, Enabled: r.Enabled, //nolint:gosec // Priorities are small; config checks bound them.
			UpdatedBy: optional(actor), UpdatedAt: s.now(),
		})
	}

	if err != nil {
		return fmt.Errorf("write route: %w", err)
	}

	return nil
}

// record writes the audit entry of a change and the notification that
// makes every replica reload.
func (s *Store) record(ctx context.Context, tx pgx.Tx, actor, action, kind, name string, before, after any) error {
	err := s.audit.Write(ctx, tx, audit.Entry{
		ActorKind: audit.ActorUser, ActorID: actor, Action: action, TargetKind: kind, TargetID: name, Old: before, New: after,
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
