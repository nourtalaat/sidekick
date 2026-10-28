package azure

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// later is how long after testNow the second call for armScope is.
		later     time.Duration
		wantCalls int
	}{
		{name: "a token is kept while it is fresh", later: 30 * time.Minute, wantCalls: 2},
		{name: "a token close to expiring is replaced", later: 56 * time.Minute, wantCalls: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cred := &fakeCredential{}
			tokens := newTokens(cred)
			now := testNow
			tokens.now = func() time.Time { return now }

			first, err := tokens.token(t.Context(), armScope)
			require.NoError(t, err)
			require.Equal(t, "token-for-"+armScope, first)

			other, err := tokens.token(t.Context(), logAnalyticsScope)
			require.NoError(t, err)
			require.Equal(t, "token-for-"+logAnalyticsScope, other, "each scope has its own token")

			now = testNow.Add(tt.later)

			again, err := tokens.token(t.Context(), armScope)
			require.NoError(t, err)
			require.Equal(t, first, again)
			require.Equal(t, tt.wantCalls, cred.calls)
		})
	}
}

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

			cfg := Config{TenantID: "22222222-3333-4444-5555-666666666666", ClientID: testWorkspaceID, ClientSecret: &ref}
			tokens := newTokens(newCredential(cfg, secrets, nil))

			_, err := tokens.token(t.Context(), armScope)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
