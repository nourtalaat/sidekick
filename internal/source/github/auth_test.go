package github

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// errSecretMissing stands for a secret file that cannot be read.
var errSecretMissing = errors.New("secret file missing")

func TestInstallationTokensExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secrets func(t *testing.T) registry.Secrets
		answer  []fakehttp.Response
		want    string
		wantErr string
		wantIs  error
	}{
		{
			name:    "makes a token that can only read",
			secrets: func(t *testing.T) registry.Secrets { t.Helper(); return keySecrets(t, 1) },
			answer:  []fakehttp.Response{tokenResponse("ghs_one", testStart.Add(time.Hour))},
			want:    "ghs_one",
		},
		{
			name:    "GitHub refuses the JWT",
			secrets: func(t *testing.T) registry.Secrets { t.Helper(); return keySecrets(t, 1) },
			answer: []fakehttp.Response{{
				Status:  http.StatusUnauthorized,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body:    []byte(`{"message":"A JSON web token could not be decoded"}`),
			}},
			wantErr: "401 A JSON web token could not be decoded",
		},
		{
			name:    "an answer without a token",
			secrets: func(t *testing.T) registry.Secrets { t.Helper(); return keySecrets(t, 1) },
			answer:  []fakehttp.Response{tokenResponse("", testStart.Add(time.Hour))},
			wantIs:  errNoToken,
		},
		{
			name: "a key that does not parse",
			secrets: func(t *testing.T) registry.Secrets {
				t.Helper()

				s := registry.NewMockSecrets(gomock.NewController(t))
				s.EXPECT().Resolve(gomock.Any(), testKeyRef).Return([]byte("not a key"), nil)

				return s
			},
			wantErr: "parse the private key",
		},
		{
			name: "a key that cannot be read",
			secrets: func(t *testing.T) registry.Secrets {
				t.Helper()

				s := registry.NewMockSecrets(gomock.NewController(t))
				s.EXPECT().Resolve(gomock.Any(), testKeyRef).Return(nil, errSecretMissing)

				return s
			},
			wantIs: errSecretMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if len(tt.answer) > 0 {
				api.Handle(http.MethodPost, testExchangePath, tt.answer...)
			}

			api.ExpectAllCalled()

			tokens := newTestTokens(api, tt.secrets(t), func() time.Time { return testStart })

			got, err := tokens.Token(t.Context())

			switch {
			case tt.wantIs != nil:
				require.ErrorIs(t, err, tt.wantIs)
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}

			if len(tt.answer) > 0 {
				requireExchange(t, api.Requests()[0])
			}
		})
	}
}

func TestInstallationTokensRefresh(t *testing.T) {
	t.Parallel()

	expires := testStart.Add(time.Hour)

	tests := []struct {
		name  string
		later time.Time
		want  string
	}{
		{name: "keeps a token that has time left", later: expires.Add(-refreshBefore - time.Second), want: "ghs_one"},
		{name: "replaces a token about to expire", later: expires.Add(-refreshBefore), want: "ghs_two"},
		{name: "replaces an expired token", later: expires.Add(time.Minute), want: "ghs_two"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			answers := []fakehttp.Response{tokenResponse("ghs_one", expires)}
			if tt.want != "ghs_one" {
				answers = append(answers, tokenResponse("ghs_two", tt.later.Add(time.Hour)))
			}

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, testExchangePath, answers...)
			api.ExpectAllCalled()

			c := &clock{now: testStart}
			tokens := newTestTokens(api, keySecrets(t, len(answers)), c.Now)

			first, err := tokens.Token(t.Context())
			require.NoError(t, err)
			require.Equal(t, "ghs_one", first)

			c.Set(tt.later)

			got, err := tokens.Token(t.Context())
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestInstallationTokensConcurrent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		callers int
	}{
		{name: "two callers", callers: 2},
		{name: "many callers", callers: 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			answer := tokenResponse("ghs_one", testStart.Add(time.Hour))
			answer.Delay = 50 * time.Millisecond
			api.Handle(http.MethodPost, testExchangePath, answer)
			api.ExpectAllCalled()

			tokens := newTestTokens(api, keySecrets(t, 1), func() time.Time { return testStart })

			var wg sync.WaitGroup

			got := make([]string, tt.callers)
			errs := make([]error, tt.callers)

			for i := range tt.callers {
				wg.Go(func() { got[i], errs[i] = tokens.Token(t.Context()) })
			}

			wg.Wait()

			for i := range tt.callers {
				require.NoError(t, errs[i])
				require.Equal(t, "ghs_one", got[i])
			}

			require.Len(t, api.Requests(), 1)
		})
	}
}

func TestAuthTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		api        []fakehttp.Response
		exchanges  int
		wantStatus int
		wantTokens []string
	}{
		{
			name:       "sends the installation token",
			method:     http.MethodGet,
			api:        []fakehttp.Response{{Status: http.StatusOK}},
			exchanges:  1,
			wantStatus: http.StatusOK,
			wantTokens: []string{"Bearer ghs_1"},
		},
		{
			name:       "tries once more with a new token after a 401",
			method:     http.MethodGet,
			api:        []fakehttp.Response{{Status: http.StatusUnauthorized}, {Status: http.StatusOK}},
			exchanges:  2,
			wantStatus: http.StatusOK,
			wantTokens: []string{"Bearer ghs_1", "Bearer ghs_2"},
		},
		{
			name:       "gives up after a second 401",
			method:     http.MethodGet,
			api:        []fakehttp.Response{{Status: http.StatusUnauthorized}, {Status: http.StatusUnauthorized}},
			exchanges:  2,
			wantStatus: http.StatusUnauthorized,
			wantTokens: []string{"Bearer ghs_1", "Bearer ghs_2"},
		},
		{
			name:       "does not repeat a request that may change something",
			method:     http.MethodPost,
			api:        []fakehttp.Response{{Status: http.StatusUnauthorized}},
			exchanges:  1,
			wantStatus: http.StatusUnauthorized,
			wantTokens: []string{"Bearer ghs_1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(tt.method, "/repos/acme/checkout", tt.api...)

			for i := range tt.exchanges {
				api.Handle(http.MethodPost, testExchangePath, tokenResponse("ghs_"+strconv.Itoa(i+1), testStart.Add(time.Hour)))
			}

			api.ExpectAllCalled()

			origin, err := url.Parse(api.URL())
			require.NoError(t, err)

			client := &http.Client{Transport: &authTransport{
				next:   api.Client().Transport,
				origin: origin,
				tokens: newTestTokens(api, keySecrets(t, tt.exchanges), func() time.Time { return testStart }),
			}}

			req, err := http.NewRequestWithContext(t.Context(), tt.method, api.URL()+"/repos/acme/checkout", nil)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tt.wantStatus, resp.StatusCode)

			var sent []string

			for _, r := range api.Requests() {
				if r.Path == "/repos/acme/checkout" {
					sent = append(sent, r.Header.Get("Authorization"))
				}
			}

			require.Equal(t, tt.wantTokens, sent)
		})
	}
}

func TestAuthTransportOtherOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
	}{
		{name: "sends no token"},
		{name: "drops a token the caller set", header: "Bearer someone-else"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			other := fakehttp.New(t)
			other.Handle(http.MethodGet, "/download", fakehttp.Response{Status: http.StatusOK})
			other.ExpectAllCalled()

			client := &http.Client{Transport: &authTransport{
				next:   other.Client().Transport,
				origin: &url.URL{Scheme: "https", Host: "api.github.com"},
			}}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, other.URL()+"/download", nil)
			require.NoError(t, err)

			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			resp, err := client.Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Empty(t, other.Requests()[0].Header.Get("Authorization"))
		})
	}
}

// requireExchange checks a token exchange request: a JWT signed with the
// test key that names the app, issued a minute early and valid for ten
// minutes in all, and a body that asks only for read permissions.
func requireExchange(t *testing.T, r fakehttp.Request) {
	t.Helper()

	key, err := testKey()
	require.NoError(t, err)

	signed, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	require.True(t, ok, "the exchange has a bearer token")

	var claims jwt.RegisteredClaims

	parsed, err := jwt.ParseWithClaims(signed, &claims,
		func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithTimeFunc(func() time.Time { return testStart }),
		jwt.WithIssuedAt(),
	)
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	require.Equal(t, "123456", claims.Issuer)
	require.Equal(t, testStart.Add(-time.Minute), claims.IssuedAt.UTC())
	require.Equal(t, testStart.Add(9*time.Minute), claims.ExpiresAt.UTC())
	require.LessOrEqual(t, claims.ExpiresAt.Sub(claims.IssuedAt.Time), 10*time.Minute)

	require.JSONEq(t, `{"permissions":{"actions":"read","contents":"read","metadata":"read","pull_requests":"read"}}`, string(r.Body))
	require.Equal(t, "sidekick-test", r.Header.Get("User-Agent"))
}
