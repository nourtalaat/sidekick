package httpauth_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func ref(name string) *registry.SecretRef {
	return &registry.SecretRef{Name: name, Key: "value"}
}

// withUserInfo returns raw with a user name and a password in it.
func withUserInfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}

	u.User = url.UserPassword("user", "secret")

	return u.String()
}

func TestConfigCheck(t *testing.T) {
	t.Parallel()

	https := httpauth.URL{Key: "url", Raw: "https://am.example.com"}
	plain := httpauth.URL{Key: "url", Raw: "http://am.example.com"}
	password := *ref("password")

	tests := []struct {
		name    string
		cfg     httpauth.Config
		urls    []httpauth.URL
		wantErr []string
	}{
		{name: "nothing set"},
		{name: "a bearer token", cfg: httpauth.Config{BearerTokenSecret: ref("token")}, urls: []httpauth.URL{https}},
		{
			name: "everything valid",
			cfg: httpauth.Config{
				BasicAuth: &httpauth.BasicAuth{Username: "sidekick", PasswordSecret: password},
				TLS:       &httpauth.TLS{CertSecret: ref("cert"), KeySecret: ref("key"), CASecret: ref("ca")},
				Headers:   map[string]string{"X-Scope-OrgID": "tenant-a"},
			},
			urls: []httpauth.URL{https},
		},
		{
			name:    "a token and basic auth",
			cfg:     httpauth.Config{BearerTokenSecret: ref("token"), BasicAuth: &httpauth.BasicAuth{Username: "u", PasswordSecret: password}},
			wantErr: []string{"bearerTokenSecret: set one of bearerTokenSecret and basicAuth, not both"},
		},
		{
			name:    "basic auth without a user or a password",
			cfg:     httpauth.Config{BasicAuth: &httpauth.BasicAuth{}},
			wantErr: []string{"basicAuth.username: required", "basicAuth.passwordSecret: required"},
		},
		{
			name:    "a client certificate without its key",
			cfg:     httpauth.Config{TLS: &httpauth.TLS{CertSecret: ref("cert")}},
			wantErr: []string{"tls: set both certSecret and keySecret, or neither"},
		},
		{
			name:    "an empty tls section",
			cfg:     httpauth.Config{TLS: &httpauth.TLS{}},
			wantErr: []string{"tls: set certSecret and keySecret, caSecret, or both"},
		},
		{
			name:    "tls with an http URL",
			cfg:     httpauth.Config{TLS: &httpauth.TLS{CASecret: ref("ca")}},
			urls:    []httpauth.URL{plain},
			wantErr: []string{"tls: needs https, but url uses http"},
		},
		{
			name:    "credentials in headers",
			cfg:     httpauth.Config{Headers: map[string]string{"authorization": "Bearer x", "Cookie": "a=b"}},
			wantErr: []string{"headers.Cookie: credentials come from Secrets", "headers.authorization: credentials come from Secrets"},
		},
		{
			name:    "a header the caller sets",
			cfg:     httpauth.Config{Headers: map[string]string{"User-Agent": "x"}},
			wantErr: []string{"headers.User-Agent: Sidekick sets this header itself"},
		},
		{
			name:    "a header name that is not a token",
			cfg:     httpauth.Config{Headers: map[string]string{"X Org": "a"}},
			wantErr: []string{`headers.X Org: "X Org" is not a valid header name`},
		},
		{
			name:    "a header value with a line break",
			cfg:     httpauth.Config{Headers: map[string]string{"X-Scope-OrgID": "a\r\nHost: evil"}},
			wantErr: []string{"headers.X-Scope-OrgID: the value must not hold control characters"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := tt.cfg.Check(tt.urls...)

			require.Len(t, errs, len(tt.wantErr))

			for i, want := range tt.wantErr {
				require.ErrorContains(t, errs[i], want)
			}
		})
	}
}

func TestCheckAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		credentials bool
		wantErr     string
	}{
		{name: "https with credentials", raw: "https://am.example.com", credentials: true},
		{name: "a path", raw: "https://mimir.example.com/alertmanager", credentials: true},
		{name: "http without credentials", raw: "http://am:9093"},
		{name: "http to loopback with credentials", raw: "http://127.0.0.1:9093", credentials: true},
		{name: "http to localhost with credentials", raw: "http://localhost:9093", credentials: true},
		{name: "http with credentials", raw: "http://am:9093", credentials: true, wantErr: "must use https"},
		{name: "a host that only looks like loopback", raw: "http://localhost.example.com", credentials: true, wantErr: "must use https"},
		{name: "user info", raw: withUserInfo("https://am.example.com"), wantErr: "must not hold a user name or password"},
		{name: "a query", raw: "https://am.example.com?x=1", wantErr: "must be an absolute http or https URL"},
		{name: "a fragment", raw: "https://am.example.com#x", wantErr: "must be an absolute http or https URL"},
		{name: "no host", raw: "https://", wantErr: "must be an absolute http or https URL"},
		{name: "another scheme", raw: "ftp://am.example.com", wantErr: "must be an absolute http or https URL"},
		{name: "not a URL", raw: "alertmanager", wantErr: "must be an absolute http or https URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := httpauth.CheckAPIURL("url", tt.raw, tt.credentials)

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, "url: ")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConfigSecretUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         httpauth.Config
		wantSends   bool
		wantSecrets bool
	}{
		{name: "nothing"},
		{name: "headers only", cfg: httpauth.Config{Headers: map[string]string{"X-A": "b"}}},
		{name: "a token", cfg: httpauth.Config{BearerTokenSecret: ref("t")}, wantSends: true, wantSecrets: true},
		{name: "basic auth", cfg: httpauth.Config{BasicAuth: &httpauth.BasicAuth{Username: "u"}}, wantSends: true, wantSecrets: true},
		{name: "a client certificate", cfg: httpauth.Config{TLS: &httpauth.TLS{CertSecret: ref("c")}}, wantSends: true, wantSecrets: true},
		{name: "a CA only", cfg: httpauth.Config{TLS: &httpauth.TLS{CASecret: ref("c")}}, wantSecrets: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantSends, tt.cfg.SendsCredentials())
			require.Equal(t, tt.wantSecrets, tt.cfg.UsesSecrets())
		})
	}
}
