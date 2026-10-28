package router

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/watch"
)

func TestNewWatchPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy *watch.Policy
		// want are the names of the watchers of the service-down route.
		want []string
	}{
		{name: "the config's policy", want: []string{"checkout-health", "checkout-latency"}},
		{name: "an empty policy starts no watchers", policy: &watch.Policy{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile("fixture/watchers.yaml")
			require.NoError(t, err)

			cfg, err := config.Parse(t.Context(), []byte(strings.ReplaceAll(string(data), "HEALTH_URL", "http://127.0.0.1/healthz")),
				config.LoadOptions{Registries: config.Registries{Models: fakeModels()}})
			require.NoError(t, err)

			r, err := New(Options{Config: cfg, WatchPolicy: tt.policy})
			require.NoError(t, err)

			var got []string

			for _, rt := range *r.routes.Load() {
				if rt.name != "service-down" {
					continue
				}

				for _, d := range rt.watchers {
					got = append(got, d.Name)
				}
			}

			require.Equal(t, tt.want, got)
		})
	}
}

func TestSetRoutesKeepsWatchers(t *testing.T) {
	t.Parallel()

	runtime := config.NewRoute("runtime-checkout")
	runtime.Priority = 1
	runtime.Match = &config.Match{Service: "checkout-*"}

	tests := []struct {
		name  string
		route string
		want  []string
	}{
		{name: "a file route keeps the watchers of the policy", route: "service-down", want: []string{"checkout-health", "checkout-latency"}},
		{name: "a runtime route gets its strategy's watchers, here none", route: "runtime-checkout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile("fixture/watchers.yaml")
			require.NoError(t, err)

			cfg, err := config.Parse(t.Context(), []byte(strings.ReplaceAll(string(data), "HEALTH_URL", "http://127.0.0.1/healthz")),
				config.LoadOptions{Registries: config.Registries{Models: fakeModels()}})
			require.NoError(t, err)

			r, err := New(Options{Config: cfg})
			require.NoError(t, err)
			require.NoError(t, r.SetRoutes(append([]config.Route{runtime}, cfg.RoutesInOrder()...)))

			var (
				got   []string
				found bool
			)

			for _, rt := range *r.routes.Load() {
				if rt.name != tt.route {
					continue
				}

				found = true

				for _, d := range rt.watchers {
					got = append(got, d.Name)
				}
			}

			require.True(t, found)
			require.Equal(t, tt.want, got)
		})
	}
}
