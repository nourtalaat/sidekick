package rules_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// fileConfig is a loaded config with two file routes, a file listener and
// the Slack app receiving events. Built by hand, it needs no plugin
// registries.
func fileConfig() *config.Config {
	payments := config.NewRoute("payments")
	payments.Priority = 50
	payments.Match = &config.Match{Service: "payments-*"}
	payments.Outputs = []string{"slack-thread"}

	def := config.NewRoute(config.DefaultRouteName)
	def.Outputs = []string{"slack-thread"}

	return &config.Config{
		Slack: &config.Slack{
			BotTokenSecret: registry.SecretRef{Name: "sidekick-slack", Key: "botToken"},
			AppTokenSecret: registry.SecretRef{Name: "sidekick-slack", Key: "appToken"},
			Mode:           config.SlackModeSocket,
		},
		Sources:   map[string]config.Section{"signoz": {Type: "mcp"}},
		Outputs:   map[string]config.Section{"slack-thread": {Type: "slack"}},
		Listeners: map[string]config.Listener{"alerts": {Channel: "#alerts"}},
		Routes:    []config.Route{def, payments},
		Budgets: config.Budgets{
			PerInvestigation: config.Limits{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute},
			Concurrency:      8,
		},
	}
}

func runtimeRoute(name string, priority int, change func(r *config.Route)) rules.Route {
	r := config.NewRoute(name)
	r.Priority = priority
	r.Match = &config.Match{Service: name + "-*"}
	r.Outputs = []string{"slack-thread"}

	if change != nil {
		change(&r)
	}

	return rules.Route{Config: r, Enabled: true, Version: 1}
}

func runtimeListener(name, channel string) rules.Listener {
	return rules.Listener{
		Name:    name,
		Config:  config.Listener{Channel: channel, Match: config.ListenerMatch{Sender: config.ListenerSender{Kind: event.SenderBot}}},
		Enabled: true,
		Version: 1,
	}
}

func TestMergeRoutes(t *testing.T) {
	t.Parallel()

	type named struct {
		Name  string
		Layer rules.Layer
	}

	tests := []struct {
		name         string
		runtime      rules.Runtime
		want         []named
		wantShadowed []rules.Skipped
		wantInvalid  []string
		wantDisabled []string
	}{
		{
			name: "the file routes alone",
			want: []named{{"payments", rules.LayerFile}, {"default", rules.LayerFile}},
		},
		{
			name: "by priority, then file before runtime, then by name, with default last",
			runtime: rules.Runtime{Routes: []rules.Route{
				runtimeRoute("zeta", 50, nil),
				runtimeRoute("alpha", 50, nil),
				runtimeRoute("early", 10, nil),
				runtimeRoute("late", 1000, nil),
			}},
			want: []named{
				{"early", rules.LayerRuntime},
				{"payments", rules.LayerFile},
				{"alpha", rules.LayerRuntime},
				{"zeta", rules.LayerRuntime},
				{"late", rules.LayerRuntime},
				{"default", rules.LayerFile},
			},
		},
		{
			name:         "a runtime route with the name of a file route is shadowed",
			runtime:      rules.Runtime{Routes: []rules.Route{runtimeRoute("payments", 1, nil)}},
			want:         []named{{"payments", rules.LayerFile}, {"default", rules.LayerFile}},
			wantShadowed: []rules.Skipped{{Kind: rules.KindRoute, Name: "payments", Reason: "a rule in the config file has the same name"}},
		},
		{
			name:         "a runtime default route is shadowed and the file default stays last",
			runtime:      rules.Runtime{Routes: []rules.Route{runtimeRoute(config.DefaultRouteName, 1, nil)}},
			want:         []named{{"payments", rules.LayerFile}, {"default", rules.LayerFile}},
			wantShadowed: []rules.Skipped{{Kind: rules.KindRoute, Name: "default", Reason: "a rule in the config file has the same name"}},
		},
		{
			name: "a runtime route the file no longer allows is left out",
			runtime: rules.Runtime{Routes: []rules.Route{
				runtimeRoute("checkout", 10, func(r *config.Route) { r.Sources = []string{"removed"} }),
				runtimeRoute("greedy", 10, func(r *config.Route) { r.Budgets.MaxToolCalls = 100 }),
			}},
			want:        []named{{"payments", rules.LayerFile}, {"default", rules.LayerFile}},
			wantInvalid: []string{"checkout", "greedy"},
		},
		{
			name: "a disabled runtime route is left out",
			runtime: rules.Runtime{Routes: []rules.Route{
				func() rules.Route { r := runtimeRoute("checkout", 10, nil); r.Enabled = false; return r }(),
			}},
			want:         []named{{"payments", rules.LayerFile}, {"default", rules.LayerFile}},
			wantDisabled: []string{"checkout"},
		},
		{
			name: "a runtime route may match a runtime listener",
			runtime: rules.Runtime{
				Listeners: []rules.Listener{runtimeListener("ops", "#ops")},
				Routes: []rules.Route{
					runtimeRoute("ops", 10, func(r *config.Route) { r.Match = &config.Match{Intake: []string{"ops"}} }),
				},
			},
			want: []named{{"ops", rules.LayerRuntime}, {"payments", rules.LayerFile}, {"default", rules.LayerFile}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := rules.Merge(t.Context(), fileConfig(), tt.runtime)

			got := make([]named, len(e.Routes))
			for i, r := range e.Routes {
				got[i] = named{r.Config.Name, r.Layer}
			}

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantShadowed, e.Shadowed)
			require.Equal(t, tt.wantInvalid, skippedNames(e.Invalid))
			require.Equal(t, tt.wantDisabled, skippedNames(e.Disabled))
		})
	}
}

