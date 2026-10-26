package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

func TestParseFileProblems(t *testing.T) {
	t.Parallel()

	minimal := minimalYAML(t)

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "empty file", yaml: "", want: "parse YAML: the file is empty"},
		{name: "comments only", yaml: "# nothing here\n", want: "parse YAML: the file is empty"},
		{name: "invalid YAML", yaml: "server: [\n", want: "parse YAML: invalid YAML: line 1: did not find expected node content"},
		{name: "two documents", yaml: "server: {}\n---\nserver: {}\n", want: "parse YAML: the file must hold one YAML document"},
		{name: "top level is a list", yaml: "- a\n", want: "expected a mapping, got a list"},
		{name: "unknown top-level key", yaml: minimal + "servr: {}\n", want: `servr: unknown key (did you mean "server"?)`},
		{name: "a later section", yaml: minimal + "memory: {}\n", want: "memory: unknown key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), []byte(tt.yaml), testOptions())
			require.EqualError(t, err, tt.want)
			require.Nil(t, cfg)
		})
	}
}

func TestParseDefaults(t *testing.T) {
	t.Parallel()

	minimal := minimalYAML(t)

	tests := []struct {
		name  string
		yaml  string
		check func(t *testing.T, cfg *Config)
	}{
		{
			name: "missing sections get their defaults",
			yaml: minimal,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, Server{Listen: ":8080", MetricsListen: ":9090"}, cfg.Server)
				require.Equal(t, DefaultMaxConns, cfg.Database.MaxConns)
				require.Equal(t, MigrationsStartup, cfg.Database.Migrations)
				require.Equal(t, secrets.DefaultDir, cfg.Secrets.Dir)
				require.Equal(t, Limits{MaxTokens: 400_000, MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostUSD: 1}, cfg.Budgets.PerInvestigation)
				require.Equal(t, DailyBudget{TimeZone: "UTC", OnDailyCap: OnDailyCapWait}, cfg.Budgets.Daily)
				require.Nil(t, cfg.Budgets.RateLimit)
				require.Equal(t, DefaultConcurrency, cfg.Budgets.Concurrency)
				require.Empty(t, cfg.RouteConcurrency())
				require.Equal(t, LogLevelInfo, cfg.Telemetry.LogLevel)
				require.Nil(t, cfg.Slack)
				require.Equal(t, []Route{{
					Name:     "default",
					Priority: DefaultRoutePriority,
					Strategy: DefaultStrategy,
					Mode:     action.ReadOnly,
					Budgets:  RouteBudgets{Limits: Limits{MaxTokens: 400_000, MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostUSD: 1}, Daily: RouteDaily{OnDailyCap: OnDailyCapWait}},
				}}, cfg.Routes)
			},
		},
		{
			name: "set values win over defaults",
			yaml: baseYAML + `
server: {listen: ":1"}
budgets: {perInvestigation: {maxToolCalls: 10}, concurrency: 4}
routes:
  - {name: a, priority: 0, budgets: {maxWallTime: 1m}, concurrency: 2}
  - {name: default}
`,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, Server{Listen: ":1", MetricsListen: ":9090"}, cfg.Server)
				require.Equal(t, Limits{MaxTokens: 400_000, MaxToolCalls: 10, MaxWallTime: 10 * time.Minute, MaxCostUSD: 1}, cfg.Budgets.PerInvestigation)
				require.Equal(t, 0, cfg.Routes[0].Priority)
				require.Equal(t, RouteBudgets{Limits: Limits{MaxTokens: 400_000, MaxToolCalls: 10, MaxWallTime: time.Minute, MaxCostUSD: 1}, Daily: RouteDaily{OnDailyCap: OnDailyCapWait}}, cfg.Routes[0].Budgets)
				require.Equal(t, 4, cfg.Budgets.Concurrency)
				require.Equal(t, map[string]int{"a": 2}, cfg.RouteConcurrency())
			},
		},
		{
			name: "plugin sections keep everything but their type",
			yaml: `
database: {urlSecret: {name: db, key: url}}
model:
  connectors:
    claude: {provider: anthropic, model: m, effort: high}
  roles: {investigate: claude}
routes: [{name: default}]
sources:
  k8s: {type: kubernetes, namespaces: [prod]}
slack: {botTokenSecret: {name: sidekick-slack, key: botToken}}
outputs:
  slack-thread: {type: slack, channel: "#alerts"}
intake:
  webhooks:
    am:
      adapter: generic
      auth: {bearer: {name: hooks, key: am}}
      trusted: true
      fields: {title: $.title}
`,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				claude := cfg.Model.Connectors["claude"]
				require.Equal(t, "anthropic", claude.Type)
				require.Equal(t, "model.connectors.claude", claude.Path())
				require.Equal(t, []string{"model", "m", "effort", "high"}, nodeStrings(claude.Node))

				require.Equal(t, "kubernetes", cfg.Sources["k8s"].Type)
				require.Equal(t, "sources.k8s", cfg.Sources["k8s"].Path())
				require.Equal(t, "slack", cfg.Outputs["slack-thread"].Type)

				am := cfg.Intake.Webhooks["am"]
				require.Equal(t, "generic", am.Type)
				require.Equal(t, "intake.webhooks.am", am.Path())
				require.True(t, am.Trusted)
				require.Equal(t, WebhookAuth{Bearer: &registry.SecretRef{Name: "hooks", Key: "am"}, Tolerance: DefaultHMACTolerance}, am.Auth)
				require.Equal(t, []string{"fields", ""}, nodeStrings(am.Node))
			},
		},
		{
			name: "webhook without auth still gets the default tolerance",
			yaml: minimal + "intake: {webhooks: {ci: {adapter: generic, auth: {hmac: {name: h, key: ci}}}}}\n",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, DefaultHMACTolerance, cfg.Intake.Webhooks["ci"].Auth.Tolerance)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), []byte(tt.yaml), testOptions())
			require.NoError(t, err)
			tt.check(t, cfg)
		})
	}
}

