package grafanasilence

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

// maxCreatedByBytes is the longest createdBy name.
const maxCreatedByBytes = 100

func ref(name string) *registry.SecretRef {
	return &registry.SecretRef{Name: name, Key: "value"}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	with := func(change func(*Config)) Config {
		c := testConfig("https://acme.grafana.net")
		change(&c)

		return c
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "the smallest config", cfg: testConfig("https://acme.grafana.net")},
		{
			name: "everything set",
			cfg: with(func(c *Config) {
				c.URL = "https://example.com/grafana"
				c.TLS = &httpauth.TLS{CASecret: ref("ca"), CertSecret: ref("cert"), KeySecret: ref("key")}
				c.CreatedBy = "sidekick-prod"
				c.Limits.MaxDuration = 24 * time.Hour
			}),
		},
		{name: "http to a loopback host", cfg: with(func(c *Config) { c.URL = "http://127.0.0.1:3000" })},
		{name: "no url", cfg: with(func(c *Config) { c.URL = "" }), wantErr: []string{"url: required"}},
		{name: "http to another host", cfg: with(func(c *Config) { c.URL = "http://grafana:3000" }), wantErr: []string{"url: ", "must use https"}},
		{name: "a url with a password", cfg: with(func(c *Config) { c.URL = "https://u:p@acme.grafana.net" }), wantErr: []string{"must not hold a user name or password"}},
		{name: "a url with a query", cfg: with(func(c *Config) { c.URL = "https://acme.grafana.net?x=1" }), wantErr: []string{"url: "}},
		{name: "no token", cfg: with(func(c *Config) { c.TokenSecret = registry.SecretRef{} }), wantErr: []string{"tokenSecret: required"}},
		{
			name:    "tls with http",
			cfg:     with(func(c *Config) { c.URL = "http://localhost:3000"; c.TLS = &httpauth.TLS{CASecret: ref("ca")} }),
			wantErr: []string{"tls: needs https"},
		},
		{name: "tls with a certificate and no key", cfg: with(func(c *Config) { c.TLS = &httpauth.TLS{CertSecret: ref("cert")} }), wantErr: []string{"tls: set both certSecret and keySecret"}},
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

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("makes no network call", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)

		_, err := New(t.Context(), "mute", testConfig(api.URL()), registry.Deps{HTTP: api.Client(), Secrets: &fakeSecrets{}})
		require.NoError(t, err)
		require.Empty(t, api.Requests())
	})

	t.Run("needs a secrets resolver for the token", func(t *testing.T) {
		t.Parallel()

		_, err := New(t.Context(), "mute", testConfig("https://acme.grafana.net"), registry.Deps{})
		require.ErrorContains(t, err, "no secrets resolver")
	})

	t.Run("refuses a limit above the ceiling", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig("https://acme.grafana.net")
		cfg.Limits.MaxDuration = mute.CeilingMaxDuration + time.Second

		_, err := New(t.Context(), "mute", cfg, registry.Deps{Secrets: &fakeSecrets{}})
		require.ErrorContains(t, err, "limits.maxDuration")
	})
}

func TestRegisteredAndReadFromYAML(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), Type)

	const text = `
url: https://acme.grafana.net
tokenSecret: {name: sidekick-grafana-silence, key: token}
tls: {caSecret: {name: grafana-ca, key: ca.crt}}
createdBy: sidekick-prod
limits:
  maxDuration: 90m
  scopes:
    - labels:
        alertname: [HighLatency, HighErrorRate]
        grafana_folder: ["payments-*"]
`

	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(text), &node))

	built, err := registry.Actions.Build(t.Context(), Type, "mute-payments", node.Content[0], registry.Deps{Secrets: &fakeSecrets{}})
	require.NoError(t, err)
	require.Equal(t, "mute-payments", built.Spec().Name)
	require.Contains(t, built.Spec().Description, "Silences alerts in Grafana")
	require.Contains(t, built.Spec().Description, "from 5m to 1h30m")

	for name, change := range map[string][2]string{
		"a scope with no labels":    {"        alertname: [HighLatency, HighErrorRate]\n        grafana_folder: [\"payments-*\"]", "        {}"},
		"a limit above the ceiling": {"maxDuration: 90m", "maxDuration: 25h"},
		"no token":                  {"tokenSecret: {name: sidekick-grafana-silence, key: token}\n", ""},
		"an http url":               {"https://acme.grafana.net", "http://grafana.example.com"},
	} {
		var bad yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(strings.Replace(text, change[0], change[1], 1)), &bad), name)

		_, err := registry.Actions.Check(t.Context(), Type, "mute-payments", bad.Content[0])
		require.Error(t, err, name)
	}
}

// otherTransport is a round tripper that is not an *http.Transport.
type otherTransport struct{}

func (otherTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}

func TestNewRefusesTLSOnATransportItCannotChange(t *testing.T) {
	t.Parallel()

	cfg := testConfig("https://acme.grafana.net")
	cfg.TLS = &httpauth.TLS{CASecret: ref("ca")}

	_, err := New(t.Context(), "mute", cfg, registry.Deps{Secrets: &fakeSecrets{}, HTTP: &http.Client{Transport: otherTransport{}}})
	require.ErrorIs(t, err, httpauth.ErrNoTransport)
}
