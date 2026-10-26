package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
)

// exampleSecrets are the secret references in fixture/valid/example.yaml.
var exampleSecrets = []registry.SecretRef{
	{Name: "sidekick-db", Key: "url"},
	{Name: "sidekick-anthropic", Key: "apiKey"},
	{Name: "signoz-mcp", Key: "token"},
	{Name: "sidekick-hooks", Key: "alertmanager"},
	{Name: "sidekick-hooks", Key: "ci"},
	{Name: "sidekick-slack", Key: "botToken"},
	{Name: "sidekick-redaction", Key: "key"},
}

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
				require.Equal(t, "claude", cfg.Model.Roles[model.RoleInvestigate])
				require.Equal(t, map[string]model.Price{"claude-opus-5-5": {InputPerMTok: 5_000_000, OutputPerMTok: 25_000_000, CacheReadPerMTok: 500_000}}, cfg.Model.PriceTable())
				require.Equal(t, Limits{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute}, cfg.Budgets.PerInvestigation)
				require.Equal(t, report.Rules{MinFalsePositiveConfidence: report.High}, cfg.Report.Rules())
				require.Equal(t, "/etc/sidekick/strategies", cfg.Strategies.Dir)
				require.Equal(t, LogLevelInfo, cfg.Telemetry.LogLevel)
				require.Equal(t, []RedactionRule{{Name: "customer_id", Pattern: "cus_[A-Za-z0-9]{14}"}}, cfg.Redaction.Custom)
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
			name: "routes in match order with effective budgets",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				routes := cfg.RoutesInOrder()
				require.Len(t, routes, 2)
				require.Equal(t, "payments-latency", routes[0].Name)
				require.Equal(t, 10, routes[0].Priority)
				require.Equal(t, []event.Severity{event.SeverityWarning, event.SeverityCritical}, routes[0].Match.Severity)
				require.Equal(t, Limits{MaxToolCalls: 30, MaxWallTime: 10 * time.Minute}, routes[0].Budgets)
				require.Equal(t, "default", routes[1].Name)
				require.Equal(t, action.ReadOnly, routes[1].Mode)
				require.Equal(t, Limits{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute}, routes[1].Budgets)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), readFixture(t, "valid/example.yaml"), testOptions())
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
			data := strings.Replace(string(readFixture(t, "valid/example.yaml")), "dir: /var/run/secrets/sidekick", "dir: "+dir, 1)

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
