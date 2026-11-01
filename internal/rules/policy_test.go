package rules_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/rules"
)

func TestActionPolicy(t *testing.T) {
	t.Parallel()

	cfg := fileConfig()
	cfg.Routes[1].Mode = action.Approved
	cfg.Routes[1].Actions = []string{"monitor-note"}

	policy := rules.ActionPolicy{Cache: rules.NewCache(t.Context(), rules.Options{File: cfg})}

	tests := []struct {
		name  string
		route string
		want  action.RoutePolicy
		found bool
	}{
		{name: "a route that proposes", route: "payments", want: action.RoutePolicy{Mode: action.Approved, Actions: []string{"monitor-note"}}, found: true},
		{name: "a read-only route", route: config.DefaultRouteName, want: action.RoutePolicy{Mode: action.ReadOnly}, found: true},
		{name: "a route that does not exist", route: "gone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, found := policy.Route(tt.route)
			require.Equal(t, tt.found, found)
			require.Equal(t, tt.want, got)
		})
	}
}
