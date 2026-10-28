package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// exampleSecrets are the secret references in fixture/valid/example.yaml.
var exampleSecrets = []registry.SecretRef{
	{Name: "sidekick-db", Key: "url"},
	{Name: "sidekick-anthropic", Key: "apiKey"},
	{Name: "signoz-mcp", Key: "token"},
	{Name: "sidekick-hooks", Key: "alertmanager"},
	{Name: "sidekick-hooks", Key: "ci"},
	{Name: "sidekick-slack", Key: "botToken"},
	{Name: "sidekick-slack", Key: "appToken"},
	{Name: "sidekick-redaction", Key: "key"},
}

// exampleStrategiesDir holds the file strategy the example config names.
// The example points at /etc/sidekick/strategies, as a deployment would.
var exampleStrategiesDir = filepath.Join("fixture", "strategies")

func TestExampleConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check func(t *testing.T, cfg *Config)
	}{
		{
			name: "typed sections",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, Server{PublicURL: "https://sidekick.example.com", Listen: ":8080", MetricsListen: ":9090"}, cfg.Server)
				require.Equal(t, 20, cfg.Database.MaxConns)
				require.Equal(t, MigrationsManual, cfg.Database.Migrations)
				require.Equal(t, "claude", cfg.Model.Roles[model.RoleInvestigate])
				require.Equal(t, map[string]model.Price{"claude-opus-5-5": {InputPerMTok: 5_000_000, OutputPerMTok: 25_000_000, CacheReadPerMTok: 500_000}}, cfg.Model.PriceTable())
				require.Equal(t, Limits{MaxTokens: 400_000, MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostUSD: 1}, cfg.Budgets.PerInvestigation)
				require.Equal(t, DailyBudget{MaxCostUSD: 200, TimeZone: "UTC", OnDailyCap: OnDailyCapWait}, cfg.Budgets.Daily)
				require.Equal(t, int64(200_000_000), cfg.Budgets.Daily.MaxCostMicros())
				require.Equal(t, &RateLimit{Events: 600, Per: time.Hour, OverLimit: OverLimitDelay}, cfg.Budgets.RateLimit)
				require.Equal(t, report.Rules{MinFalsePositiveConfidence: report.High}, cfg.Report.Rules())
				require.Equal(t, exampleStrategiesDir, cfg.Strategies.Dir)
				require.Equal(t, LogLevelInfo, cfg.Telemetry.LogLevel)
				require.Equal(t, []RedactionRule{{Name: "customer_id", Pattern: "cus_[A-Za-z0-9]{14}"}}, cfg.Redaction.Custom)
				require.Equal(t, WatchHTTP{AllowHosts: []string{"*.internal.example.com", "status.example.com"}, AllowPrivate: []string{"10.20.0.0/16"}}, cfg.Watch.HTTP)
				require.Equal(t, Retention{
					Events:         Period(90 * Day),
					Investigations: Period(365 * Day),
					Steps:          Period(90 * Day),
					AuditLog:       Period(730 * Day),
					WatcherChecks:  Period(30 * Day),
					Memory:         Period(730 * Day),
				}, cfg.Retention)
			},
		},
		{
			name: "plugin sections",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, "anthropic", cfg.Model.Connectors["claude"].Type)
				require.Equal(t, "mcp", cfg.Sources["signoz"].Type)
				require.Equal(t, "generic", cfg.Intake.Webhooks["generic-ci"].Type)
				require.Equal(t, 5*time.Minute, cfg.Intake.Webhooks["generic-ci"].Auth.Tolerance)
				require.True(t, cfg.Intake.Webhooks["alertmanager"].Trusted)
				require.Equal(t, "slack", cfg.Outputs["slack-thread"].Type)
			},
		},
		{
			name: "the Slack app and its listeners",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, SlackModeSocket, cfg.Slack.Mode)
				require.True(t, cfg.Slack.Connects())
				require.Equal(t, []string{"B01SIGNOZ"}, cfg.Slack.TrustedBots)
				require.Equal(t, map[string]RoleName{"S01ONCALL": RoleResponder, "S01SRE": RoleAdmin}, cfg.Slack.RoleGroups)
				require.Equal(t, map[string]RoleName{"U01LEAD": RoleApprover}, cfg.Slack.RoleUsers)

				alerts := cfg.Listeners["alerts"]
				require.Equal(t, "#alerts", alerts.Channel)
				require.Equal(t, ListenerSender{Kind: event.SenderBot, Names: []string{"SigNoz"}}, alerts.Match.Sender)
				require.Len(t, alerts.Extract, 4)
				require.Equal(t, "(?i)resolved|recovered", alerts.Recovery)
				require.True(t, alerts.Trusted)
				require.Equal(t, []string{"alertmanager", "alerts", SlackMentionIntake}, cfg.RoutesInOrder()[0].Match.Intake)
			},
		},
		{
			name: "built-in and file strategies",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, []string{"error-rate-alert", "generic-alert", "latency-alert", "service-down"}, cfg.StrategySet().Names())

				st, ok := cfg.StrategySet().Get("latency-alert")
				require.True(t, ok)
				require.Equal(t, strategy.File, st.Origin)
			},
		},
		{
			name: "routes in match order with effective budgets",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				routes := cfg.RoutesInOrder()
				require.Len(t, routes, 2)
				require.Equal(t, "payments-latency", routes[0].Name)
				require.Equal(t, 10, routes[0].Priority)
				require.Equal(t, []event.Severity{event.SeverityWarning, event.SeverityCritical}, routes[0].Match.Severity)
				require.Equal(t, RouteBudgets{
					Limits: Limits{MaxTokens: 400_000, MaxToolCalls: 30, MaxWallTime: 10 * time.Minute, MaxCostUSD: 0.75},
					Daily:  RouteDaily{MaxCostUSD: 40, OnDailyCap: OnDailyCapSkip},
				}, routes[0].Budgets)
				require.Equal(t, int64(750_000), routes[0].Budgets.MaxCostMicros())
				require.Equal(t, &RateLimit{Events: 60, Per: time.Hour, OverLimit: OverLimitSkip}, routes[0].RateLimit)
				require.Equal(t, "default", routes[1].Name)
				require.Equal(t, action.ReadOnly, routes[1].Mode)
				require.Equal(t, RouteBudgets{Limits: Limits{MaxTokens: 400_000, MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostUSD: 1}, Daily: RouteDaily{OnDailyCap: OnDailyCapWait}}, routes[1].Budgets)
				require.Nil(t, routes[1].RateLimit)
				require.Equal(t, 8, cfg.Budgets.Concurrency)
				require.Equal(t, map[string]int{"payments-latency": 3}, cfg.RouteConcurrency())
			},
		},
		{
			name: "watcher policy with resolved route watchers",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				p := cfg.WatchPolicy()
				require.Equal(t, 3, p.MaxPerGroup)
				require.Equal(t, 100, p.MaxTotal)
				require.Equal(t, watch.Bounds{Path: "watch", MinInterval: 10 * time.Second, MaxInterval: 15 * time.Minute, MaxTimeout: 4 * time.Hour}, p.Bounds)

				payments, ok := p.Route("payments-latency")
				require.True(t, ok)
				require.True(t, payments.AllowModel)
				require.Equal(t, []watch.Type{watch.TypeHTTP, watch.TypeTool}, payments.Types)
				require.Equal(t, []string{"*.internal.example.com"}, payments.Hosts)
				require.Len(t, payments.Definitions, 3)

				health := payments.Definitions[0]
				require.Equal(t, "checkout-health", health.Name)
				require.Equal(t, watch.OriginRoute, health.Origin)
				require.Equal(t, watch.Settings{Interval: 15 * time.Second, Timeout: 90 * time.Minute, PassesToRecover: 3, CheckTimeout: 10 * time.Second}, health.Settings)
				require.True(t, health.Match.Matches("CheckoutDown", ""))
				require.False(t, health.Match.Matches("CheckoutLatency", ""))
				require.IsType(t, watch.HTTPCheck{}, health.Check)

				errorRate := payments.Definitions[1]
				require.Equal(t, 20*time.Second, errorRate.Settings.Interval)
				require.IsType(t, watch.ToolCheck{}, errorRate.Check)
				require.Equal(t, watch.TypeAlert, payments.Definitions[2].Check.Type())

				def, ok := p.Route("default")
				require.True(t, ok)
				require.Equal(t, []watch.Type{watch.TypeHTTP, watch.TypeTool, watch.TypeAlert}, def.Types)
				require.Empty(t, def.Tools)
				require.Equal(t, cfg.Watch.HTTP.AllowHosts, def.Hosts)
				require.Empty(t, def.Definitions)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), exampleYAML(t), testOptions())
			require.NoError(t, err)
			tt.check(t, cfg)
		})
	}
}

func TestExampleConfigWithSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		present []registry.SecretRef
		wantErr string
	}{
		{name: "every secret present: checks and build pass", present: exampleSecrets},
		{
			name:    "one secret missing",
			present: exampleSecrets[1:],
			wantErr: "database.urlSecret: cannot read secret sidekick-db/url: the file does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeSecrets(t, tt.present...)
			data := strings.Replace(string(exampleYAML(t)), "dir: /var/run/secrets/sidekick", "dir: "+dir, 1)

			opts := testOptions()
			opts.CheckSecrets = true

			cfg, err := Parse(t.Context(), []byte(data), opts)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			in, err := Build(t.Context(), cfg, opts.Registries, registry.Deps{})
			require.NoError(t, err)
			require.Len(t, in.Models, 3)
			require.NoError(t, in.Close())
		})
	}
}

// exampleYAML returns fixture/valid/example.yaml with strategies.dir set to
// exampleStrategiesDir.
func exampleYAML(t *testing.T) []byte {
	t.Helper()

	data := string(readFixture(t, "valid/example.yaml"))
	require.Contains(t, data, "dir: /etc/sidekick/strategies")

	return []byte(strings.Replace(data, "dir: /etc/sidekick/strategies", "dir: "+exampleStrategiesDir, 1))
}
