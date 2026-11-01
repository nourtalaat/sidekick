//go:build integration

package router

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
)

func TestInvestigationModeOfAReadWriteRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		event        uuid.UUID
		intake       string
		allowUntrust bool
		want         string
	}{
		{name: "a trusted event keeps read-write", event: paymentsEvent, intake: "alertmanager", want: "read-write"},
		{name: "an untrusted event waits for approval", event: diskEvent, intake: "grafana", want: "approved"},
		{name: "an untrusted event with the override keeps read-write", event: diskEvent, intake: "grafana", allowUntrust: true, want: "read-write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, instances := loadConfig(t)

			route := config.NewRoute("write")
			route.Priority = 1
			route.Match = &config.Match{Intake: []string{tt.intake}}
			route.Mode, route.Actions, route.AllowUntrustedWrite = action.ReadWrite, []string{"monitor-note"}, tt.allowUntrust
			cfg.Routes = append(cfg.Routes, cfg.ResolveRuntimeRoute(route))

			h := setupWith(t, cfg, instances)
			require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, tt.event)))

			investigations := h.investigations(t)
			require.Len(t, investigations, 1)
			require.Equal(t, "write", investigations[0].Route)
			require.Equal(t, tt.want, investigations[0].Mode)
		})
	}
}
