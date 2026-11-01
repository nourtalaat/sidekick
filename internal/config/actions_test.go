package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestActionsSection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		file         string
		wantTTL      time.Duration
		wantMax      int
		wantNames    []string
		wantRouteAct []string
	}{
		{name: "the defaults", file: "valid/minimal.yaml", wantTTL: 24 * time.Hour, wantMax: 5},
		{
			name: "settings and an action", file: "valid/actions.yaml", wantTTL: 12 * time.Hour, wantMax: 3,
			wantNames: []string{"monitor-note"}, wantRouteAct: []string{"monitor-note"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), readFixture(t, tt.file), testOptions())
			require.NoError(t, err)

			require.Equal(t, tt.wantTTL, cfg.Actions.ProposalTTL)
			require.Equal(t, tt.wantMax, cfg.Actions.MaxPerInvestigation)
			require.Equal(t, tt.wantNames, sortedKeys(cfg.Actions.Definitions))
			require.Equal(t, tt.wantRouteAct, cfg.Routes[0].Actions)

			in, err := Build(t.Context(), cfg, testRegistries(), registry.Deps{})
			require.NoError(t, err)
			require.Equal(t, tt.wantNames, sortedKeys(in.Actions))

			for name, a := range in.Actions {
				require.Equal(t, name, a.Spec().Name)
			}
		})
	}
}

func TestCheckRuntimeRouteActions(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), readFixture(t, "valid/actions.yaml"), testOptions())
	require.NoError(t, err)

	route := NewRoute("checkout")
	route.Match = &Match{Service: "checkout-*"}
	route.Mode = action.Approved
	route.Actions = []string{"monitor-note"}

	require.NoError(t, cfg.CheckRuntimeRoute(t.Context(), route, nil))

	route.Actions = []string{"monitor-note", "other"}
	requireProblems(t, cfg.CheckRuntimeRoute(t.Context(), route, nil), []string{
		`routes.checkout.actions[1]: action "other" is not defined`,
	})
}
