package awsauth

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test accounts, Regions and roles.
const (
	ownAccount   = "111122223333"
	roleAccount  = "444455556666"
	otherAccount = "999988887777"
	testRegion   = "us-east-1"
	testRole     = "arn:aws:iam::" + roleAccount + ":role/sidekick-reader"
)

func TestCheckAccounts(t *testing.T) {
	t.Parallel()

	own := Account{ID: ownAccount}
	role := Account{ID: roleAccount, RoleARN: testRole, ExternalID: "sidekick-external"}

	tests := []struct {
		name    string
		in      []Account
		opts    []AccountOption
		wantErr []string
	}{
		{name: "an account with Sidekick's own credentials", in: []Account{own}},
		{name: "an account with a role and an external ID", in: []Account{own, role}},
		{name: "a GovCloud role", in: []Account{{ID: roleAccount, RoleARN: "arn:aws-us-gov:iam::" + roleAccount + ":role/path/sidekick"}}},
		{name: "a role is required and given", in: []Account{role}, opts: []AccountOption{RequireRole()}},
		{name: "no accounts", wantErr: []string{"accounts: list at least one account"}},
		{
			name:    "account IDs that are not IDs or are listed twice",
			in:      []Account{{ID: "prod"}, {ID: roleAccount, RoleARN: testRole}, {ID: roleAccount, RoleARN: testRole}},
			wantErr: []string{`accounts[0].id: "prod" is not a 12-digit AWS account ID`, `accounts[2].id: "` + roleAccount + `" is listed twice`},
		},
		{
			name:    "two accounts with Sidekick's own credentials",
			in:      []Account{own, {ID: roleAccount}},
			wantErr: []string{"accounts[1].roleARN: required, because account " + ownAccount + " already uses Sidekick's own credentials"},
		},
		{
			name: "a role that is not a role ARN, or is in another account",
			in: []Account{
				{ID: ownAccount, RoleARN: "arn:aws:iam::" + ownAccount + ":user/sidekick"},
				{ID: otherAccount, RoleARN: testRole},
			},
			wantErr: []string{`accounts[0].roleARN: "arn:aws:iam::` + ownAccount + `:user/sidekick" is not an IAM role ARN`,
				"accounts[1].roleARN: the role is in account " + roleAccount + ", not " + otherAccount},
		},
		{
			name:    "an external ID without a role, and one STS refuses",
			in:      []Account{{ID: ownAccount, ExternalID: "x1"}, {ID: roleAccount, RoleARN: testRole, ExternalID: "has spaces"}},
			wantErr: []string{"accounts[0].externalID: needs roleARN", "accounts[1].externalID: must be 2 to 1224"},
		},
		{name: "an external ID one character long", in: []Account{{ID: roleAccount, RoleARN: testRole, ExternalID: "x"}},
			wantErr: []string{"accounts[0].externalID: must be 2 to 1224"}},
		{name: "an external ID two characters long", in: []Account{{ID: roleAccount, RoleARN: testRole, ExternalID: "x1"}}},
		{
			name:    "a role is required and missing",
			in:      []Account{role, own},
			opts:    []AccountOption{RequireRole()},
			wantErr: []string{"accounts[1].roleARN: required, so that this plugin has credentials of its own"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := CheckAccounts(tt.in, tt.opts...)
			if len(tt.wantErr) == 0 {
				require.Empty(t, errs)

				return
			}

			require.Len(t, errs, len(tt.wantErr))

			for i, want := range tt.wantErr {
				require.ErrorContains(t, errs[i], want)
			}
		})
	}
}

func TestCheckRegions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      []string
		wantErr []string
	}{
		{name: "Regions", in: []string{testRegion, "eu-west-1", "us-gov-west-1"}},
		{name: "none", wantErr: []string{"regions: list at least one Region"}},
		{
			name:    "not Regions, or listed twice",
			in:      []string{"US-EAST-1", testRegion, testRegion},
			wantErr: []string{`regions[0]: "US-EAST-1" is not an AWS Region code such as us-east-1`, `regions[2]: "us-east-1" is listed twice`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := CheckRegions(tt.in)
			require.Len(t, errs, len(tt.wantErr))

			for i, want := range tt.wantErr {
				require.EqualError(t, errs[i], want)
			}
		})
	}
}

func TestCheckListCutsLongValues(t *testing.T) {
	t.Parallel()

	long := make([]byte, 2*maxShownLen)
	for i := range long {
		long[i] = 'x'
	}

	errs := CheckList("things", []string{string(long)}, regexp.MustCompile(`^y$`), "a thing")
	require.Len(t, errs, 1)
	require.Less(t, len(errs[0].Error()), 2*maxShownLen)
}
