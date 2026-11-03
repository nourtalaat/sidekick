package awslambda

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
)

// Test account, Region, role, function and alias.
const (
	testAccount  = "111122223333"
	testRegion   = "us-east-1"
	testRole     = "arn:aws:iam::" + testAccount + ":role/sidekick-lambda-rollback"
	testFunction = "checkout"
	testAlias    = "live"
)

// testConfig is the config the tests use: one account with a role, one
// Region, the functions checkout and cart-*, and the alias live.
func testConfig() Config {
	return Config{
		Config: awsaction.Config{Accounts: []awsaction.Account{{ID: testAccount, RoleARN: testRole}}, Regions: []string{testRegion}},
		Limits: Limits{Functions: []string{testFunction, "cart-*"}, Aliases: []string{testAlias}},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "a function name of the longest length", change: func(c *Config) { c.Limits.Functions = []string{strings.Repeat("a", 64)} }},
		{name: "a function pattern of only a star", change: func(c *Config) { c.Limits.Functions = []string{"*"} }},
		{name: "an alias name of the longest length", change: func(c *Config) { c.Limits.Aliases = []string{strings.Repeat("a", 128)} }},
		{name: "an alias with digits and letters", change: func(c *Config) { c.Limits.Aliases = []string{"v2"} }},
		{name: "maxRevisionsBack at the lowest", change: func(c *Config) { c.MaxRevisionsBack = new(1) }},
		{name: "maxRevisionsBack at the highest", change: func(c *Config) { c.MaxRevisionsBack = new(10) }},
		{name: "maxRevisionsBack of 0", change: func(c *Config) { c.MaxRevisionsBack = new(0) }, wantErr: "maxRevisionsBack: 0 must be from 1 to 10"},
		{name: "maxRevisionsBack of 11", change: func(c *Config) { c.MaxRevisionsBack = new(11) }, wantErr: "maxRevisionsBack: 11 must be from 1 to 10"},
		{name: "no functions", change: func(c *Config) { c.Limits.Functions = nil }, wantErr: "limits.functions: list at least one"},
		{name: "a function given by ARN", change: func(c *Config) {
			c.Limits.Functions = []string{"arn:aws:lambda:us-east-1:111122223333:function:checkout"}
		}, wantErr: "limits.functions[0]"},
		{name: "a function name one over the length", change: func(c *Config) { c.Limits.Functions = []string{strings.Repeat("a", 65)} }, wantErr: "limits.functions[0]"},
		{name: "a function pattern with another wildcard", change: func(c *Config) { c.Limits.Functions = []string{"cart-?"} }, wantErr: "limits.functions[0]"},
		{name: "a function pattern with a colon", change: func(c *Config) { c.Limits.Functions = []string{"checkout:live"} }, wantErr: "limits.functions[0]"},
		{name: "a function listed twice", change: func(c *Config) { c.Limits.Functions = []string{"cart-*", "cart-*"} }, wantErr: `limits.functions[1]: "cart-*" is listed twice`},
		{name: "no aliases", change: func(c *Config) { c.Limits.Aliases = nil }, wantErr: "limits.aliases: list at least one"},
		{name: "the alias $LATEST", change: func(c *Config) { c.Limits.Aliases = []string{"$LATEST"} }, wantErr: "limits.aliases[0]"},
		{name: "an alias with a star", change: func(c *Config) { c.Limits.Aliases = []string{"li*"} }, wantErr: "limits.aliases[0]"},
		{name: "an alias name one over the length", change: func(c *Config) { c.Limits.Aliases = []string{strings.Repeat("a", 129)} }, wantErr: "limits.aliases[0]"},
		{name: "an alias that is a version number", change: func(c *Config) { c.Limits.Aliases = []string{"12"} }, wantErr: `limits.aliases[0]: "12" is a version number`},
		{name: "an alias listed twice", change: func(c *Config) { c.Limits.Aliases = []string{"live", "live"} }, wantErr: `limits.aliases[1]: "live" is listed twice`},
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
    roleARN: arn:aws:iam::111122223333:role/sidekick-lambda-rollback
    externalID: sidekick-prod
regions: [us-east-1]
limits:
  functions: [checkout, "cart-*"]
  aliases: [live]
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
		Limits: Limits{Functions: []string{"checkout", "cart-*"}, Aliases: []string{"live"}},
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
