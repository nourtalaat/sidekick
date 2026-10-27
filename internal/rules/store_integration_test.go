//go:build integration

package rules_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// errBadValue stands for an error from a route change function.
var errBadValue = errors.New("bad value")

func TestAddListener(t *testing.T) {
	t.Parallel()

	billing := config.Listener{
		Channel: "#billing",
		Match:   config.ListenerMatch{Sender: config.ListenerSender{Kind: event.SenderBot, Names: []string{"Grafana"}}, Text: "(?i)firing"},
	}

	tests := []struct {
		name      string
		listener  string
		config    config.Listener
		wantErr   error
		wantAudit []auditRow
	}{
		{
			name:     "a new listener",
			listener: "billing",
			config:   billing,
			wantAudit: []auditRow{{
				Actor: actor, Action: rules.ActionListenerCreated, Target: "listener:billing",
				Details: map[string]any{"new": map[string]any{
					"channel": "#billing", "trusted": false, "enabled": true,
					"match": map[string]any{"sender": map[string]any{"kind": "bot", "names": []any{"Grafana"}}, "text": "(?i)firing"},
				}},
			}},
		},
		{name: "the name of a file listener", listener: "alerts", config: billing, wantErr: rules.ErrFileRule},
		{name: "the name of a runtime listener", listener: "ops", config: billing, wantErr: rules.ErrExists},
		{name: "a listener that fails the checks", listener: "billing", config: config.Listener{Channel: "billing"}, wantErr: config.Errors{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			err := store.AddListener(t.Context(), actor, tt.listener, tt.config)
			requireErr(t, err, tt.wantErr)
			require.Equal(t, tt.wantAudit, auditRows(t, pool))

			if tt.wantErr == nil {
				rt, err := store.Load(t.Context())
				require.NoError(t, err)
				require.Len(t, rt.Listeners, 2)
				require.Equal(t, tt.config, rt.Listeners[0].Config)
				require.Equal(t, actor, rt.Listeners[0].UpdatedBy)
			}
		})
	}
}

func TestRemoveListener(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		listener  string
		wantErr   error
		wantAudit []auditRow
	}{
		{
			name:     "a runtime listener",
			listener: "ops",
			wantAudit: []auditRow{{
				Actor: actor, Action: rules.ActionListenerDeleted, Target: "listener:ops",
				Details: map[string]any{"old": map[string]any{
					"channel": "#ops", "trusted": false, "enabled": true, "match": map[string]any{"sender": map[string]any{"kind": "bot"}},
				}},
			}},
		},
		{name: "a file listener", listener: "alerts", wantErr: rules.ErrFileRule},
		{name: "a listener that does not exist", listener: "nope", wantErr: rules.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			err := store.RemoveListener(t.Context(), actor, tt.listener)
			requireErr(t, err, tt.wantErr)
			require.Equal(t, tt.wantAudit, auditRows(t, pool))
		})
	}
}

