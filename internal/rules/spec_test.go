package rules

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestRouteSpecRoundTrip(t *testing.T) {
	t.Parallel()

	full := config.NewRoute("checkout")
	full.Priority = 20
	full.Match = &config.Match{
		Intake: []string{"alerts"}, Labels: map[string]string{"team": "payments"}, Service: "checkout-*",
		Severity: []event.Severity{event.SeverityCritical}, Text: "(?i)latency",
	}
	full.Strategy = "latency-alert"
	full.Sources = []string{"signoz"}
	full.Outputs = []string{"slack-thread"}
	full.Budgets = config.RouteBudgets{Limits: config.Limits{MaxTokens: 100_000, MaxToolCalls: 20, MaxWallTime: 5 * time.Minute, MaxCostUSD: 1.5}}
	full.Grouping = config.Grouping{Window: time.Hour, FingerprintLabels: []string{"service"}, RelatedWindow: 15 * time.Minute}

	minimal := config.NewRoute("minimal")
	minimal.Match = &config.Match{Service: "minimal"}

	tests := []struct {
		name  string
		route config.Route
	}{
		{name: "every field", route: full},
		{name: "defaults only", route: minimal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec, err := json.Marshal(specOfRoute(tt.route))
			require.NoError(t, err)

			got, err := routeOfRow(db.Route{Name: tt.route.Name, Priority: int32(tt.route.Priority), Spec: spec, Enabled: true, Version: 3}) //nolint:gosec // Test priorities are small.
			require.NoError(t, err)
			require.Equal(t, tt.route, got.Config)
			require.True(t, got.Enabled)
			require.Equal(t, 3, got.Version)
		})
	}
}

func TestListenerMatchRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		listener config.Listener
	}{
		{
			name: "every field",
			listener: config.Listener{
				Channel: "C01OPS",
				Match: config.ListenerMatch{
					Sender: config.ListenerSender{Kind: event.SenderBot, Names: []string{"Grafana"}, Bots: []string{"B01GRAFANA"}, Users: []string{"U01ME"}},
					Text:   "(?i)firing",
				},
				Extract:  []string{"service=(?P<service>[a-z-]+)"},
				Recovery: "(?i)resolved",
			},
		},
		{name: "a channel only", listener: config.Listener{Channel: "#ops"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			match, err := json.Marshal(matchOfListener(tt.listener))
			require.NoError(t, err)

			got, err := listenerOfRow(db.Listener{Name: "ops", SlackChannel: tt.listener.Channel, Match: match, Enabled: true, Version: 1})
			require.NoError(t, err)
			require.Equal(t, tt.listener, got.Config)
		})
	}
}

func TestRouteAuditView(t *testing.T) {
	t.Parallel()

	r := config.NewRoute("checkout")
	r.Match = &config.Match{Service: "checkout-*"}
	r.Sources = []string{"signoz"}

	data, err := json.Marshal(Route{Config: r, Enabled: true}.view())
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	require.ElementsMatch(t, []string{"priority", "enabled", "spec"}, keys(got))
	require.ElementsMatch(t, []string{"match", "strategy", "sources", "mode", "grouping"}, keys(got["spec"].(map[string]any)))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
