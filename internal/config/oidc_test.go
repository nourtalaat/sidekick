package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/roles"
)

func TestOIDCSection(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), readFixture(t, "valid/oidc.yaml"), testOptions())
	require.NoError(t, err)
	require.Equal(t, &OIDC{
		Issuer:       "https://acme.okta.com",
		ClientID:     "sidekick",
		ClientSecret: registry.SecretRef{Name: "sidekick-oidc", Key: "clientSecret"},
		GroupsClaim:  DefaultGroupsClaim,
		Scopes:       []string{"groups"},
		Roles: map[RoleName][]string{
			RoleAdmin:     {"sre-admins"},
			RoleApprover:  {"oncall-leads"},
			RoleResponder: {"engineering"},
			RoleViewer:    {AnyGroup},
		},
	}, cfg.OIDC)
}

func TestOIDCRoleOf(t *testing.T) {
	t.Parallel()

	o := &OIDC{Roles: map[RoleName][]string{
		RoleAdmin:     {"sre-admins"},
		RoleApprover:  {"oncall-leads", "managers"},
		RoleResponder: {"engineering"},
	}}

	everyone := &OIDC{Roles: map[RoleName][]string{RoleViewer: {AnyGroup}, RoleAdmin: {"sre-admins"}}}

	tests := []struct {
		name   string
		oidc   *OIDC
		groups []string
		want   roles.Role
	}{
		{name: "one matching group", oidc: o, groups: []string{"engineering"}, want: roles.Responder},
		{name: "the highest match wins", oidc: o, groups: []string{"engineering", "sre-admins", "managers"}, want: roles.Admin},
		{name: "any listed group of a role", oidc: o, groups: []string{"managers"}, want: roles.Approver},
		{name: "no matching group", oidc: o, groups: []string{"sales"}, want: 0},
		{name: "no groups", oidc: o, want: 0},
		{name: "a star matches everyone", oidc: everyone, want: roles.Viewer},
		{name: "a group above the star wins", oidc: everyone, groups: []string{"sre-admins"}, want: roles.Admin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.oidc.RoleOf(tt.groups))
		})
	}
}
