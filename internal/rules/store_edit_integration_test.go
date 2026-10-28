//go:build integration

package rules_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/rules"
)

func TestUpdateListener(t *testing.T) {
	t.Parallel()

	moved := config.Listener{Channel: "#ops-2", Match: config.ListenerMatch{Sender: config.ListenerSender{Kind: event.SenderBot}}}
	same := config.Listener{Channel: "#ops", Match: config.ListenerMatch{Sender: config.ListenerSender{Kind: event.SenderBot}}}

	tests := []struct {
		name        string
		listener    string
		version     int
		config      config.Listener
		enabled     bool
		wantErr     error
		wantVersion int
		wantActions []string
	}{
		{
			name: "a new channel, turned off", listener: "ops", version: 1, config: moved, enabled: false,
			wantVersion: 2, wantActions: []string{rules.ActionListenerUpdated},
		},
		{name: "a change that changes nothing", listener: "ops", version: 1, config: same, enabled: true, wantVersion: 1},
		{name: "an old version", listener: "ops", version: 2, config: moved, enabled: true, wantErr: rules.ErrStale, wantVersion: 1},
		{name: "a file listener", listener: "alerts", version: 1, config: moved, enabled: true, wantErr: rules.ErrFileRule},
		{name: "a listener that does not exist", listener: "nope", version: 1, config: moved, enabled: true, wantErr: rules.ErrNotFound},
		{
			name: "a listener that fails the checks", listener: "ops", version: 1, config: config.Listener{Channel: "ops"}, enabled: true,
			wantErr: config.Errors{}, wantVersion: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			got, err := store.UpdateListener(t.Context(), actor, tt.listener, tt.version, tt.config, tt.enabled)
			requireErr(t, err, tt.wantErr)

			var actions []string
			for _, row := range auditRows(t, pool) {
				actions = append(actions, row.Action)
			}

			require.Equal(t, tt.wantActions, actions, "each change writes exactly one audit row")

			if tt.wantErr != nil {
				return
			}

			require.Equal(t, tt.wantVersion, got.Version)
			require.Equal(t, tt.enabled, got.Enabled)

			rt, err := store.Load(t.Context())
			require.NoError(t, err)
			require.Len(t, rt.Listeners, 1)
			require.Equal(t, tt.wantVersion, rt.Listeners[0].Version)
			require.Equal(t, tt.enabled, rt.Listeners[0].Enabled)
			require.Equal(t, tt.config.Channel, rt.Listeners[0].Config.Channel)
		})
	}
}

func TestUpdateListenerAuditsOldAndNew(t *testing.T) {
	t.Parallel()

	pool, store := setup(t)

	_, err := store.UpdateListener(t.Context(), actor, "ops", 1, config.Listener{Channel: "#ops-2"}, true)
	require.NoError(t, err)

	rows := auditRows(t, pool)
	require.Len(t, rows, 1)
	require.Equal(t, "#ops", rows[0].Details["old"].(map[string]any)["channel"])
	require.Equal(t, "#ops-2", rows[0].Details["new"].(map[string]any)["channel"])
}

func TestCreateRoute(t *testing.T) {
	t.Parallel()

	noMatch := runtimeRoute("search", 10, func(r *config.Route) { r.Match = nil }).Config

	tests := []struct {
		name        string
		route       config.Route
		wantErr     error
		wantActions []string
	}{
		{name: "a new route", route: runtimeRoute("search", 10, nil).Config, wantActions: []string{rules.ActionRouteCreated}},
		{name: "the name of a runtime route", route: runtimeRoute("checkout", 10, nil).Config, wantErr: rules.ErrExists},
		{name: "the name of a file route", route: runtimeRoute("payments", 10, nil).Config, wantErr: rules.ErrFileRule},
		{name: "the default route", route: runtimeRoute(config.DefaultRouteName, 10, nil).Config, wantErr: rules.ErrFileRule},
		{name: "a route without a match", route: noMatch, wantErr: config.Errors{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			res, err := store.CreateRoute(t.Context(), actor, tt.route)
			requireErr(t, err, tt.wantErr)

			var actions []string
			for _, row := range auditRows(t, pool) {
				actions = append(actions, row.Action)
			}

			require.Equal(t, tt.wantActions, actions)

			if tt.wantErr == nil {
				require.True(t, res.Created)
				require.Equal(t, 1, res.New.Version)
				require.Equal(t, actorID, res.New.UpdatedBy)
			}
		})
	}
}

