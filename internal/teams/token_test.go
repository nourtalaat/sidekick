package teams

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Test identities.
const (
	testAppID    = "00000000-1111-2222-3333-444444444444"
	testTenantID = "55555555-6666-7777-8888-999999999999"
	testSecret   = "client-secret-value"
)

// testTokenPath is where a fake Entra ID answers token requests.
const testTokenPath = "/" + testTenantID + "/oauth2/v2.0/token"

// testSecretRef names the client secret in testSecrets.
var testSecretRef = registry.SecretRef{Name: "sidekick-teams", Key: "clientSecret"}

func TestTokens(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		// workload uses workload identity with a token file holding
		// "assertion-1", rewritten to "assertion-2" after the first call.
		workload bool
		answers  []fakehttp.Response
		// steps are the times Token is called at, after start, in order.
		steps      []time.Duration
		invalidate bool
		want       []string
		wantForms  []url.Values
	}{
		{
			name:    "a client secret, with the token cached until shortly before it expires",
			answers: []fakehttp.Response{tokenAnswer("token-1", 3600), tokenAnswer("token-2", 3600)},
			steps:   []time.Duration{0, 54 * time.Minute, 56 * time.Minute},
			want:    []string{"token-1", "token-1", "token-2"},
			wantForms: []url.Values{
				secretForm(),
				secretForm(),
			},
		},
		{
			name:     "workload identity reads the federated token anew for each request",
			workload: true,
			answers:  []fakehttp.Response{tokenAnswer("token-1", 3600), tokenAnswer("token-2", 3600)},
			steps:    []time.Duration{0, 2 * time.Hour},
			want:     []string{"token-1", "token-2"},
			wantForms: []url.Values{
				assertionForm("assertion-1"),
				assertionForm("assertion-2"),
			},
		},
		{
			name:       "an invalidated token is replaced at once",
			answers:    []fakehttp.Response{tokenAnswer("token-1", 3600), tokenAnswer("token-2", 3600)},
			steps:      []time.Duration{0, time.Minute},
			invalidate: true,
			want:       []string{"token-1", "token-2"},
			wantForms:  []url.Values{secretForm(), secretForm()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entra := fakehttp.New(t)
			entra.Handle(http.MethodPost, testTokenPath, tt.answers...)
			entra.ExpectAllCalled()

			tokenFile := filepath.Join(t.TempDir(), "token")
			require.NoError(t, os.WriteFile(tokenFile, []byte("assertion-1\n"), 0o600))

			opts := TokenOptions{Credentials: testCredentials(), Scope: BotFrameworkScope, HTTP: entra.Client(), LoginURL: entra.URL() + "/"}
			if tt.workload {
				opts.Credentials.ClientSecret = registry.SecretRef{}
				opts.Credentials.WorkloadIdentity = true
				opts.FederatedTokenFile = tokenFile
			} else {
				opts.Secrets = testSecrets(t)
			}

			tokens, err := NewTokens(opts)
			require.NoError(t, err)

			now := start
			tokens.now = func() time.Time { return now }

			for i, step := range tt.steps {
				now = start.Add(step)

				got, err := tokens.Token(t.Context())
				require.NoError(t, err)
				require.Equal(t, tt.want[i], got)

				if tt.invalidate {
					tokens.Invalidate(got)
				}

				require.NoError(t, os.WriteFile(tokenFile, []byte("assertion-2\n"), 0o600))
			}

			reqs := entra.Requests()
			require.Len(t, reqs, len(tt.wantForms))

			for i, req := range reqs {
				require.Equal(t, "application/x-www-form-urlencoded", req.Header.Get("Content-Type"))

				form, err := url.ParseQuery(string(req.Body))
				require.NoError(t, err)
				require.Equal(t, tt.wantForms[i], form)
			}
		})
	}
}

func TestTokensErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		answer  fakehttp.Response
		wantErr string
		wantIs  error
	}{
		{
			name: "Entra ID refuses the secret",
			answer: fakehttp.Response{
				Status: http.StatusUnauthorized,
				Body:   []byte(`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided.","error_codes":[7000215]}`),
			},
			wantErr: "token request refused by Microsoft Entra ID (HTTP 401): invalid_client (AADSTS7000215)",
		},
		{
			name:    "an error answer that is not JSON",
			answer:  fakehttp.Response{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")},
			wantErr: "decode the token response (HTTP 502)",
		},
		{
			name:   "an answer without a token",
			answer: tokenAnswer("", 3600),
			wantIs: errNoAccessToken,
		},
		{
			name:   "an answer without an expiry",
			answer: tokenAnswer("token-1", 0),
			wantIs: errNoAccessToken,
		},
		{
			name:   "an answer larger than the limit",
			answer: fakehttp.Response{Body: make([]byte, maxTokenResponseBytes+1)},
			wantIs: errTokenTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entra := fakehttp.New(t)
			entra.Handle(http.MethodPost, testTokenPath, tt.answer)
			entra.ExpectAllCalled()

			tokens, err := NewTokens(TokenOptions{
				Credentials: testCredentials(),
				Scope:       GraphScope,
				Secrets:     testSecrets(t),
				HTTP:        entra.Client(),
				LoginURL:    entra.URL() + "/",
			})
			require.NoError(t, err)

			_, err = tokens.Token(t.Context())
			require.Error(t, err)
			require.NotContains(t, err.Error(), testSecret)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			}

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}

func TestNewTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(o *TokenOptions)
		wantIs  error
		wantErr string
	}{
		{name: "valid", change: func(*TokenOptions) {}},
		{name: "no app ID", change: func(o *TokenOptions) { o.Credentials.AppID = "" }, wantIs: errNoAppID},
		{name: "no tenant ID", change: func(o *TokenOptions) { o.Credentials.TenantID = "" }, wantIs: errNoTenantID},
		{name: "no scope", change: func(o *TokenOptions) { o.Scope = "" }, wantIs: errNoScope},
		{name: "no credentials", change: func(o *TokenOptions) { o.Credentials.ClientSecret = registry.SecretRef{} }, wantIs: errNoCredentials},
		{name: "both credentials", change: func(o *TokenOptions) { o.Credentials.WorkloadIdentity = true }, wantIs: errTwoCredentials},
		{name: "a client secret without a resolver", change: func(o *TokenOptions) { o.Secrets = nil }, wantIs: errNoSecrets},
		{
			name:   "workload identity with a token file",
			change: func(o *TokenOptions) { o.Credentials = workloadCredentials(); o.FederatedTokenFile = "/var/run/token" },
		},
		{
			name:    "a login URL over plain http",
			change:  func(o *TokenOptions) { o.LoginURL = "http://login.example.com/" },
			wantErr: "must use https",
		},
		{
			name:    "a login URL without a trailing slash",
			change:  func(o *TokenOptions) { o.LoginURL = "https://login.example.com" },
			wantErr: `does not end in "/"`,
		},
		{
			name:   "a loopback login URL over http",
			change: func(o *TokenOptions) { o.LoginURL = "http://127.0.0.1:8080/" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := TokenOptions{Credentials: testCredentials(), Scope: BotFrameworkScope, Secrets: secrets.NewDir(t.TempDir())}
			tt.change(&opts)

			_, err := NewTokens(opts)

			switch {
			case tt.wantIs != nil:
				require.ErrorIs(t, err, tt.wantIs)
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestNewTokensWithoutTokenFile(t *testing.T) {
	t.Parallel()

	if os.Getenv(FederatedTokenFileEnv) != "" {
		t.Skip(FederatedTokenFileEnv + " is set in this environment")
	}

	_, err := NewTokens(TokenOptions{Credentials: workloadCredentials(), Scope: BotFrameworkScope})
	require.ErrorIs(t, err, errNoTokenFile)
}

func TestCheckBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw     string
		wantErr bool
	}{
		{raw: "https://smba.trafficmanager.net/teams/"},
		{raw: "http://localhost:3978/"},
		{raw: "http://[::1]:3978/"},
		{raw: "http://smba.trafficmanager.net/teams/", wantErr: true},
		{raw: "https://smba.trafficmanager.net/teams", wantErr: true},
		{raw: "/teams/", wantErr: true},
		{raw: "ftp://example.com/", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()

			err := CheckBaseURL(tt.raw)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
		})
	}
}

// testCredentials are credentials with the client secret in testSecrets.
func testCredentials() Credentials {
	return Credentials{AppID: testAppID, TenantID: testTenantID, ClientSecret: testSecretRef}
}

// workloadCredentials are credentials that use workload identity.
func workloadCredentials() Credentials {
	return Credentials{AppID: testAppID, TenantID: testTenantID, WorkloadIdentity: true}
}

// testSecrets returns a resolver that holds testSecret at testSecretRef,
// with the trailing newline a Secret file often has.
func testSecrets(t *testing.T) registry.Secrets {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, testSecretRef.Name), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, testSecretRef.Name, testSecretRef.Key), []byte(testSecret+"\n"), 0o600))

	return secrets.NewDir(dir)
}

// tokenAnswer is a token endpoint answer with token, valid for expiresIn
// seconds.
func tokenAnswer(token string, expiresIn int) fakehttp.Response {
	body := `{"token_type":"Bearer","expires_in":` + strconv.Itoa(expiresIn) + `,"access_token":"` + token + `"}`

	return fakehttp.Response{Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(body)}
}

// secretForm is the form of a token request with testSecret.
func secretForm() url.Values {
	return url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {testAppID},
		"scope":         {BotFrameworkScope},
		"client_secret": {testSecret},
	}
}

// assertionForm is the form of a token request with a federated token.
func assertionForm(assertion string) url.Values {
	return url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {testAppID},
		"scope":                 {BotFrameworkScope},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
}
