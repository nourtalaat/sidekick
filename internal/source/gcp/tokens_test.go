package gcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testKeyBits is the size of the service account key the tests make.
const testKeyBits = 2048

// testCredentialsRef is the secret reference of the test credential file.
var testCredentialsRef = registry.SecretRef{Name: "sidekick-gcp", Key: "credentials.json"}

func TestSecretTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// file returns the credential file, given the token endpoint.
		file    func(t *testing.T, tokenURL string) []byte
		err     error
		wantErr string
	}{
		{
			name: "a service account key gets a token, read once while the token lasts",
			file: serviceAccountFile,
		},
		{
			name:    "a file that is not JSON",
			file:    func(*testing.T, string) []byte { return []byte("not json, but a secret") },
			wantErr: "the file is not JSON",
		},
		{
			name:    "a file of a type the source refuses",
			file:    func(*testing.T, string) []byte { return []byte(`{"type":"authorized_user","refresh_token":"x"}`) },
			wantErr: `the file has type "authorized_user"; use "external_account" (workload identity federation) or "service_account"`,
		},
		{
			name:    "a secret that cannot be read",
			err:     errors.New("no such file"),
			wantErr: "resolve secret sidekick-gcp/credentials.json: no such file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			var file []byte
			if tt.file != nil {
				file = tt.file(t, api.URL()+"/token")
			}

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), testCredentialsRef).Return(file, tt.err).Times(1)

			if tt.wantErr == "" {
				api.Handle(http.MethodPost, "/token", body(`{"access_token":"from-the-key","token_type":"Bearer","expires_in":3600}`))
				api.ExpectAllCalled()
			}

			ref := testCredentialsRef
			tokens := newTokens(&ref, secrets, api.Client())

			token, err := tokens.Token()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), "a secret", "the error never quotes the file")

				return
			}

			require.NoError(t, err)
			require.Equal(t, "from-the-key", token.AccessToken)

			again, err := tokens.Token()
			require.NoError(t, err)
			require.Equal(t, token.AccessToken, again.AccessToken)

			form, err := url.ParseQuery(string(api.Requests()[0].Body))
			require.NoError(t, err)
			require.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", form.Get("grant_type"))
		})
	}
}

// serviceAccountFile returns a service account key file whose token
// endpoint is tokenURL.
func serviceAccountFile(t *testing.T, tokenURL string) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, testKeyBits)
	require.NoError(t, err)

	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	data, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     testProject,
		"private_key_id": "test-key",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "sidekick@shop-prod.iam.gserviceaccount.com",
		"token_uri":      tokenURL,
	})
	require.NoError(t, err)

	return data
}