func TestMergeFillsRuntimeBudgets(t *testing.T) {
	t.Parallel()

	e := rules.Merge(t.Context(), fileConfig(), rules.Runtime{Routes: []rules.Route{
		runtimeRoute("checkout", 10, func(r *config.Route) { r.Budgets.MaxToolCalls = 20 }),
	}})

	require.Equal(t, "checkout", e.Routes[0].Config.Name)
	require.Equal(t, 20, e.Routes[0].Config.Budgets.MaxToolCalls)
	require.Equal(t, 10*time.Minute, e.Routes[0].Config.Budgets.MaxWallTime)
}

func TestMergeListeners(t *testing.T) {
	t.Parallel()

	type named struct {
		Name  string
		Layer rules.Layer
	}

	tests := []struct {
		name         string
		runtime      []rules.Listener
		want         []named
		wantShadowed []string
		wantInvalid  []string
	}{
		{
			name: "the file listeners alone",
			want: []named{{"alerts", rules.LayerFile}},
		},
		{
			name:    "file and runtime listeners in name order",
			runtime: []rules.Listener{runtimeListener("ops", "#ops"), runtimeListener("billing", "C01BILLING")},
			want:    []named{{"alerts", rules.LayerFile}, {"billing", rules.LayerRuntime}, {"ops", rules.LayerRuntime}},
		},
		{
			name:         "a runtime listener with the name of a file listener is shadowed",
			runtime:      []rules.Listener{runtimeListener("alerts", "#other")},
			want:         []named{{"alerts", rules.LayerFile}},
			wantShadowed: []string{"alerts"},
		},
		{
			name:        "a runtime listener that fails the checks is left out",
			runtime:     []rules.Listener{runtimeListener("ops", "ops")},
			want:        []named{{"alerts", rules.LayerFile}},
			wantInvalid: []string{"ops"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := rules.Merge(t.Context(), fileConfig(), rules.Runtime{Listeners: tt.runtime})

			got := make([]named, len(e.Listeners))
			for i, l := range e.Listeners {
				got[i] = named{l.Name, l.Layer}
			}

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantShadowed, skippedNames(e.Shadowed))
			require.Equal(t, tt.wantInvalid, skippedNames(e.Invalid))
			require.Len(t, e.ListenerConfigs(), len(tt.want))
		})
	}
}

func skippedNames(skipped []rules.Skipped) []string {
	var names []string
	for _, s := range skipped {
		names = append(names, s.Name)
	}

	return names
}
