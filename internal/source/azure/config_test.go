package azure

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	secret := &registry.SecretRef{Name: "sidekick-azure", Key: "client-secret"}
	tenant := "22222222-3333-4444-5555-666666666666"
	workspace := Workspace{ID: testWorkspaceID, ResourceID: testWorkspace}

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "a subscription and a workspace", cfg: Config{Subscriptions: []string{testSub}, Workspaces: []Workspace{workspace}}},
		{name: "a client secret with its tenant and client", cfg: Config{Subscriptions: []string{testSub}, TenantID: tenant, ClientID: tenant, ClientSecret: secret}},
		{name: "no subscriptions", cfg: Config{}, wantErr: []string{"subscriptions: list at least one subscription ID"}},
		{
			name:    "subscriptions that are not GUIDs or are listed twice",
			cfg:     Config{Subscriptions: []string{"prod", "abcdef01-2222-3333-4444-555555555555", "ABCDEF01-2222-3333-4444-555555555555"}},
			wantErr: []string{`subscriptions[0]: "prod" is not a GUID`, `subscriptions[2]: "ABCDEF01-2222-3333-4444-555555555555" is listed twice`},
		},
		{
			name: "workspaces that are not workspaces",
			cfg: Config{Subscriptions: []string{testSub}, Workspaces: []Workspace{
				{ID: "prod-logs", ResourceID: testSite},
				workspace,
				{ID: testWorkspaceID, ResourceID: testWorkspace},
			}},
			wantErr: []string{
				`workspaces[0].id: "prod-logs" is not a workspace ID`,
				"workspaces[0].resourceID: \"" + testSite + "\" is not a Log Analytics workspace resource ID",
				`workspaces[2].id: "` + testWorkspaceID + `" is listed twice`,
				`workspaces[2].resourceID: another workspace is also named "prod-logs"`,
			},
		},
		{name: "a tenant that is not a GUID", cfg: Config{Subscriptions: []string{testSub}, TenantID: "contoso.onmicrosoft.com"},
			wantErr: []string{`tenantID: "contoso.onmicrosoft.com" is not a GUID`}},
		{name: "a client secret without a client ID", cfg: Config{Subscriptions: []string{testSub}, TenantID: tenant, ClientSecret: secret},
			wantErr: []string{"clientSecret: needs tenantID and clientID"}},
		{
			name:    "a timeout longer than the HTTP client waits",
			cfg:     Config{Subscriptions: []string{testSub}, Settings: source.Settings{Timeout: 2 * time.Minute}},
			wantErr: []string{"timeout: 2m0s is above"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}
