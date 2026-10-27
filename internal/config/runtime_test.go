package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
)

func TestCheckRuntimeRoute(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), exampleYAML(t), testOptions())
	require.NoError(t, err)

	route := func(change func(r *Route)) Route {
		r := NewRoute("checkout")
		r.Match = &Match{Service: "checkout-*"}
		r.Outputs = []string{"slack-thread"}

		if change != nil {
			change(&r)
		}

		return r
	}

	tests := []struct {
		name      string
		route     Route
		listeners []string
		want      []string
	}{
		{name: "a valid route", route: route(nil)},
		{
			name:      "a match on a runtime listener",
			route:     route(func(r *Route) { r.Match = &Match{Intake: []string{"ops"}} }),
			listeners: []string{"ops"},
		},
		{
			name:  "the default route",
			route: route(func(r *Route) { r.Name = DefaultRouteName }),
			want:  []string{"routes.default: the default route is set only in the config file"},
		},
		{
			name:  "the name of a file route",
			route: route(func(r *Route) { r.Name = "payments-latency" }),
			want:  []string{`routes.payments-latency: route "payments-latency" is defined in the config file, which only a config change can alter`},
		},
		{
			name:  "a bad name",
			route: route(func(r *Route) { r.Name = "Checkout" }),
			want:  []string{`routes.Checkout: route name "Checkout" ` + nameRule},
		},
		{
			name:  "no match",
			route: route(func(r *Route) { r.Match = nil }),
			want:  []string{"routes.checkout.match: required: a runtime route needs a match, or it would take every event before the default route"},
		},
		{
			name:  "a budget above the file ceiling",
			route: route(func(r *Route) { r.Budgets = Limits{MaxToolCalls: 80, MaxWallTime: time.Hour} }),
			want: []string{
				"routes.checkout.budgets.maxToolCalls: 80 is above the global ceiling budgets.perInvestigation.maxToolCalls 40",
				"routes.checkout.budgets.maxWallTime: 1h0m0s is above the global ceiling budgets.perInvestigation.maxWallTime 10m0s",
			},
		},
		{
			name: "names that the file does not define",
			route: route(func(r *Route) {
				r.Sources = []string{"signos"}
				r.Outputs = []string{"board"}
				r.Strategy = "nope"
				r.Match = &Match{Intake: []string{"ops"}}
			}),
			want: []string{
				`routes.checkout.match.intake[0]: intake "ops" is not defined`,
				`routes.checkout.outputs[0]: output "board" is not defined`,
				`routes.checkout.sources[0]: source "signos" is not defined (did you mean "signoz"?)`,
				`routes.checkout.strategy: strategy "nope" is not defined`,
			},
		},
		{
			name:  "a mode other than read-only",
			route: route(func(r *Route) { r.Mode = action.Approved }),
			want:  []string{`routes.checkout.mode: mode "approved" is not available yet: routes are read-only until action modes arrive in M4`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := cfg.CheckRuntimeRoute(t.Context(), tt.route, tt.listeners)
			requireProblems(t, err, tt.want)
		})
	}
}

func TestCheckRuntimeListener(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), exampleYAML(t), testOptions())
	require.NoError(t, err)

	tests := []struct {
		name     string
		listener string
		config   Listener
		want     []string
	}{
		{
			name:     "a valid listener",
			listener: "ops",
			config:   Listener{Channel: "#ops", Match: ListenerMatch{Sender: ListenerSender{Kind: event.SenderBot, Names: []string{"Grafana"}}, Text: "(?i)firing"}},
		},
		{
			name:     "the name of a file listener",
			listener: "alerts",
			config:   Listener{Channel: "#alerts"},
			want:     []string{`listeners.alerts: listener "alerts" is defined in the config file, which only a config change can alter`},
		},
		{
			name:     "a bad channel and pattern",
			listener: "ops",
			config:   Listener{Channel: "ops", Match: ListenerMatch{Text: "(firing"}},
			want: []string{
				`listeners.ops.channel: expected a channel name such as "#alerts" or a channel ID such as C01ABCDEF, got "ops"`,
				"listeners.ops.match.text: invalid regular expression: error parsing regexp: missing closing ): `(firing`",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := cfg.CheckRuntimeListener(t.Context(), tt.listener, tt.config)
			requireProblems(t, err, tt.want)
		})
	}
}

func TestCheckRuntimeListenerWithoutSlackEvents(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), []byte(minimalYAML(t)), testOptions())
	require.NoError(t, err)

	err = cfg.CheckRuntimeListener(t.Context(), "ops", Listener{Channel: "#ops"})
	requireProblems(t, err, []string{
		"listeners.ops: listeners need the Slack app to receive events: set slack.appTokenSecret, or slack.mode: events with slack.signingSecret",
	})
}

// requireProblems checks that err lists exactly the problems in want, or is
// nil when want is empty.
func requireProblems(t *testing.T, err error, want []string) {
	t.Helper()

	if len(want) == 0 {
		require.NoError(t, err)

		return
	}

	var problems Errors
	require.ErrorAs(t, err, &problems)

	got := make([]string, len(problems))
	for i, p := range problems {
		got[i] = p.String()
	}

	require.Equal(t, want, got)
}
