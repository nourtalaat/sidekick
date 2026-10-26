package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

func TestValidateFixtures(t *testing.T) {
	t.Parallel()

	const readOnlyUntilM4 = "routes are read-only until action modes arrive in M4"

	tests := []struct {
		file string
		want []string
	}{
		// Schema
		{file: "unknown-key", want: []string{`server.listn: unknown key (did you mean "listen"?)`}},
		{file: "bad-duration", want: []string{`budgets.perInvestigation.maxWallTime: expected a duration such as 30s or 10m, got "10x"`}},
		{file: "plugin-unknown-key", want: []string{`model.connectors.claude.thinkng: unknown key (did you mean "thinking"?)`}},
		{file: "bad-regex", want: []string{"routes[0].match.text: invalid regular expression: error parsing regexp: missing closing ): `(?i)latency(`"}},
		{file: "bad-glob", want: []string{`routes[0].match.service: invalid glob "checkout-[": syntax error in pattern`}},
		// Names
		{file: "bad-name", want: []string{`model.connectors.Claude: name "Claude" ` + nameRule}},
		{file: "duplicate-route", want: []string{`routes[1].name: route "payments" is already defined at routes[0]`}},
		{file: "route-name-missing", want: []string{"routes[0].name: required"}},
		{file: "route-name-bad", want: []string{`routes[0].name: route name "checkout latency" ` + nameRule}},
		// Models
		{file: "unknown-provider", want: []string{`model.connectors.claude.provider: model provider "antropic" is not registered (did you mean "anthropic"?)`}},
		{file: "missing-provider", want: []string{"model.connectors.claude.provider: required"}},
		{file: "unsupported-effort", want: []string{`model.connectors.claude.effort: "none" is not supported by claude-opus-5-5`}},
		{file: "plugin-validate", want: []string{"outputs.slack-thread.channel: required"}},
		// Roles
		{file: "missing-investigate", want: []string{"model.roles.investigate: required"}},
		{file: "undefined-role-connector", want: []string{`model.roles.investigate: connector "claud" is not defined (did you mean "claude"?)`}},
		{file: "unknown-role", want: []string{`model.roles.sumarize: unknown role (did you mean "summarize"?)`}},
		{file: "no-tool-calling", want: []string{`model.roles.investigate: connector "local" cannot call tools, which the investigate role needs`}},
		{file: "route-role-no-tool-calling", want: []string{`routes[0].roles.investigate: connector "local" cannot call tools, which the investigate role needs`}},
		// Sources
		{file: "unknown-source-type", want: []string{`sources.k8s.type: source type "kubernets" is not registered (did you mean "kubernetes"?)`}},
		{file: "source-tool-name-too-long", want: []string{`sources.signoz.tools.rename.search_logs: tool "signoz__search_logs_in_every_index_of_every_cluster_in_the_whole_company" is longer than 64 characters: invalid name`}},
		{file: "source-bad-tool-glob", want: []string{`sources.signoz.tools.deny[1]: invalid glob "*delete[": syntax error in pattern`}},
		{file: "source-negative-limits", want: []string{"sources.signoz.concurrency: must not be negative", "sources.signoz.timeout: must not be negative"}},
		{file: "source-mcp-timeout-above-limit", want: []string{"sources.signoz.timeout: 1m1s is above 1m0s, the longest the HTTP client waits for response headers with transport http"}},
		{file: "source-mcp-oauth", want: []string{"sources.sentry.oauth: MCP sources do not support OAuth yet; put a token in a Secret and send it with headers (or env for stdio), see docs/sources/mcp-authentication.md"}},
		// Secrets
		{file: "secret-missing-key", want: []string{"database.urlSecret.key: required"}},
		{file: "plugin-secret-shape", want: []string{`sources.signoz.headers.Authorization.secret.name: "../signoz" must not be "." or ".." or contain "/" or "\"`}},
		{file: "missing-database", want: []string{"database.urlSecret: required"}},
		// Routes
		{file: "no-default-route", want: []string{`routes: a route named "default" is required; it takes every event no other route matches`}},
		{file: "default-with-match", want: []string{"routes[0].match: the default route must not have a match"}},
		{file: "undefined-output", want: []string{`routes[0].outputs[1]: output "slak-thread" is not defined (did you mean "slack-thread"?)`}},
		{file: "undefined-source", want: []string{`routes[0].sources[0]: source "signz" is not defined (did you mean "signoz"?)`}},
		{file: "undefined-intake", want: []string{`routes[0].match.intake[0]: intake "alertmanger" is not defined (did you mean "alertmanager"?)`}},
		{file: "mode-approved", want: []string{`routes[0].mode: mode "approved" is not available yet: ` + readOnlyUntilM4}},
		{file: "unknown-mode", want: []string{`routes[0].mode: unknown mode "readonly" (did you mean "read-only"?)`}},
		{file: "actions-set", want: []string{"routes[0].actions: must be empty: " + readOnlyUntilM4}},
		{file: "undefined-strategy", want: []string{`routes[0].strategy: strategy "generic-alrt" is not defined (did you mean "generic-alert"?)`}},
		{file: "bad-strategy-file", want: []string{`strategies.dir: fixture/strategies-invalid/generic-alert.yaml: line 3: unknown key "frist"`}},
		{file: "unknown-severity", want: []string{`routes[0].match.severity[0]: unknown severity "critcal" (did you mean "critical"?)`}},
		// Budgets
		{file: "route-tool-calls-above-ceiling", want: []string{"routes[0].budgets.maxToolCalls: 50 is above the global ceiling budgets.perInvestigation.maxToolCalls 40"}},
		{file: "route-wall-time-above-ceiling", want: []string{"routes[0].budgets.maxWallTime: 15m0s is above the global ceiling budgets.perInvestigation.maxWallTime 10m0s"}},
		{file: "route-budget-negative", want: []string{
			"routes[0].budgets.maxToolCalls: must not be negative",
			"routes[0].budgets.maxWallTime: must not be negative",
		}},
		{file: "global-budget-zero", want: []string{"budgets.perInvestigation.maxToolCalls: must be positive"}},
		{file: "route-concurrency-above-global", want: []string{
			"routes[0].concurrency: 12 is above the global limit budgets.concurrency 8",
			"routes[1].concurrency: must not be negative",
		}},
		{file: "global-concurrency-zero", want: []string{"budgets.concurrency: must be positive"}},
		// Report
		{file: "unknown-confidence", want: []string{`report.minFalsePositiveConfidence: unknown confidence "hgh" (did you mean "high"?)`}},
		// Intake
		{file: "webhook-two-auth", want: []string{"intake.webhooks.ci.auth: set exactly one of bearer and hmac, not both"}},
		{file: "webhook-bad-name", want: []string{`intake.webhooks["alerts/prod"]: name "alerts/prod" ` + nameRule}},
		{file: "webhook-tolerance", want: []string{"intake.webhooks.ci.auth.tolerance: must be positive"}},
		{file: "webhook-no-auth", want: []string{"intake.webhooks.ci.auth: set bearer or hmac; every webhook endpoint must authenticate its deliveries"}},
		// Other sections
		{file: "bad-listen", want: []string{`server.listen: expected host:port such as ":8080", got "8080"`}},
		{file: "bad-public-url", want: []string{`server.publicURL: expected an absolute http or https URL, got "sidekick.example.com"`}},
		{file: "bad-migrations", want: []string{`database.migrations: expected startup or manual, got "manul" (did you mean "manual"?)`}},
		{file: "bad-log-level", want: []string{`telemetry.logLevel: unknown log level "debg" (did you mean "debug"?)`}},
		{file: "bad-otlp-endpoint", want: []string{`telemetry.otlpEndpoint: expected an absolute URL such as http://otel-collector:4317, got "otel-collector:4317"`}},
		{file: "bad-redaction-pattern", want: []string{"redaction.custom[0].pattern: invalid regular expression: error parsing regexp: missing closing ]: `[A-Za-z0-9{14}`"}},
		{file: "redaction-rule-names", want: []string{
			"redaction.custom[0].name: required",
			`redaction.custom[2].name: rule "customer_id" is defined more than once`,
			"redaction.custom[2].pattern: required",
		}},
		{file: "redaction-built-in-names", want: []string{
			`redaction.builtIn[0]: unknown built-in rule "emals" (did you mean "emails"?)`,
			`redaction.builtIn[2]: unknown built-in rule "ipaddresses" (did you mean "ip_addresses"?)`,
			`redaction.custom[0].name: rule name "emails" is the name of a built-in rule`,
			`redaction.custom[1].name: rule name "Customer ID" must start with a lower-case letter and hold only lower-case letters, digits, "_" and "-", at most 32 characters`,
		}},
		{file: "slack-without-token", want: []string{"slack.botTokenSecret: required"}},
		{file: "slack-output-without-slack", want: []string{"outputs.slack-thread: type slack needs slack.botTokenSecret"}},
		{file: "negative-price", want: []string{"model.prices.claude-opus-5-5.inputPerMTok: must not be negative"}},
		// Several problems, reported together and sorted by path.
		{file: "three-errors", want: []string{
			`report.minFalsePositiveConfidence: unknown confidence "hgh" (did you mean "high"?)`,
			`routes[0].mode: mode "read-write" is not available yet: ` + readOnlyUntilM4,
			`routes[0].outputs[0]: output "slak-thread" is not defined (did you mean "slack-thread"?)`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), readFixture(t, "invalid/"+tt.file+".yaml"), testOptions())
			require.Nil(t, cfg)

			var errs Errors
			require.ErrorAs(t, err, &errs)
			require.Equal(t, strings.Join(tt.want, "\n"), errs.Error())
		})
	}
}

func TestValidateValidFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data func(t *testing.T) []byte
	}{
		{name: "minimal", data: func(t *testing.T) []byte { t.Helper(); return readFixture(t, "valid/minimal.yaml") }},
		{name: "example", data: exampleYAML},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), tt.data(t), testOptions())
			require.NoError(t, err)
			require.NotNil(t, cfg)
		})
	}
}

func TestValidateStrategies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		wantErr string
		wantLog string
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "a route without a strategy uses generic-alert",
			file: "valid/minimal.yaml",
			check: func(t *testing.T, cfg *Config) {
				t.Helper()
				require.Equal(t, DefaultStrategy, cfg.Routes[0].Strategy)

				_, ok := cfg.StrategySet().Get(DefaultStrategy)
				require.True(t, ok)
			},
		},
		{
			name:    "a strategies dir that cannot be read",
			file:    "invalid/strategies-dir-not-dir.yaml",
			wantErr: "strategies.dir: load file strategies: read strategies dir fixture/valid/minimal.yaml: list files: ",
		},
		{
			name:    "a file strategy that overrides a built-in one is logged",
			file:    "valid/override-strategy.yaml",
			wantLog: `msg="file strategy overrides built-in strategy" strategy=generic-alert file=fixture/strategies-override/generic-alert.yaml`,
			check: func(t *testing.T, cfg *Config) {
				t.Helper()

				st, ok := cfg.StrategySet().Get(DefaultStrategy)
				require.True(t, ok)
				require.Equal(t, strategy.File, st.Origin)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			opts := testOptions()
			opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))

			cfg, err := Parse(t.Context(), readFixture(t, tt.file), opts)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Contains(t, logs.String(), tt.wantLog)
			tt.check(t, cfg)
		})
	}
}

