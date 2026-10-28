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

	const listenersNeedSlack = "listeners: listeners need the Slack app to receive events: set slack.appTokenSecret, or slack.mode: events with slack.signingSecret"

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
		{file: "route-tokens-cost-above-ceiling", want: []string{
			"routes[0].budgets.daily.maxCostUSD: must be a dollar amount that is not negative",
			"routes[0].budgets.maxCostUSD: 2.50 is above the global ceiling budgets.perInvestigation.maxCostUSD 1.00",
			"routes[0].budgets.maxTokens: 500000 is above the global ceiling budgets.perInvestigation.maxTokens 400000",
		}},
		{file: "route-daily-above-global", want: []string{
			"routes[0].budgets.daily.maxCostUSD: 20.00 is above the global cap budgets.daily.maxCostUSD 10.00",
			`routes[0].budgets.daily.onDailyCap: unknown value "wiat" (did you mean "wait"?)`,
		}},
		{file: "daily-bad-time-zone", want: []string{
			"budgets.daily.maxCostUSD: must be a dollar amount that is not negative",
			`budgets.daily.timeZone: unknown time zone "Mars/Olympus": use an IANA name such as UTC or Europe/Berlin`,
			"budgets.perInvestigation.maxCostUSD: must be a positive dollar amount",
			"budgets.perInvestigation.maxTokens: must be positive",
		}},
		{file: "rate-limit-invalid", want: []string{
			"budgets.rateLimit.events: must be positive",
			`budgets.rateLimit.overLimit: unknown value "dely" (did you mean "delay"?)`,
			"budgets.rateLimit.per: must be positive",
		}},
		{file: "route-rate-limit-looser", want: []string{
			"routes[0].rateLimit: 120 events per 1h0m0s is looser than the global limit budgets.rateLimit of 10 events per 10m0s",
		}},
		{file: "route-concurrency-above-global", want: []string{
			"routes[0].concurrency: 12 is above the global limit budgets.concurrency 8",
			"routes[1].concurrency: must not be negative",
		}},
		{file: "route-grouping-invalid", want: []string{
			"routes[0].grouping.fingerprintLabels[1]: must not be empty",
			`routes[0].grouping.fingerprintLabels[2]: label "service" is listed twice`,
			"routes[0].grouping.window: must be positive",
		}},
		{file: "route-related-window-invalid", want: []string{
			"routes[0].grouping.relatedWindow: must not be negative",
			"routes[1].grouping.relatedWindow: 45m0s is longer than the grouping window 30m0s",
		}},
		{file: "global-concurrency-zero", want: []string{"budgets.concurrency: must be positive"}},
		// Report
		{file: "unknown-confidence", want: []string{`report.minFalsePositiveConfidence: unknown confidence "hgh" (did you mean "high"?)`}},
		// Memory
		{file: "memory-examples", want: []string{"memory.examples: must be between 1 and 10, got 11; set memory.enabled to false for none"}},
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
		// Slack app and listeners
		{file: "slack-bad-mode", want: []string{`slack.mode: expected socket or events, got "sockets" (did you mean "socket"?)`}},
		{file: "slack-events-without-signing-secret", want: []string{"slack.signingSecret: required with mode events"}},
		{file: "slack-bad-values", want: []string{
			`slack.apiURL: expected an absolute http or https URL ending in "/", such as https://slack.com/api/, got "https://slack.com/api"`,
			`slack.trustedBots[0]: expected a bot ID such as B01ABCDEF, got "SigNoz"`,
		}},
		{file: "listeners-without-app-token", want: []string{listenersNeedSlack}},
		{file: "listeners-without-slack", want: []string{listenersNeedSlack}},
		{file: "listener-problems", want: []string{
			`listeners.alerts.channel: expected a channel name such as "#alerts" or a channel ID such as C01ABCDEF, got "alerts"`,
			"listeners.alerts.extract[0]: has no named group such as (?P<service>...); only named groups set fields",
			"listeners.alerts.extract[1]: invalid regular expression: error parsing regexp: missing closing ]: `[a-z`",
			`listeners.alerts.match.sender.bots[0]: expected a bot ID such as B01ABCDEF, got "SigNoz"`,
			`listeners.alerts.match.sender.kind: expected human or bot, got "robot" (did you mean "bot"?)`,
			`listeners.alerts.match.sender.users[0]: expected a user ID such as U01ABCDEF, got "bob"`,
			"listeners.alerts.match.text: invalid regular expression: error parsing regexp: missing closing ): `(?i)alert(`",
			"listeners.alerts.recovery: invalid regular expression: error parsing regexp: missing closing ): `resolved(`",
			"listeners.alerts.trusted: has no effect without slack.trustedBots, the bots whose messages are trusted",
		}},
		{file: "intake-name-clash", want: []string{
			`intake.webhooks.slack-mention: name "slack-mention" is reserved for events from mentions of Sidekick in Slack`,
			`listeners.alertmanager: listener "alertmanager" has the name of a webhook endpoint; events of both would share one intake name`,
			`listeners.slack-mention: listener "slack-mention" has the name of a webhook endpoint; events of both would share one intake name`,
			`listeners.slack-mention: name "slack-mention" is reserved for events from mentions of Sidekick in Slack`,
		}},
		{file: "undefined-listener-intake", want: []string{`routes[0].match.intake[0]: intake "alerst" is not defined (did you mean "alerts"?)`}},
		{file: "mention-without-slack", want: []string{`routes[0].match.intake[0]: intake "slack-mention" is not defined`}},
		// Teams bot and listeners
		{file: "teams-bad-values", want: []string{
			"teams: set exactly one of clientSecret and workloadIdentity, not both",
			`teams.appID: expected a client ID such as 00000000-1111-2222-3333-444444444444, got "sidekick-bot"`,
			`teams.serviceURL: expected an https URL ending in "/", such as https://smba.trafficmanager.net/teams/, got "http://smba.trafficmanager.net/teams"`,
			`teams.tenantID: expected a tenant ID such as 00000000-1111-2222-3333-444444444444, got "contoso.onmicrosoft.com"`,
			`teams.trustedBots[0]: expected a bot ID such as 28:00000000-1111-2222-3333-444444444444, got "Grafana"`,
		}},
		{file: "teams-without-credentials", want: []string{
			"teams: set clientSecret or workloadIdentity: true, so the bot can get tokens to post with",
			"teams.appID: required",
			"teams.tenantID: required",
		}},
		{file: "teams-listener-problems", want: []string{
			`listeners.chat.source: expected slack or teams, got "team" (did you mean "teams"?)`,
			`listeners.teams-alerts.channel: expected a Teams channel ID such as 19:abc123@thread.tacv2, got "#alerts"`,
			`listeners.teams-alerts.match.sender.bots[0]: expected a bot ID such as 28:00000000-1111-2222-3333-444444444444, got "B01ABCDEF"`,
			`listeners.teams-alerts.match.sender.users[0]: expected a user's Microsoft Entra object ID such as 00000000-1111-2222-3333-444444444444, got "U01ABCDEF"`,
			"listeners.teams-alerts.trusted: has no effect without teams.trustedBots, the bots whose messages are trusted",
			`listeners.teams-mention: name "teams-mention" is reserved for events from mentions of Sidekick in Teams`,
		}},
		{file: "teams-listeners-without-teams", want: []string{"listeners: listeners with source teams need the teams section, the Teams bot that receives their messages"}},
		{file: "teams-mention-without-teams", want: []string{`routes[0].match.intake[0]: intake "teams-mention" is not defined`}},
		{file: "negative-price", want: []string{"model.prices.claude-opus-5-5.inputPerMTok: must not be negative"}},
		// Watchers
		{file: "watch-http", want: []string{
			`watch.http.allowHosts[1]: host pattern "https://status.example.com": label "https://status": holds ':'; only letters, digits, - and _ are allowed; expected a host name, an IP address, or "*." and a host name`,
			`watch.http.allowHosts[2]: host pattern "*": label "*": holds '*'; only letters, digits, - and _ are allowed; expected a host name, an IP address, or "*." and a host name`,
			`watch.http.allowPrivate[1]: "10.20.0.0/33" is not an address range such as 10.20.0.0/16 or an IP address`,
			`watch.http.allowPrivate[2]: "internal.example.com" is not an address range such as 10.20.0.0/16 or an IP address`,
		}},
		{file: "watchers-invalid", want: []string{
			"routes[0].watcherDefaults.interval: 5s is below routes[0].watcherLimits.minInterval 15s",
			"routes[0].watcherDefaults.interval: 5s is below watch.minInterval 10s",
			`routes[0].watcherLimits.hosts[0]: host pattern "*.example.com" is not inside watch.http.allowHosts`,
			"routes[0].watcherLimits.maxTimeout: 5h0m0s is above watch.maxTimeout 4h0m0s",
			`routes[0].watcherLimits.tools[0]: "nosuch__tool" is not the qualified name of a tool of the route's sources, such as signoz__query_metrics`,
			`routes[0].watcherLimits.types[1]: unknown check type "ping"; expected one of [http tool alert]`,
			`routes[0].watchers[0].check.url: host "other.example.com" is not in watch.http.allowHosts`,
			"routes[0].watchers[0].interval: 20m0s is above watch.maxInterval 15m0s",
			`routes[0].watchers[1].check.source: source "signoz" is not one of the route's sources`,
			`routes[0].watchers[1].name: watcher "health" is already defined at routes[0].watchers[0]`,
			"watch.defaults.timeout: 5h0m0s is above watch.maxTimeout 4h0m0s",
			"watch.maxPerGroup: must be positive",
		}},
		{file: "watcher-built-in-defaults", want: []string{
			"the built-in default interval 30s is below routes[0].watcherLimits.minInterval 45s; set routes[0].watcherDefaults.interval or watch.defaults.interval",
			"the built-in default timeout 1h0m0s is above watch.maxTimeout 30m0s; set watch.defaults.timeout",
		}},
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
		{name: "teams", data: func(t *testing.T) []byte { t.Helper(); return readFixture(t, "valid/teams.yaml") }},
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
