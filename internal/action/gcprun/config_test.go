package gcprun

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(*Config)
		wantErr []string
	}{
		{name: "a valid config", change: func(*Config) {}},
		{name: "the project ID with a domain prefix", change: func(c *Config) { c.Limits.Projects = []string{"example.com:shop-prod"} }},
		{name: "no limits", change: func(c *Config) { c.Limits = Limits{} }, wantErr: []string{
			"limits.projects: list at least one", "limits.locations: list at least one", "limits.services: list at least one",
		}},
		{name: "a project that is not an ID", change: func(c *Config) { c.Limits.Projects = []string{"Shop"} }, wantErr: []string{`limits.projects[0]: "Shop" is not a project ID`}},
		{name: "a project listed twice", change: func(c *Config) { c.Limits.Projects = []string{"shop-prod", "shop-prod"} }, wantErr: []string{`limits.projects[1]: "shop-prod" is listed twice`}},
		{name: "a location that is a wildcard", change: func(c *Config) { c.Limits.Locations = []string{"-"} }, wantErr: []string{`limits.locations[0]: "-" is not a region`}},
		{name: "a location that is a zone-less word", change: func(c *Config) { c.Limits.Locations = []string{"europe"} }, wantErr: []string{`limits.locations[0]: "europe" is not a region`}},
		{name: "a service that is one star", change: func(c *Config) { c.Limits.Services = []string{"*"} }},
		{name: "a service name of 49 characters", change: func(c *Config) { c.Limits.Services = []string{"a" + strings.Repeat("b", 48)} }},
		{
			name:    "a service name of 50 characters",
			change:  func(c *Config) { c.Limits.Services = []string{"a" + strings.Repeat("b", 49)} },
			wantErr: []string{"limits.services[0]"},
		},
		{name: "a service with capitals", change: func(c *Config) { c.Limits.Services = []string{"Checkout"} }, wantErr: []string{`limits.services[0]: "Checkout" is not a service name`}},
		{name: "a service pattern with another wildcard", change: func(c *Config) { c.Limits.Services = []string{"check?ut"} }, wantErr: []string{"limits.services[0]"}},
		{name: "a service listed twice", change: func(c *Config) { c.Limits.Services = []string{"a", "a"} }, wantErr: []string{`limits.services[1]: "a" is listed twice`}},
		{name: "maxRevisionsBack of 1", change: func(c *Config) { c.MaxRevisionsBack = new(1) }},
		{name: "maxRevisionsBack of 10", change: func(c *Config) { c.MaxRevisionsBack = new(10) }},
		{name: "maxRevisionsBack of 0", change: func(c *Config) { c.MaxRevisionsBack = new(0) }, wantErr: []string{"maxRevisionsBack: 0 must be from 1 to 10"}},
		{name: "maxRevisionsBack of 11", change: func(c *Config) { c.MaxRevisionsBack = new(11) }, wantErr: []string{"maxRevisionsBack: 11 must be from 1 to 10"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := limitedConfig()
			tt.change(&cfg)

			err := cfg.Validate()
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestConfigYAML(t *testing.T) {
	t.Parallel()

	section := `
allowReadWrite: true
maxRevisionsBack: 2
credentialsSecret: { name: sidekick-gcp-run, key: credentials.json }
limits:
  projects: [shop-prod]
  locations: [europe-west1, us-central1]
  services: [checkout, "payments-*"]
`

	var cfg Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &cfg))
	require.NoError(t, cfg.Validate())
	require.Equal(t, Config{
		ReadWriteGate:     action.ReadWriteGate{AllowReadWrite: true},
		RevisionLimit:     action.RevisionLimit{MaxRevisionsBack: new(2)},
		CredentialsSecret: &registry.SecretRef{Name: "sidekick-gcp-run", Key: "credentials.json"},
		Limits: Limits{
			Projects:  []string{"shop-prod"},
			Locations: []string{"europe-west1", "us-central1"},
			Services:  []string{"checkout", "payments-*"},
		},
	}, cfg)
}

func TestConfigIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "without the opt-in", yaml: "limits: {}", want: true},
		{name: "with the opt-in", yaml: "allowReadWrite: true", want: false},
		{name: "with the opt-in turned off", yaml: "allowReadWrite: false", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg Config

			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &cfg))
			require.Equal(t, tt.want, action.ReadWriteBlocked(&cfg))
		})
	}
}

// TestRegistryCheck decodes a config section the way the config check does,
// which is how a read-write route that lists the action is refused without
// allowReadWrite: true.
func TestRegistryCheck(t *testing.T) {
	t.Parallel()

	limits := "limits: { projects: [shop-prod], locations: [europe-west1], services: [checkout] }\n"

	tests := []struct {
		name        string
		section     string
		wantBlocked bool
		wantErr     string
	}{
		{name: "without the opt-in", section: limits, wantBlocked: true},
		{name: "with the opt-in", section: "allowReadWrite: true\n" + limits},
		{name: "an unknown key", section: "allowReadWriter: true\n" + limits, wantErr: "allowReadWriter"},
		{name: "limits that fail Validate", section: "limits: { projects: [shop-prod] }\n", wantErr: "limits.locations: list at least one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var doc yaml.Node

			require.NoError(t, yaml.Unmarshal([]byte(tt.section), &doc))

			decoded, err := registry.Actions.Check(t.Context(), Type, "cloud-run-rollback", doc.Content[0])
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantBlocked, action.ReadWriteBlocked(decoded))
		})
	}
}
