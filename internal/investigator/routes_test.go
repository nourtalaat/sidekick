package investigator

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestStaticRoutes(t *testing.T) {
	t.Parallel()

	routes := StaticRoutes{"payments": {Sources: []string{"signoz"}}, "checkout": {}}

	got, ok := routes.Route("payments")
	require.True(t, ok)
	require.Equal(t, []string{"signoz"}, got.Sources)

	_, ok = routes.Route("search")
	require.False(t, ok)

	require.Equal(t, []string{"checkout", "payments"}, routes.Names())
}

func TestConfigRoutes(t *testing.T) {
	t.Parallel()

	current := []config.Route{
		{Name: "payments", Sources: []string{"signoz"}, Outputs: []string{"slack-thread"}, Budgets: config.RouteBudgets{
			Daily: config.RouteDaily{MaxCostUSD: 2.5, OnDailyCap: config.OnDailyCapSkip},
		}},
		{Name: "checkout"},
	}

	routes := ConfigRoutes(func() []config.Route { return current })

	tests := []struct {
		name   string
		route  string
		want   Route
		wantOK bool
	}{
		{
			name:   "a route with a skipping cap",
			route:  "payments",
			want:   Route{Sources: []string{"signoz"}, Outputs: []string{"slack-thread"}, DailyMaxCostMicros: 2_500_000, DailySkip: true},
			wantOK: true,
		},
		{name: "a route without settings", route: "checkout", want: Route{}, wantOK: true},
		{name: "a route that is not in effect", route: "search", want: Route{}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := routes.Route(tt.route)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}

	require.Equal(t, []string{"checkout", "payments"}, routes.Names())

	current = []config.Route{{Name: "search", Sources: []string{"loki"}}}

	got, ok := routes.Route("search")
	require.True(t, ok, "a route that appears later is found")
	require.Equal(t, []string{"loki"}, got.Sources)

	_, ok = routes.Route("payments")
	require.False(t, ok, "a route that is deleted is gone")
	require.Equal(t, []string{"search"}, routes.Names())
}

func TestRunSettingsReadTheRoute(t *testing.T) {
	t.Parallel()

	conns, err := json.Marshal(Connectors{model.RoleInvestigate: {Connector: "claude"}})
	require.NoError(t, err)

	tests := []struct {
		name        string
		route       string
		wantSources []string
		wantErr     string
	}{
		{name: "a route in effect", route: "payments", wantSources: []string{"signoz"}},
		{name: "a route that is gone", route: "deleted", wantErr: `route "deleted" is no longer in effect`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &Handler{opts: Options{
				Connectors: map[string]Connector{"claude": {}},
				Routes:     StaticRoutes{"payments": {Sources: []string{"signoz"}}},
			}}
			r := &run{h: h, inv: db.Investigation{Route: tt.route, Connectors: conns, Budget: []byte(`{}`)}, log: slog.New(slog.DiscardHandler)}

			err := r.settings()
			if tt.wantErr != "" {
				require.ErrorIs(t, err, queue.ErrGiveUp)
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantSources, r.route.Sources)
		})
	}
}