func TestValidateCheckSecrets(t *testing.T) {
	t.Parallel()

	dbURL := registry.SecretRef{Name: "sidekick-db", Key: "url"}
	signoz := registry.SecretRef{Name: "signoz-mcp", Key: "token"}

	withSource := minimalYAML(t) + `
sources:
  signoz:
    type: mcp
    url: https://signoz-mcp.internal/mcp
    headers:
      Authorization: { secret: { name: signoz-mcp, key: token }, prefix: "Bearer " }
`

	tests := []struct {
		name       string
		yaml       string
		present    []registry.SecretRef
		unreadable []registry.SecretRef
		want       []string
	}{
		{name: "every file is readable", yaml: withSource, present: []registry.SecretRef{dbURL, signoz}},
		{
			name: "missing core and plugin secrets",
			yaml: withSource,
			want: []string{
				"database.urlSecret: cannot read secret sidekick-db/url: the file does not exist",
				"sources.signoz.headers.Authorization.secret: cannot read secret signoz-mcp/token: the file does not exist",
			},
		},
		{
			name:       "file without read permission",
			yaml:       minimalYAML(t),
			unreadable: []registry.SecretRef{dbURL},
			want:       []string{"database.urlSecret: cannot read secret sidekick-db/url: permission denied"},
		},
		{
			name: "references with a bad shape are not read",
			yaml: strings.Replace(minimalYAML(t), "{ name: sidekick-db, key: url }", "{ name: sidekick-db }", 1),
			want: []string{"database.urlSecret.key: required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeSecrets(t, append(tt.present, tt.unreadable...)...)
			for _, ref := range tt.unreadable {
				require.NoError(t, os.Chmod(filepath.Join(dir, ref.Name, ref.Key), 0))
			}

			yaml := tt.yaml + "\nsecrets:\n  dir: " + dir + "\n"
			opts := testOptions()
			opts.CheckSecrets = true

			_, err := Parse(t.Context(), []byte(yaml), opts)
			if tt.want == nil {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, strings.Join(tt.want, "\n"))
		})
	}
}

