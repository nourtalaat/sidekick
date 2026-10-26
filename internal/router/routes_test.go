package router

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

func TestPick(t *testing.T) {
	t.Parallel()

	cfg, settings := loadConfig(t)

	routes, err := newRoutes(cfg, settings)
	require.NoError(t, err)

	names := make([]string, 0, len(routes))
	for _, r := range routes {
		names = append(names, r.name)
	}

	require.Equal(t, []string{"payments-latency", "grafana", "billing", "critical", "default"}, names, "by priority, then name, with default last")

	payments := candidate{
		intake:   "alertmanager",
		labels:   map[string]string{"team": "payments"},
		service:  "checkout-api",
		severity: event.SeverityCritical,
		text:     "p99 latency is high",
	}

	tests := []struct {
		name  string
		event candidate
		want  string
	}{
		{name: "the first match wins although critical matches too", event: payments, want: "payments-latency"},
		{name: "another intake moves on to the next match", event: withIntake(payments, "grafana"), want: "grafana"},
		{name: "a lower priority is tried first", event: candidate{intake: "grafana", severity: event.SeverityCritical}, want: "grafana"},
		{name: "same priority goes by name", event: candidate{intake: "alertmanager", labels: map[string]string{"team": "billing"}, severity: event.SeverityCritical}, want: "billing"},
		{name: "one field that does not fit moves on", event: withSeverity(payments, event.SeverityInfo), want: "default"},
		{name: "no text moves to the next match", event: candidate{intake: "alertmanager", labels: map[string]string{"team": "payments"}, service: "checkout-api", severity: event.SeverityCritical}, want: "critical"},
		{name: "the default route takes the rest", event: candidate{intake: "alertmanager", severity: event.SeverityWarning}, want: "default"},
		{name: "the default route takes an empty event", event: candidate{}, want: "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := pick(routes, tt.event)
			require.True(t, ok)
			require.Equal(t, tt.want, got.name)
		})
	}
}

func TestPickWithoutDefault(t *testing.T) {
	t.Parallel()

	m, err := newMatcher(&config.Match{Intake: []string{"grafana"}})
	require.NoError(t, err)

	_, ok := pick([]route{{name: "grafana", matcher: m}}, candidate{intake: "alertmanager"})
	require.False(t, ok)
}

func TestNewRoutesSettings(t *testing.T) {
	t.Parallel()

	cfg, settings := loadConfig(t)

	generic, ok := cfg.StrategySet().Get(strategy.GenericAlert)
	require.True(t, ok)

	routes, err := newRoutes(cfg, settings)
	require.NoError(t, err)

	byName := map[string]route{}
	for _, r := range routes {
		byName[r.name] = r
	}

	tests := []struct {
		name           string
		route          string
		wantStrategy   string
		wantVersion    string
		wantConnectors string
		wantBudget     string
	}{
		{
			name:           "own strategy, roles and budgets",
			route:          "payments-latency",
			wantStrategy:   "latency-alert",
			wantConnectors: `{"investigate": {"connector": "big", "model": "big-model"}, "summarize": {"connector": "fake", "model": "fake-model"}}`,
			// The route allows 35 tool calls and 5 minutes; the strategy
			// 30 tool calls and 8 minutes.
			wantBudget: `{"max_tool_calls": 30, "max_wall_time_ms": 300000}`,
		},
		{
			name:           "the defaults",
			route:          "default",
			wantStrategy:   strategy.GenericAlert,
			wantVersion:    generic.Version,
			wantConnectors: `{"investigate": {"connector": "fake", "model": "fake-model"}, "summarize": {"connector": "fake", "model": "fake-model"}}`,
			wantBudget:     `{"max_tool_calls": 40, "max_wall_time_ms": 600000}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, ok := byName[tt.route]
			require.True(t, ok)
			require.Equal(t, tt.wantStrategy, r.strategy)
			require.Len(t, r.strategyVersion, 16)

			if tt.wantVersion != "" {
				require.Equal(t, tt.wantVersion, r.strategyVersion)
			}

			require.Equal(t, action.ReadOnly, r.mode)
			require.JSONEq(t, tt.wantConnectors, string(r.connectors))
			require.JSONEq(t, tt.wantBudget, string(r.budget))
		})
	}
}

func TestNewRoutesErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     func(t *testing.T) *config.Config
		wantErr string
	}{
		{
			name:    "a config without strategies",
			cfg:     func(*testing.T) *config.Config { return &config.Config{} },
			wantErr: errNoStrategies.Error(),
		},
		{
			name: "a strategy that is not loaded",
			cfg: func(t *testing.T) *config.Config {
				t.Helper()

				cfg, _ := loadConfig(t)
				cfg.Routes = append(cfg.Routes, config.Route{Name: "other", Strategy: "missing"})

				return cfg
			},
			wantErr: `route "other": strategy "missing" is not loaded`,
		},
		{
			name: "a match that does not compile",
			cfg: func(t *testing.T) *config.Config {
				t.Helper()

				cfg, _ := loadConfig(t)
				cfg.Routes = append(cfg.Routes, config.Route{Name: "other", Match: &config.Match{Text: "("}})

				return cfg
			},
			wantErr: `route "other": match: text:`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newRoutes(tt.cfg(t), nil)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestEffectiveBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		route    config.Limits
		strategy strategy.Budgets
		want     investigator.Budget
	}{
		{
			name:  "the strategy sets no limits",
			route: config.Limits{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute},
			want:  investigator.Budget{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute},
		},
		{
			name:     "the strategy is lower",
			route:    config.Limits{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute},
			strategy: strategy.Budgets{MaxToolCalls: 20, MaxWallTime: time.Minute},
			want:     investigator.Budget{MaxToolCalls: 20, MaxWallTime: time.Minute},
		},
		{
			name:     "the route is lower",
			route:    config.Limits{MaxToolCalls: 10, MaxWallTime: time.Minute},
			strategy: strategy.Budgets{MaxToolCalls: 20, MaxWallTime: 5 * time.Minute},
			want:     investigator.Budget{MaxToolCalls: 10, MaxWallTime: time.Minute},
		},
		{
			name:     "only the strategy sets a limit",
			strategy: strategy.Budgets{MaxToolCalls: 20},
			want:     investigator.Budget{MaxToolCalls: 20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, effectiveBudget(tt.route, tt.strategy))
		})
	}
}

func withIntake(c candidate, intake string) candidate {
	c.intake = intake

	return c
}
