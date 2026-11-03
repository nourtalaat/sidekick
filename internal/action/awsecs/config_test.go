package awsecs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("a", 255)

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "a name of the longest length", change: func(c *Config) { c.Limits.Clusters = []string{longName} }},
		{name: "a service pattern of only a star", change: func(c *Config) { c.Limits.Services = []string{"*"} }},
		{name: "maxRevisionsBack at the lowest", change: func(c *Config) { c.MaxRevisionsBack = new(1) }},
		{name: "maxRevisionsBack at the highest", change: func(c *Config) { c.MaxRevisionsBack = new(10) }},
		{name: "maxRevisionsBack of 0", change: func(c *Config) { c.MaxRevisionsBack = new(0) }, wantErr: "maxRevisionsBack: 0 must be from 1 to 10"},
		{name: "maxRevisionsBack of 11", change: func(c *Config) { c.MaxRevisionsBack = new(11) }, wantErr: "maxRevisionsBack: 11 must be from 1 to 10"},
		{name: "no clusters", change: func(c *Config) { c.Limits.Clusters = nil }, wantErr: "limits.clusters: list at least one"},
		{name: "a cluster given by ARN", change: func(c *Config) {
			c.Limits.Clusters = []string{"arn:aws:ecs:us-east-1:111122223333:cluster/shop"}
		}, wantErr: "limits.clusters[0]"},
		{name: "a cluster name one over the length", change: func(c *Config) { c.Limits.Clusters = []string{longName + "a"} }, wantErr: "limits.clusters[0]"},
		{name: "a cluster listed twice", change: func(c *Config) { c.Limits.Clusters = []string{"shop", "shop"} }, wantErr: `limits.clusters[1]: "shop" is listed twice`},
		{name: "no services", change: func(c *Config) { c.Limits.Services = nil }, wantErr: "limits.services: list at least one"},
		{name: "a service pattern with another wildcard", change: func(c *Config) { c.Limits.Services = []string{"cart-?"} }, wantErr: "limits.services[0]"},
		{name: "a service pattern with a slash", change: func(c *Config) { c.Limits.Services = []string{"a/b"} }, wantErr: "limits.services[0]"},
		{name: "a service listed twice", change: func(c *Config) { c.Limits.Services = []string{"cart-*", "cart-*"} }, wantErr: "limits.services[1]"},
		{name: "no accounts", change: func(c *Config) { c.Accounts = nil }, wantErr: "accounts: list at least one account"},
		{name: "an account without a role", change: func(c *Config) { c.Accounts[0].RoleARN = "" }, wantErr: "accounts[0].roleARN: required"},
		{name: "a Region that is not one", change: func(c *Config) { c.Regions = []string{"east"} }, wantErr: "regions[0]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := testConfig()
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

func TestConfigReadsYAML(t *testing.T) {
	t.Parallel()

	const section = `
allowReadWrite: true
maxRevisionsBack: 2
accounts:
  - id: "111122223333"
    roleARN: arn:aws:iam::111122223333:role/sidekick-ecs-rollback
    externalID: sidekick-prod
regions: [us-east-1]
limits:
  clusters: [shop]
  services: [checkout, "cart-*"]
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		ReadWriteGate: action.ReadWriteGate{AllowReadWrite: true},
		RevisionLimit: action.RevisionLimit{MaxRevisionsBack: new(2)},
		Config: awsaction.Config{
			Accounts: []awsaction.Account{{ID: testAccount, RoleARN: testRole, ExternalID: "sidekick-prod"}},
			Regions:  []string{testRegion},
		},
		Limits: Limits{Clusters: []string{"shop"}, Services: []string{"checkout", "cart-*"}},
	}, c)
}

func TestConfigIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "without the opt-in", yaml: "regions: [us-east-1]", want: true},
		{name: "with the opt-in", yaml: "allowReadWrite: true", want: false},
		{name: "with the opt-in turned off", yaml: "allowReadWrite: false", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var c Config

			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &c))
			require.Equal(t, tt.want, action.ReadWriteBlocked(&c))
		})
	}
}

func TestNewIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(c *Config)
		want   bool
	}{
		{name: "without the opt-in", change: func(*Config) {}, want: true},
		{name: "with the opt-in", change: func(c *Config) { c.AllowReadWrite = true }, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, _ := newTestAction(t, tt.change)

			require.Equal(t, tt.want, action.ReadWriteBlocked(r), "the built action reports it")
		})
	}
}
