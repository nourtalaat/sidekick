package aws

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	own := Account{ID: testAccount}
	role := Account{ID: roleAccount, RoleARN: testRole, ExternalID: "sidekick-external"}
	regions := []string{testRegion}

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "an account with Sidekick's own credentials", cfg: Config{Accounts: []Account{own}, Regions: regions, LogGroups: []string{"/ecs/"}}},
		{name: "an account with a role and an external ID", cfg: Config{Accounts: []Account{own, role}, Regions: []string{testRegion, otherRegion}}},
		{name: "a GovCloud role", cfg: Config{Regions: []string{"us-gov-west-1"},
			Accounts: []Account{{ID: roleAccount, RoleARN: "arn:aws-us-gov:iam::" + roleAccount + ":role/path/sidekick"}}}},
		{name: "no accounts or Regions", cfg: Config{},
			wantErr: []string{"accounts: list at least one account", "regions: list at least one Region"}},
		{
			name: "account IDs that are not IDs or are listed twice",
			cfg:  Config{Regions: regions, Accounts: []Account{{ID: "prod"}, {ID: roleAccount, RoleARN: testRole}, {ID: roleAccount, RoleARN: testRole}}},
			wantErr: []string{`accounts[0].id: "prod" is not a 12-digit AWS account ID`,
				`accounts[2].id: "` + roleAccount + `" is listed twice`},
		},
		{
			name:    "two accounts with Sidekick's own credentials",
			cfg:     Config{Regions: regions, Accounts: []Account{own, {ID: roleAccount}}},
			wantErr: []string{"accounts[1].roleARN: required, because account " + testAccount + " already uses Sidekick's own credentials"},
		},
		{
			name: "a role that is not a role ARN, or is in another account",
			cfg: Config{Regions: regions, Accounts: []Account{
				{ID: testAccount, RoleARN: "arn:aws:iam::" + testAccount + ":user/sidekick"},
				{ID: otherAccount, RoleARN: testRole},
			}},
			wantErr: []string{`accounts[0].roleARN: "arn:aws:iam::` + testAccount + `:user/sidekick" is not an IAM role ARN`,
				"accounts[1].roleARN: the role is in account " + roleAccount + ", not " + otherAccount},
		},
		{
			name: "an external ID without a role, and one STS refuses",
			cfg: Config{Regions: regions, Accounts: []Account{
				{ID: testAccount, ExternalID: "x1"},
				{ID: roleAccount, RoleARN: testRole, ExternalID: "has spaces"},
			}},
			wantErr: []string{"accounts[0].externalID: needs roleARN", "accounts[1].externalID: must be 2 to 1224"},
		},
		{
			name:    "Regions that are not Regions or are listed twice",
			cfg:     Config{Accounts: []Account{own}, Regions: []string{"US-EAST-1", testRegion, testRegion}},
			wantErr: []string{`regions[0]: "US-EAST-1" is not an AWS Region code`, `regions[2]: "us-east-1" is listed twice`},
		},
		{
			name:    "log group prefixes that are not log group names",
			cfg:     Config{Accounts: []Account{own}, Regions: regions, LogGroups: []string{"/ecs/*", "", "/ecs/"}},
			wantErr: []string{`logGroups[0]: "/ecs/*" is not the start of a log group name`, `logGroups[1]: "" is not`},
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

func TestRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
		wantErr string
	}{
		{
			name: "a quoted account ID and a role",
			section: "accounts:\n  - id: \"" + roleAccount + "\"\n    roleARN: " + testRole + "\n    externalID: sidekick-external\n" +
				"regions: [us-east-1]\nlogGroups: [/ecs/]\n",
		},
		{
			name:    "an unquoted account ID with a leading zero keeps its digits",
			section: "accounts:\n  - id: 012345678901\nregions: [us-east-1]\n",
		},
		{
			name:    "a Region is required",
			section: "accounts:\n  - id: \"" + testAccount + "\"\n",
			wantErr: "regions: list at least one Region",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.section), &node))

			_, err := registry.Sources.Check(t.Context(), Type, "aws", node.Content[0])
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}
