package awsaction

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction/awsactiontest"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// Test accounts, Regions and roles.
const (
	accountA    = "111122223333"
	accountB    = "444455556666"
	otherAcct   = "999988887777"
	regionA     = "us-east-1"
	regionB     = "eu-west-1"
	roleA       = "arn:aws:iam::" + accountA + ":role/sidekick-action"
	roleB       = "arn:aws:iam::" + accountB + ":role/sidekick-action"
	externalID  = "sidekick-external"
	stsPath     = "/"
	sessionName = "sidekick"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "one account and one Region", cfg: Config{Accounts: []Account{{ID: accountA, RoleARN: roleA}}, Regions: []string{regionA}}},
		{
			name: "two accounts, one with an external ID, and two Regions",
			cfg: Config{Accounts: []Account{{ID: accountA, RoleARN: roleA}, {ID: accountB, RoleARN: roleB, ExternalID: externalID}},
				Regions: []string{regionA, regionB}},
		},
		{name: "nothing", cfg: Config{}, wantErr: []string{"accounts: list at least one account", "regions: list at least one Region"}},
		{
			name:    "an account without a role",
			cfg:     Config{Accounts: []Account{{ID: accountA}}, Regions: []string{regionA}},
			wantErr: []string{"accounts[0].roleARN: required"},
		},
		{
			name:    "a role in another account",
			cfg:     Config{Accounts: []Account{{ID: otherAcct, RoleARN: roleA}}, Regions: []string{regionA}},
			wantErr: []string{"accounts[0].roleARN: the role is in account " + accountA + ", not " + otherAcct},
		},
		{
			name:    "a Region that is not one",
			cfg:     Config{Accounts: []Account{{ID: accountA, RoleARN: roleA}}, Regions: []string{"east"}},
			wantErr: []string{`regions[0]: "east" is not an AWS Region code`},
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

func newClients(t *testing.T, cfg Config) (*Clients, *fakehttp.Server) {
	t.Helper()

	sts := fakehttp.New(t)
	sts.ExpectAllCalled()

	c, err := New(t.Context(), cfg, sts.Client(), WithLoader(awsactiontest.Loader), WithSTSEndpoint(sts.URL()))
	require.NoError(t, err)

	return c, sts
}

func TestNewChecksTheConfigAndCallsNothing(t *testing.T) {
	t.Parallel()

	sts := fakehttp.New(t)

	_, err := New(t.Context(), Config{}, sts.Client(), WithLoader(awsactiontest.Loader), WithSTSEndpoint(sts.URL()))
	require.ErrorContains(t, err, "accounts: list at least one account")

	_, err = New(t.Context(), Config{Accounts: []Account{{ID: accountA, RoleARN: roleA}}, Regions: []string{regionA}}, sts.Client(),
		WithLoader(awsactiontest.Loader), WithSTSEndpoint(sts.URL()))
	require.NoError(t, err)
	require.Empty(t, sts.Requests())
}

func TestTarget(t *testing.T) {
	t.Parallel()

	one := Config{Accounts: []Account{{ID: accountA, RoleARN: roleA}}, Regions: []string{regionA}}
	two := Config{Accounts: []Account{{ID: accountA, RoleARN: roleA}, {ID: accountB, RoleARN: roleB}}, Regions: []string{regionA, regionB}}

	tests := []struct {
		name      string
		cfg       Config
		account   string
		region    string
		want      Target
		wantErr   string
		wantErrIs error
	}{
		{name: "both left out with one of each", cfg: one, want: Target{Account: accountA, Region: regionA}},
		{name: "both given", cfg: two, account: accountB, region: regionB, want: Target{Account: accountB, Region: regionB}},
		{name: "given with spaces around", cfg: two, account: " " + accountA + " ", region: regionA, want: Target{Account: accountA, Region: regionA}},
		{
			name: "the account left out with two", cfg: two, region: regionA,
			wantErr: "give account; allowed: " + accountA + ", " + accountB, wantErrIs: action.ErrParams,
		},
		{
			name: "the Region left out with two", cfg: two, account: accountA,
			wantErr: "give Region; allowed: " + regionA + ", " + regionB, wantErrIs: action.ErrParams,
		},
		{
			name: "an account the config does not list", cfg: one, account: otherAcct,
			wantErr: `account "` + otherAcct + `" is not one Sidekick may change; allowed: ` + accountA, wantErrIs: action.ErrLimit,
		},
		{
			name: "a Region the config does not list", cfg: one, region: regionB,
			wantErr: `Region "` + regionB + `" is not one Sidekick may change; allowed: ` + regionA, wantErrIs: action.ErrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clients, _ := newClients(t, tt.cfg)

			got, err := clients.Target(tt.account, tt.region)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, tt.wantErrIs)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want.Account, got.Account)
			require.Equal(t, tt.want.Region, got.Region)
			require.Equal(t, tt.want.Region, got.Config.Region)
			require.NotNil(t, got.Config.Credentials)
		})
	}
}

func TestReady(t *testing.T) {
	t.Parallel()

	cfg := Config{Accounts: []Account{{ID: accountB, RoleARN: roleB, ExternalID: externalID}}, Regions: []string{regionA}}

	t.Run("the role is assumed with the session name and the external ID", func(t *testing.T) {
		t.Parallel()

		clients, sts := newClients(t, cfg)
		sts.Handle(http.MethodPost, stsPath, awsactiontest.AssumedRole(accountB))

		target, err := clients.Target("", "")
		require.NoError(t, err)
		require.NoError(t, target.Ready(t.Context()))
		require.NoError(t, target.Ready(t.Context()), "the credentials are kept")

		reqs := sts.Requests()
		require.Len(t, reqs, 1)
		form := string(reqs[0].Body)
		require.Contains(t, form, "Action=AssumeRole")
		require.Contains(t, form, "RoleArn="+encode(roleB))
		require.Contains(t, form, "RoleSessionName="+sessionName)
		require.Contains(t, form, "ExternalId="+externalID)
	})

	t.Run("a role STS refuses is a permanent failure", func(t *testing.T) {
		t.Parallel()

		clients, sts := newClients(t, cfg)
		sts.Handle(http.MethodPost, stsPath, awsactiontest.AssumeRoleDenied())

		target, err := clients.Target("", "")
		require.NoError(t, err)

		err = target.Ready(t.Context())
		require.ErrorContains(t, err, "get credentials for account "+accountB)
		require.ErrorIs(t, err, action.ErrPermanent)
	})
}

func TestConsoleRoot(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://us-east-1.console.aws.amazon.com", ConsoleRoot("us-east-1"))
	require.Equal(t, "https://cn-north-1.console.amazonaws.cn", ConsoleRoot("cn-north-1"))
}

// encode writes s as a form value of an STS request.
func encode(s string) string {
	return url.QueryEscape(s)
}
