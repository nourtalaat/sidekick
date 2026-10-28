package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestTeamsListeners(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), readFixture(t, "valid/teams.yaml"), testOptions())
	require.NoError(t, err)
	require.Equal(t, &Teams{
		AppID:            "00000000-1111-2222-3333-444444444444",
		TenantID:         "55555555-6666-7777-8888-999999999999",
		WorkloadIdentity: true,
		TrustedBots:      []string{"28:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		ServiceURL:       "https://smba.trafficmanager.net/emea/",
	}, cfg.Teams)

	require.Equal(t, registry.TeamsBot{
		AppID: cfg.Teams.AppID, TenantID: cfg.Teams.TenantID, WorkloadIdentity: true, ServiceURL: cfg.Teams.ServiceURL,
	}, cfg.Teams.Bot())

	tests := []struct {
		listener string
		want     bool
	}{
		{listener: "slack-alerts", want: false},
		{listener: "teams-alerts", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.listener, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, cfg.Listeners[tt.listener].OnTeams())
		})
	}
}
