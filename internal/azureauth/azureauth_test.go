package azureauth

import (
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/registry"
)

const (
	testTenant = "22222222-3333-4444-5555-666666666666"
	testClient = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testScope  = "https://management.azure.com/.default"
)

func TestSecretCredential(t *testing.T) {
	t.Parallel()

	ref := registry.SecretRef{Name: "sidekick-azure", Key: "client-secret"}

	tests := []struct {
		name    string
		secret  []byte
		err     error
		wantErr string
	}{
		{name: "an empty secret", secret: []byte(" \n"), wantErr: errEmptySecret.Error()},
		{name: "a secret that cannot be read", err: errors.New("no such file"), wantErr: "resolve secret sidekick-azure/client-secret: no such file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), ref).Return(tt.secret, tt.err)

			cred := NewCredential(Identity{TenantID: testTenant, ClientID: testClient, ClientSecret: &ref}, secrets, nil)

			_, err := cred.GetToken(t.Context(), policy.TokenRequestOptions{Scopes: []string{testScope}})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNewCredentialKind(t *testing.T) {
	t.Parallel()

	ref := registry.SecretRef{Name: "sidekick-azure", Key: "client-secret"}

	tests := []struct {
		name string
		id   Identity
		want any
	}{
		{name: "a client secret", id: Identity{TenantID: testTenant, ClientID: testClient, ClientSecret: &ref}, want: &secretCredential{}},
		{name: "no client secret", id: Identity{}, want: &workloadCredential{}},
		{name: "no client secret, with the IDs", id: Identity{TenantID: testTenant, ClientID: testClient}, want: &workloadCredential{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.IsType(t, tt.want, NewCredential(tt.id, nil, nil))
		})
	}
}

func TestIdentityCheck(t *testing.T) {
	t.Parallel()

	ref := &registry.SecretRef{Name: "sidekick-azure", Key: "client-secret"}

	tests := []struct {
		name string
		id   Identity
		want []string
	}{
		{name: "nothing set", id: Identity{}},
		{name: "both IDs", id: Identity{TenantID: testTenant, ClientID: testClient}},
		{name: "a client secret with both IDs", id: Identity{TenantID: testTenant, ClientID: testClient, ClientSecret: ref}},
		{name: "a tenant that is not a GUID", id: Identity{TenantID: "contoso"}, want: []string{`tenantID: "contoso" is not a GUID`}},
		{name: "a client that is not a GUID", id: Identity{ClientID: "x"}, want: []string{`clientID: "x" is not a GUID`}},
		{name: "a client secret without a tenant", id: Identity{ClientID: testClient, ClientSecret: ref}, want: []string{"clientSecret: needs tenantID and clientID"}},
		{name: "a client secret without a client", id: Identity{TenantID: testTenant, ClientSecret: ref}, want: []string{"clientSecret: needs tenantID and clientID"}},
		{name: "two IDs that are not GUIDs", id: Identity{TenantID: "t", ClientID: "c", ClientSecret: ref}, want: []string{
			`tenantID: "t" is not a GUID`, `clientID: "c" is not a GUID`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, err := range tt.id.Check() {
				got = append(got, err.Error())
			}

			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsGUID(t *testing.T) {
	t.Parallel()

	require.True(t, IsGUID(testTenant))
	require.True(t, IsGUID("AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"))
	require.False(t, IsGUID(testTenant+"0"))
	require.False(t, IsGUID(testTenant[1:]))
	require.False(t, IsGUID(""))
}
