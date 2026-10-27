package slackroles_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/slackroles"
)

// slackConfig maps two user groups and two users to roles.
func slackConfig() *config.Slack {
	return &config.Slack{
		RoleGroups: map[string]config.RoleName{"S01ONCALL": config.RoleResponder, "S01SRE": config.RoleAdmin},
		RoleUsers:  map[string]config.RoleName{"U01LEAD": config.RoleApprover, "U01GUEST": config.RoleViewer},
	}
}

func TestRoleOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		user   string
		groups []string
		want   roles.Role
	}{
		{name: "a role from slack.roleGroups", user: "U01ANA", groups: []string{"S01SRE"}, want: roles.Admin},
		{name: "a role from slack.roleUsers", user: "U01LEAD", want: roles.Approver},
		{name: "an unmatched user is a responder", user: "U01NOBODY", want: roles.Responder},
		{name: "the highest group wins", user: "U01ANA", groups: []string{"S01ONCALL", "S01SRE"}, want: roles.Admin},
		{name: "a group above the user's own role wins", user: "U01LEAD", groups: []string{"S01SRE"}, want: roles.Admin},
		{name: "the user's own role above a group wins", user: "U01LEAD", groups: []string{"S01ONCALL"}, want: roles.Approver},
		{name: "a user mapped to viewer stays a viewer", user: "U01GUEST", want: roles.Viewer},
		{name: "a group no longer in the config gives nothing", user: "U01ANA", groups: []string{"S01GONE"}, want: roles.Responder},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, slackroles.RoleOf(slackConfig(), tt.user, tt.groups))
		})
	}
}

func TestNewWithoutSlack(t *testing.T) {
	t.Parallel()

	_, err := slackroles.New(slackroles.Options{})
	require.Error(t, err)
}

func TestLookupWithoutRoleGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user string
		want roles.Role
	}{
		{name: "a user from slack.roleUsers", user: "U01LEAD", want: roles.Approver},
		{name: "an unmatched user", user: "U01NOBODY", want: roles.Responder},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := slackConfig()
			cfg.RoleGroups = nil

			// No pool and no Slack: without groups, nothing is read.
			lookup, err := slackroles.New(slackroles.Options{Slack: cfg})
			require.NoError(t, err)

			got, err := lookup.Role(t.Context(), "T01TEAM", tt.user)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
