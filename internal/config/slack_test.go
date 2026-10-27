package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/roles"
)

func TestSlackConnects(t *testing.T) {
	t.Parallel()

	const bot = "  botTokenSecret: {name: sidekick-slack, key: botToken}\n"

	tests := []struct {
		name     string
		yaml     string
		wantMode SlackMode
		want     bool
	}{
		{name: "no slack section", want: false},
		{name: "socket mode by default, without an app token: posts only", yaml: "slack:\n" + bot, wantMode: SlackModeSocket},
		{
			name:     "socket mode with an app token",
			yaml:     "slack:\n" + bot + "  appTokenSecret: {name: sidekick-slack, key: appToken}\n",
			wantMode: SlackModeSocket,
			want:     true,
		},
		{
			name:     "events mode with a signing secret",
			yaml:     "slack:\n" + bot + "  mode: events\n  signingSecret: {name: sidekick-slack, key: signingSecret}\n",
			wantMode: SlackModeEvents,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), []byte(minimalYAML(t)+tt.yaml), testOptions())
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Slack.Connects())

			if tt.wantMode != "" {
				require.Equal(t, tt.wantMode, cfg.Slack.Mode)
			}
		})
	}
}

func TestRoleNameRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name RoleName
		want roles.Role
	}{
		{name: RoleViewer, want: roles.Viewer},
		{name: RoleResponder, want: roles.Responder},
		{name: RoleApprover, want: roles.Approver},
		{name: RoleAdmin, want: roles.Admin},
		{name: "owner", want: 0},
		{name: "", want: 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.name.Role())
		})
	}
}
