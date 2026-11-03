package alertmanagersilence

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func ref(name string) *registry.SecretRef {
	return &registry.SecretRef{Name: name, Key: "value"}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	with := func(change func(*Config)) Config {
		c := testConfig("https://alertmanager.example.com")
		change(&c)

		return c
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "the smallest config", cfg: testConfig("https://alertmanager.example.com")},
		{
			name: "everything set",
			cfg: with(func(c *Config) {
				c.URL = "https://mimir.example.com/alertmanager"
				c.BasicAuth = &httpauth.BasicAuth{Username: "sidekick", PasswordSecret: *ref("password")}
				c.Headers = map[string]string{"X-Scope-OrgID": "team-a"}
				c.TLS = &httpauth.TLS{CASecret: ref("ca"), CertSecret: ref("cert"), KeySecret: ref("key")}
				c.CreatedBy = "sidekick-prod"
				c.Limits.MaxDuration = 24 * time.Hour
			}),
		},
		{name: "http to a loopback host with a token", cfg: with(func(c *Config) { c.URL = "http://127.0.0.1:9093"; c.BearerTokenSecret = ref("token") })},
		{name: "http without a credential", cfg: with(func(c *Config) { c.URL = "http://alertmanager:9093" })},
		{name: "no url", cfg: with(func(c *Config) { c.URL = "" }), wantErr: []string{"url: required"}},
		{
			name:    "http with a token",
			cfg:     with(func(c *Config) { c.URL = "http://alertmanager:9093"; c.BearerTokenSecret = ref("token") }),
			wantErr: []string{"url: ", "must use https"},
		},
		{name: "a url with a password", cfg: with(func(c *Config) { c.URL = "https://u:p@alertmanager.example.com" }), wantErr: []string{"must not hold a user name or password"}},
		{name: "a token and basic auth", cfg: with(func(c *Config) {
			c.BearerTokenSecret = ref("token")
			c.BasicAuth = &httpauth.BasicAuth{Username: "u", PasswordSecret: *ref("p")}
		}), wantErr: []string{"set one of bearerTokenSecret and basicAuth"}},
		{name: "a credential in headers", cfg: with(func(c *Config) { c.Headers = map[string]string{"Authorization": "Bearer x"} }), wantErr: []string{"headers.Authorization: credentials come from Secrets"}},
		{name: "no scopes", cfg: with(func(c *Config) { c.Limits.Scopes = nil }), wantErr: []string{"limits.scopes: at least one scope is required"}},
		{
			name:    "a scope with no labels",
			cfg:     with(func(c *Config) { c.Limits.Scopes = []mute.Scope{{}} }),
			wantErr: []string{"limits.scopes[0].labels: at least one label is required"},
		},
		{name: "the shortest maxDuration", cfg: with(func(c *Config) { c.Limits.MaxDuration = mute.MinDuration })},
		{name: "just below the shortest maxDuration", cfg: with(func(c *Config) { c.Limits.MaxDuration = mute.MinDuration - time.Second }), wantErr: []string{"limits.maxDuration: "}},
		{name: "the ceiling", cfg: with(func(c *Config) { c.Limits.MaxDuration = mute.CeilingMaxDuration })},
		{
			name:    "just above the ceiling",
			cfg:     with(func(c *Config) { c.Limits.MaxDuration = mute.CeilingMaxDuration + time.Second }),
			wantErr: []string{"limits.maxDuration: ", "above 24h"},
		},
		{name: "the longest createdBy", cfg: with(func(c *Config) { c.CreatedBy = strings.Repeat("a", maxCreatedByBytes) })},
		{name: "a createdBy too long", cfg: with(func(c *Config) { c.CreatedBy = strings.Repeat("a", maxCreatedByBytes+1) }), wantErr: []string{"createdBy: longer than 100 bytes"}},
		{name: "a createdBy with a line break", cfg: with(func(c *Config) { c.CreatedBy = "a\nb" }), wantErr: []string{"createdBy: must not hold control characters"}},
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

func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := testConfig("https://alertmanager.example.com")

	require.Equal(t, DefaultCreatedBy, cfg.createdBy())

	cfg.CreatedBy = "ops-bot"
	require.Equal(t, "ops-bot", cfg.createdBy())

	longest, err := mute.MaxDuration(cfg.Limits.MaxDuration)
	require.NoError(t, err)
	require.Equal(t, 2*time.Hour, longest)
}

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("makes no network call", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)

		_, err := New(t.Context(), "mute", testConfig(api.URL()), registry.Deps{HTTP: api.Client(), Secrets: fakeToken{}})
		require.NoError(t, err)
		require.Empty(t, api.Requests())
	})

	tests := []struct {
		name    string
		cfg     func(*Config)
		deps    registry.Deps
		wantErr error
	}{
		{name: "a token without a secrets resolver", cfg: func(c *Config) { c.BearerTokenSecret = ref("token") }, wantErr: errNoSecrets},
		{name: "a CA without a secrets resolver", cfg: func(c *Config) { c.TLS = &httpauth.TLS{CASecret: ref("ca")} }, wantErr: errNoSecrets},
		{
			name:    "tls with a transport whose TLS cannot be set",
			cfg:     func(c *Config) { c.TLS = &httpauth.TLS{CASecret: ref("ca")} },
			deps:    registry.Deps{Secrets: fakeToken{}, HTTP: &http.Client{Transport: otherTransport{}}},
			wantErr: httpauth.ErrNoTransport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := testConfig("https://alertmanager.example.com")
			tt.cfg(&cfg)

			_, err := New(t.Context(), "mute", cfg, tt.deps)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// otherTransport is a round tripper that is not an *http.Transport.
type otherTransport struct{}

func (otherTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errNoBody
}

func TestRegisteredAndReadFromYAML(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), Type)

	const text = `
url: https://mimir.example.com/alertmanager
bearerTokenSecret: {name: sidekick-am, key: token}
headers: {X-Scope-OrgID: team-a}
tls: {caSecret: {name: am-ca, key: ca.crt}}
createdBy: sidekick-prod
limits:
  maxDuration: 90m
  scopes:
    - labels:
        alertname: [HighLatency, HighErrorRate]
        service: ["checkout-*"]
`

	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(text), &node))

	built, err := registry.Actions.Build(t.Context(), Type, "mute-checkout", node.Content[0], registry.Deps{Secrets: fakeToken{}})
	require.NoError(t, err)
	require.Equal(t, "mute-checkout", built.Spec().Name)
	require.Contains(t, built.Spec().Description, "from 5m to 1h30m")

	for name, change := range map[string][2]string{
		"a scope with no labels":     {"        alertname: [HighLatency, HighErrorRate]\n        service: [\"checkout-*\"]", "        {}"},
		"a limit above the ceiling":  {"maxDuration: 90m", "maxDuration: 25h"},
		"a credential in the header": {"headers: {X-Scope-OrgID: team-a}", "headers: {Authorization: x}"},
	} {
		var bad yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(strings.Replace(text, change[0], change[1], 1)), &bad), name)

		_, err := registry.Actions.Check(t.Context(), Type, "mute-checkout", bad.Content[0])
		require.Error(t, err, name)
	}
}