func TestParsePluginSectionProblems(t *testing.T) {
	t.Parallel()

	minimal := minimalYAML(t)

	tests := []struct {
		name string
		yaml string
		want Errors
	}{
		{
			name: "section that is not a mapping",
			yaml: minimal + "sources: {k8s: kubernetes}\n",
			want: Errors{{Path: "sources.k8s", Message: `expected a mapping, got "kubernetes"`}},
		},
		{
			name: "type that is not a string",
			yaml: minimal + "outputs: {x: {type: [slack]}}\n",
			want: Errors{{Path: "outputs.x.type", Message: "expected a string, got a list"}},
		},
		{
			name: "webhook auth with an unknown key",
			yaml: minimal + "intake: {webhooks: {ci: {adapter: generic, auth: {hmac: {name: a, key: b}, bearr: {name: a, key: b}}}}}\n",
			want: Errors{{Path: "intake.webhooks.ci.auth.bearr", Message: `unknown key (did you mean "bearer"?)`}},
		},
		{
			name: "source set twice",
			yaml: minimal + "sources:\n  a: {type: mcp}\n  a: {type: kubernetes}\n",
			want: Errors{{Path: "sources.a", Message: "set more than once"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse(t.Context(), []byte(tt.yaml), testOptions())
			require.Equal(t, tt.want, err)
		})
	}
}

type fakeConnectorConfig struct {
	model.Settings `yaml:",inline"`

	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
}

func TestSectionDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		want    fakeConnectorConfig
		wantErr error
	}{
		{
			name: "known keys",
			yaml: "{provider: p, model: m, maxTokens: 10, apiKeySecret: {name: n, key: k}}",
			want: fakeConnectorConfig{
				Settings:     model.Settings{Model: "m", MaxTokens: 10},
				APIKeySecret: registry.SecretRef{Name: "n", Key: "k"},
			},
		},
		{
			name: "unknown key with the full path",
			yaml: "{provider: p, model: m, thinkng: {mode: off}}",
			want: fakeConnectorConfig{Settings: model.Settings{Model: "m"}},
			wantErr: Errors{{
				Path:    "connectors.claude.thinkng",
				Message: `unknown key (did you mean "thinking"?)`,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var m Model
			require.Empty(t, decodeString(t, "connectors: {claude: "+tt.yaml+"}", &m))

			var got fakeConnectorConfig

			err := m.Connectors["claude"].Decode(&got)
			require.Equal(t, tt.wantErr, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSectionDecodeTarget(t *testing.T) {
	t.Parallel()

	var nilTarget *fakeConnectorConfig

	tests := []struct {
		name   string
		target any
	}{
		{name: "not a pointer", target: fakeConnectorConfig{}},
		{name: "nil pointer", target: nilTarget},
		{name: "nil", target: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, Section{Node: &yaml.Node{}}.Decode(tt.target), errNotPointer)
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yaml")
	require.NoError(t, os.WriteFile(valid, []byte(minimalYAML(t)), 0o600))

	invalid := filepath.Join(dir, "invalid.yaml")
	require.NoError(t, os.WriteFile(invalid, []byte(minimalYAML(t)+"servr: {}\n"), 0o600))

	tests := []struct {
		name       string
		path       string
		wantErr    string
		wantErrors Errors
	}{
		{name: "valid file", path: valid},
		{name: "missing file", path: filepath.Join(dir, "missing.yaml"), wantErr: "read config: open " + filepath.Join(dir, "missing.yaml") + ": no such file or directory"},
		{
			name:       "file with problems",
			path:       invalid,
			wantErr:    "load config " + invalid + `: servr: unknown key (did you mean "server"?)`,
			wantErrors: Errors{{Path: "servr", Message: `unknown key (did you mean "server"?)`}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(t.Context(), tt.path, testOptions())
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.Nil(t, cfg)

				if tt.wantErrors != nil {
					var errs Errors
					require.ErrorAs(t, err, &errs)
					require.Equal(t, tt.wantErrors, errs)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, "default", cfg.Routes[0].Name)
		})
	}
}

func TestDatabaseURL(t *testing.T) {
	t.Parallel()

	ref := registry.SecretRef{Name: "sidekick-db", Key: "url"}

	tests := []struct {
		name    string
		content string
		wantURL string
		wantErr string
	}{
		{
			name:    "trailing newline removed",
			content: "postgres://db/sidekick\n",
			wantURL: "postgres://db/sidekick",
		},
		{
			name:    "missing file",
			wantErr: "read database.urlSecret: resolve text secret: read secret sidekick-db/url: open file: openat sidekick-db/url: no such file or directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.content != "" {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, ref.Name), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ref.Name, ref.Key), []byte(tt.content), 0o600))
			}

			cfg := &Config{Database: Database{URLSecret: ref}, Secrets: Secrets{Dir: dir}}

			got, err := cfg.DatabaseURL(t.Context(), nil)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantURL, got)
		})
	}
}

func TestRoutesInOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		routes []Route
		want   []string
	}{
		{name: "no routes", routes: nil, want: []string{}},
		{
			name: "priority, then name, default last",
			routes: []Route{
				{Name: "default", Priority: 0},
				{Name: "zeta", Priority: 10},
				{Name: "beta", Priority: 100},
				{Name: "alpha", Priority: 100},
				{Name: "first", Priority: -5},
			},
			want: []string{"first", "zeta", "alpha", "beta", "default"},
		},
		{
			name:   "default route sorts after a later route",
			routes: []Route{{Name: "a"}, {Name: "default"}, {Name: "b"}},
			want:   []string{"a", "b", "default"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{Routes: tt.routes}

			got := []string{}
			for _, r := range cfg.RoutesInOrder() {
				got = append(got, r.Name)
			}

			require.Equal(t, tt.want, got)
		})
	}
}

func TestAccessors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "price table in micro-dollars",
			check: func(t *testing.T) {
				t.Helper()

				m := Model{Prices: map[string]Price{"m": {InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75}}}
				require.Equal(t, map[string]model.Price{"m": {
					InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, CacheReadPerMTok: 300_000, CacheWritePerMTok: 3_750_000,
				}}, m.PriceTable())
			},
		},
		{
			name: "report rules",
			check: func(t *testing.T) {
				t.Helper()
				require.Equal(t, report.Rules{MinFalsePositiveConfidence: report.Medium}, Report{MinFalsePositiveConfidence: report.Medium}.Rules())
				require.Equal(t, report.Rules{}, Report{}.Rules())
			},
		},
		{
			name: "log levels",
			check: func(t *testing.T) {
				t.Helper()

				levels := map[LogLevel]slog.Level{
					LogLevelDebug: slog.LevelDebug,
					LogLevelInfo:  slog.LevelInfo,
					LogLevelWarn:  slog.LevelWarn,
					LogLevelError: slog.LevelError,
				}
				for name, want := range levels {
					require.Equal(t, want, Telemetry{LogLevel: name}.Level(), name)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t)
		})
	}
}

// nodeStrings lists the scalar values of a flat mapping node, keys and
// values in order.
func nodeStrings(n *yaml.Node) []string {
	out := []string{}
	for _, c := range n.Content {
		out = append(out, c.Value)
	}

	return out
}