func TestReplaceRoute(t *testing.T) {
	t.Parallel()

	overBudget := runtimeRoute("checkout", 20, func(r *config.Route) { r.Budgets.MaxToolCalls = 100 }).Config

	tests := []struct {
		name        string
		version     int
		route       config.Route
		enabled     bool
		wantErr     error
		wantVersion int
		wantActions []string
	}{
		{
			name: "a new priority, turned off", version: 1, route: runtimeRoute("checkout", 5, nil).Config, enabled: false,
			wantVersion: 2, wantActions: []string{rules.ActionRouteUpdated},
		},
		{name: "an old version", version: 2, route: runtimeRoute("checkout", 5, nil).Config, enabled: true, wantErr: rules.ErrStale, wantVersion: 1},
		{name: "a route that does not exist", version: 1, route: runtimeRoute("search", 5, nil).Config, enabled: true, wantErr: rules.ErrNotFound},
		{name: "a file route", version: 1, route: runtimeRoute("payments", 5, nil).Config, enabled: true, wantErr: rules.ErrFileRule},
		{name: "a budget above the file ceiling", version: 1, route: overBudget, enabled: true, wantErr: config.Errors{}, wantVersion: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			res, err := store.ReplaceRoute(t.Context(), actor, tt.version, tt.route, tt.enabled)
			requireErr(t, err, tt.wantErr)

			var actions []string
			for _, row := range auditRows(t, pool) {
				actions = append(actions, row.Action)
			}

			require.Equal(t, tt.wantActions, actions)

			version, err := routeVersion(t.Context(), store, tt.route.Name)
			require.NoError(t, err)
			require.Equal(t, tt.wantVersion, version)

			if tt.wantErr == nil {
				require.Equal(t, tt.wantVersion, res.New.Version)
				require.Equal(t, tt.enabled, res.New.Enabled)
			}
		})
	}
}

func TestDeleteRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// seed is SQL that runs before the deletion.
		seed         string
		route        string
		wantErr      error
		wantShadowed bool
		wantActions  []string
	}{
		{name: "a runtime route", route: "checkout", wantActions: []string{rules.ActionRouteDeleted}},
		{
			name:  "a runtime route that a file route shadows",
			seed:  `INSERT INTO routes (name, priority, spec, version, updated_at) VALUES ('payments', 10, '{"mode": "read-only"}', 1, now())`,
			route: "payments", wantShadowed: true, wantActions: []string{rules.ActionRouteDeleted},
		},
		{name: "a file route", route: "payments", wantErr: rules.ErrFileRule},
		{name: "the default route", route: config.DefaultRouteName, wantErr: rules.ErrFileRule},
		{name: "a route that does not exist", route: "nope", wantErr: rules.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			if tt.seed != "" {
				_, err := pool.Exec(t.Context(), tt.seed)
				require.NoError(t, err)
			}

			shadowed, err := store.DeleteRoute(t.Context(), actor, tt.route)
			requireErr(t, err, tt.wantErr)
			require.Equal(t, tt.wantShadowed, shadowed)

			var actions []string
			for _, row := range auditRows(t, pool) {
				actions = append(actions, row.Action)
			}

			require.Equal(t, tt.wantActions, actions)

			if tt.wantErr == nil {
				version, err := routeVersion(t.Context(), store, tt.route)
				require.NoError(t, err)
				require.Zero(t, version, "the route is gone")
			}
		})
	}
}

func TestChangesRecordTheKindOfActor(t *testing.T) {
	t.Parallel()

	pool, store := setup(t)
	token := rules.Actor{Kind: audit.ActorToken, ID: uuid.NewString()}

	_, err := store.DeleteRoute(t.Context(), token, "checkout")
	require.NoError(t, err)

	var kind, id string

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT actor_kind, actor_id FROM audit_log").Scan(&kind, &id))
	require.Equal(t, audit.ActorToken, kind)
	require.Equal(t, token.ID, id)
}
