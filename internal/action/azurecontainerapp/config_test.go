package azurecontainerapp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const (
	testTenant = "22222222-3333-4444-5555-666666666666"
	testClient = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{Limits: Limits{
		Subscriptions:  []string{testSub},
		ResourceGroups: []string{testGroup},
		Apps:           []string{"checkout-*"},
	}}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	secret := registry.SecretRef{Name: "azure-rollback", Key: "client-secret"}

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "a client secret with both IDs", change: func(c *Config) {
			c.TenantID, c.ClientID, c.ClientSecret = testTenant, testClient, &secret
		}},
		{name: "maxRevisionsBack at the lowest", change: func(c *Config) { c.MaxRevisionsBack = new(1) }},
		{name: "maxRevisionsBack at the highest", change: func(c *Config) { c.MaxRevisionsBack = new(10) }},
		{name: "maxRevisionsBack below the lowest", change: func(c *Config) { c.MaxRevisionsBack = new(0) }, wantErr: "maxRevisionsBack: 0 must be from 1 to 10"},
		{name: "maxRevisionsBack above the highest", change: func(c *Config) { c.MaxRevisionsBack = new(11) }, wantErr: "maxRevisionsBack: 11 must be from 1 to 10"},
		{name: "a client secret without IDs", change: func(c *Config) { c.ClientSecret = &secret }, wantErr: "clientSecret: needs tenantID and clientID"},
		{name: "a tenant that is not a GUID", change: func(c *Config) { c.TenantID = "contoso" }, wantErr: `tenantID: "contoso" is not a GUID`},
		{name: "a client that is not a GUID", change: func(c *Config) { c.ClientID = "x" }, wantErr: `clientID: "x" is not a GUID`},
		{name: "no subscriptions", change: func(c *Config) { c.Limits.Subscriptions = nil }, wantErr: "limits.subscriptions: list at least one subscription ID"},
		{name: "a subscription that is not a GUID", change: func(c *Config) { c.Limits.Subscriptions = []string{"prod"} }, wantErr: `limits.subscriptions[0]: "prod" is not a GUID`},
		{name: "a subscription listed twice", change: func(c *Config) {
			c.Limits.Subscriptions = []string{testSub, strings.ToUpper(testSub)}
		}, wantErr: "limits.subscriptions[1]:"},
		{name: "no resource groups", change: func(c *Config) { c.Limits.ResourceGroups = nil }, wantErr: "limits.resourceGroups: list at least one resource group"},
		{name: "a resource group of the longest length", change: func(c *Config) { c.Limits.ResourceGroups = []string{strings.Repeat("a", 90)} }},
		{name: "a resource group one longer", change: func(c *Config) { c.Limits.ResourceGroups = []string{strings.Repeat("a", 91)} }, wantErr: "limits.resourceGroups[0]:"},
		{name: "a resource group with a slash", change: func(c *Config) { c.Limits.ResourceGroups = []string{"a/b"} }, wantErr: "limits.resourceGroups[0]:"},
		{name: "a resource group listed twice", change: func(c *Config) { c.Limits.ResourceGroups = []string{"shop", "SHOP"} }, wantErr: "limits.resourceGroups[1]:"},
		{name: "no apps", change: func(c *Config) { c.Limits.Apps = nil }, wantErr: "limits.apps: list at least one app name or pattern"},
		{name: "an app pattern of only a star", change: func(c *Config) { c.Limits.Apps = []string{"*"} }},
		{name: "an app of the longest length", change: func(c *Config) { c.Limits.Apps = []string{strings.Repeat("a", 32)} }},
		{name: "an app one longer", change: func(c *Config) { c.Limits.Apps = []string{strings.Repeat("a", 33)} }, wantErr: "limits.apps[0]:"},
		{name: "an app with an upper-case letter", change: func(c *Config) { c.Limits.Apps = []string{"Checkout"} }, wantErr: "limits.apps[0]:"},
		{name: "an app pattern with a question mark", change: func(c *Config) { c.Limits.Apps = []string{"check?ut"} }, wantErr: "limits.apps[0]:"},
		{name: "an app pattern with brackets", change: func(c *Config) { c.Limits.Apps = []string{"[a]*"} }, wantErr: "limits.apps[0]:"},
		{name: "an app listed twice", change: func(c *Config) { c.Limits.Apps = []string{"a", "a"} }, wantErr: `limits.apps[1]: "a" is listed twice`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := validConfig()
			tt.change(&c)

			err := c.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConfigDecodes(t *testing.T) {
	t.Parallel()

	const section = `
tenantID: 22222222-3333-4444-5555-666666666666
clientID: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
clientSecret: { name: azure-rollback, key: client-secret }
allowReadWrite: true
maxRevisionsBack: 3
limits:
  subscriptions: [11111111-2222-3333-4444-555555555555]
  resourceGroups: [shop]
  apps: [checkout-*, worker]
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.True(t, c.AllowReadWrite)
	require.Equal(t, 3, c.Max())
	require.Equal(t, &registry.SecretRef{Name: "azure-rollback", Key: "client-secret"}, c.ClientSecret)
	require.Equal(t, Limits{
		Subscriptions:  []string{testSub},
		ResourceGroups: []string{"shop"},
		Apps:           []string{"checkout-*", "worker"},
	}, c.Limits)
}

func TestConfigAllowsApp(t *testing.T) {
	t.Parallel()

	l := Limits{Apps: []string{"checkout-*", "worker"}}

	require.True(t, l.allowsApp("checkout-api"))
	require.True(t, l.allowsApp("checkout-"))
	require.True(t, l.allowsApp("worker"))
	require.False(t, l.allowsApp("checkout"))
	require.False(t, l.allowsApp("worker-2"))
	require.False(t, l.allowsApp(""))
}

func TestApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allow   bool
		blocked bool
	}{
		{name: "without the opt-in", blocked: true},
		{name: "with the opt-in", allow: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			cfg.AllowReadWrite = tt.allow

			require.Equal(t, tt.blocked, action.ReadWriteBlocked(&cfg), "the config reports it")

			built, err := New(t.Context(), "rollback", cfg, registry.Deps{})
			require.NoError(t, err)
			require.Equal(t, tt.blocked, action.ReadWriteBlocked(built), "the built action reports it")
		})
	}
}