func TestSetRoute(t *testing.T) {
	t.Parallel()

	setPriority := func(p int) func(r *rules.Route) error {
		return func(r *rules.Route) error {
			r.Config.Priority = p

			return nil
		}
	}

	tests := []struct {
		name        string
		route       string
		change      func(r *rules.Route) error
		wantErr     error
		wantCreated bool
		wantChanged bool
		wantActions []string
		wantVersion int
	}{
		{
			name:  "a new route",
			route: "search",
			change: func(r *rules.Route) error {
				r.Config.Match = &config.Match{Service: "search-*"}

				return nil
			},
			wantCreated: true,
			wantChanged: true,
			wantActions: []string{rules.ActionRouteCreated},
			wantVersion: 1,
		},
		{
			name:        "a change to a runtime route",
			route:       "checkout",
			change:      setPriority(5),
			wantChanged: true,
			wantActions: []string{rules.ActionRouteUpdated},
			wantVersion: 2,
		},
		{
			name:        "a change that changes nothing",
			route:       "checkout",
			change:      setPriority(20),
			wantVersion: 1,
		},
		{
			name:  "a new route without a match",
			route: "search",
			change: func(r *rules.Route) error {
				r.Config.Priority = 10

				return nil
			},
			wantErr: config.Errors{},
		},
		{
			name:  "a budget above the file ceiling",
			route: "checkout",
			change: func(r *rules.Route) error {
				r.Config.Budgets.MaxToolCalls = 100

				return nil
			},
			wantErr:     config.Errors{},
			wantVersion: 1,
		},
		{name: "a file route", route: "payments", change: setPriority(1), wantErr: rules.ErrFileRule},
		{name: "the default route", route: config.DefaultRouteName, change: setPriority(1), wantErr: rules.ErrFileRule},
		{
			name:        "an error from the change",
			route:       "checkout",
			change:      func(*rules.Route) error { return errBadValue },
			wantErr:     errBadValue,
			wantVersion: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			res, err := store.SetRoute(t.Context(), actor, tt.route, tt.change)
			requireErr(t, err, tt.wantErr)
			require.Equal(t, tt.wantCreated, res.Created)
			require.Equal(t, tt.wantChanged, res.Changed)

			var actions []string
			for _, row := range auditRows(t, pool) {
				require.Equal(t, "route:"+tt.route, row.Target)
				actions = append(actions, row.Action)
			}

			require.Equal(t, tt.wantActions, actions, "each change writes exactly one audit row")

			version, err := routeVersion(t.Context(), store, tt.route)
			require.NoError(t, err)
			require.Equal(t, tt.wantVersion, version)
		})
	}
}

func TestSetRouteAuditsOldAndNew(t *testing.T) {
	t.Parallel()

	pool, store := setup(t)

	_, err := store.SetRoute(t.Context(), actor, "checkout", func(r *rules.Route) error {
		r.Config.Priority = 5

		return nil
	})
	require.NoError(t, err)

	rows := auditRows(t, pool)
	require.Len(t, rows, 1)
	require.InDelta(t, 20, rows[0].Details["old"].(map[string]any)["priority"], 0)
	require.InDelta(t, 5, rows[0].Details["new"].(map[string]any)["priority"], 0)
}

// TestQueryPlans runs EXPLAIN ANALYZE on the queries that look up one
// rule. ListListeners and ListRoutes read whole tables, which hold a few
// dozen rows at most, so they have no plan check. Run with -v to see the
// plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy.sql")

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex string
	}{
		{
			name: "LockListener",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockListener(ctx, "listener-500")

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "listeners_pkey",
		},
		{
			name: "LockRoute",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockRoute(ctx, "route-500")

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "routes_pkey",
		},
		{
			name: "DeleteListener",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DeleteListener(ctx, "listener-500")

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "listeners_pkey",
		},
		{
			name: "UpdateRoute",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.UpdateRoute(ctx, db.UpdateRouteParams{Name: "route-500", Priority: 5, Spec: []byte(`{}`), Enabled: true, UpdatedAt: time.Now()})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "routes_pkey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := &storetest.Explainer{Tx: tx}

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)
			t.Logf("EXPLAIN ANALYZE of %s:\n%s", tt.name, explain.Plan)
			require.Contains(t, explain.Plan, tt.wantIndex)
			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}

// requireErr checks that err is nil when want is nil, wraps want when it
// is an error value, and holds config.Errors when want is config.Errors.
func requireErr(t *testing.T, err, want error) {
	t.Helper()

	var problems config.Errors

	switch {
	case want == nil:
		require.NoError(t, err)
	case errors.As(want, &problems):
		require.ErrorAs(t, err, &problems)
		require.NotEmpty(t, problems)
	default:
		require.ErrorIs(t, err, want)
	}
}

// routeVersion returns the stored version of a runtime route, or 0 when
// none is stored.
func routeVersion(ctx context.Context, store *rules.Store, name string) (int, error) {
	rt, err := store.Load(ctx)
	if err != nil {
		return 0, fmt.Errorf("load: %w", err)
	}

	for _, r := range rt.Routes {
		if r.Config.Name == name {
			return r.Version, nil
		}
	}

	return 0, nil
}