func TestValidateCheckSecretsResolver(t *testing.T) {
	t.Parallel()

	errBroken := errors.New("resolver broken")

	tests := []struct {
		name       string
		setupMocks func(s *registry.MockSecrets)
		want       string
	}{
		{
			name: "resolver given in the options is used",
			setupMocks: func(s *registry.MockSecrets) {
				s.EXPECT().Resolve(gomock.Any(), registry.SecretRef{Name: "sidekick-db", Key: "url"}).Return([]byte("secret value"), nil)
			},
		},
		{
			name: "other resolver errors are reported without the value",
			setupMocks: func(s *registry.MockSecrets) {
				s.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, errBroken)
			},
			want: "database.urlSecret: cannot read secret sidekick-db/url: resolver broken",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := registry.NewMockSecrets(gomock.NewController(t))
			tt.setupMocks(s)

			opts := testOptions()
			opts.CheckSecrets = true
			opts.Secrets = s

			_, err := Parse(t.Context(), readFixture(t, "valid/minimal.yaml"), opts)
			if tt.want == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.want)
			require.NotContains(t, err.Error(), "secret value")
		})
	}
}

func TestValidateUsesGlobalRegistries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{name: "no types are registered yet", want: `model.connectors.claude.provider: model provider "anthropic" is not registered`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse(t.Context(), readFixture(t, "valid/minimal.yaml"), LoadOptions{})
			require.EqualError(t, err, tt.want)
		})
	}
}

func TestPluginErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want Errors
	}{
		{
			name: "decode errors keep their paths",
			err: fmt.Errorf("check source %q: %w", "x", &registry.StageError{
				Stage: registry.StageDecode,
				Err:   Errors{{Path: "sources.x.a", Message: "unknown key"}},
			}),
			want: Errors{{Path: "sources.x.a", Message: "unknown key"}},
		},
		{
			name: "validate errors move to their fields",
			err: fmt.Errorf("check source %q: %w", "x", &registry.StageError{
				Stage: registry.StageValidate,
				Err:   errors.Join(errors.New("url: required"), errors.New("tools.allow[0]: bad"), errors.New("no field here")),
			}),
			want: Errors{
				{Path: "sources.x.url", Message: "required"},
				{Path: "sources.x.tools.allow[0]", Message: "bad"},
				{Path: "sources.x", Message: "no field here"},
			},
		},
		{
			name: "field given as an index",
			err:  &registry.StageError{Stage: registry.StageValidate, Err: errors.New(`["a.b"]: bad`)},
			want: Errors{{Path: `sources.x["a.b"]`, Message: "bad"}},
		},
		{
			name: "constructor failure that looks like a field",
			err:  fmt.Errorf("build source %q: %w", "x", &registry.StageError{Stage: registry.StageConstruct, Err: errors.New("dial: connection refused")}),
			want: Errors{{Path: "sources.x", Message: "cannot build: dial: connection refused"}},
		},
		{
			name: "error without a stage keeps its whole text",
			err:  fmt.Errorf("build source %q: %w", "x", registry.ErrUnknownType),
			want: Errors{{Path: "sources.x", Message: `build source "x": unknown type`}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, pluginErrors("sources.x", tt.err))
		})
	}
}

func TestFieldErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		err  error
		want Errors
	}{
		{name: "nil", path: "a", err: nil, want: nil},
		{name: "empty path", path: "", err: errors.New("name: required"), want: Errors{{Path: "name", Message: "required"}}},
		{name: "nested field", path: "a", err: errors.New("thinking.mode: bad"), want: Errors{{Path: "a.thinking.mode", Message: "bad"}}},
		{name: "sentence is not a field", path: "a", err: errors.New("dial failed: refused"), want: Errors{{Path: "a", Message: "dial failed: refused"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, fieldErrors(tt.path, tt.err))
		})
	}
}
