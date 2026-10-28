package prometheus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	const base = "https://mimir.example.com/prometheus"

	password := registry.SecretRef{Name: "sidekick-prometheus", Key: "password"}

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "an in-cluster Prometheus over http without credentials", cfg: Config{BaseURL: "http://prometheus-operated.monitoring:9090"}},
		{name: "every setting", cfg: Config{
			BaseURL: base, AlertmanagerURL: "https://mimir.example.com/alertmanager",
			BasicAuth: &BasicAuth{Username: "sidekick", PasswordSecret: password},
			TLS:       &TLS{CertSecret: ref("cert"), KeySecret: ref("key"), CASecret: ref("ca")},
			Headers:   map[string]string{"X-Scope-OrgID": "tenant-a"},
			Links:     Links{PrometheusURL: "https://prometheus.example.com", GrafanaURL: "https://grafana.example.com", GrafanaDatasource: "prom-main"},
			Settings:  source.Settings{Timeout: time.Minute},
		}},
		{name: "a bearer token to a loopback host over http", cfg: Config{BaseURL: "http://127.0.0.1:9090", BearerTokenSecret: ref("token")}},
		{name: "no base URL", cfg: Config{}, wantErr: []string{"baseURL: required"}},
		{name: "a base URL without a scheme", cfg: Config{BaseURL: "prometheus:9090"}, wantErr: []string{"must be an absolute http or https URL"}},
		{name: "a base URL with a query", cfg: Config{BaseURL: "https://prom.example.com/?x=1"}, wantErr: []string{"baseURL:"}},
		{name: "a base URL with a password", cfg: Config{BaseURL: "https://sidekick:secret@prom.example.com"},
			wantErr: []string{"baseURL: must not hold a user name or password"}},
		{name: "a bearer token over http", cfg: Config{BaseURL: "http://prometheus:9090", BearerTokenSecret: ref("token")},
			wantErr: []string{"baseURL: \"http://prometheus:9090\" must use https, because credentials are sent"}},
		{name: "basic auth to an Alertmanager over http",
			cfg:     Config{BaseURL: base, AlertmanagerURL: "http://alertmanager:9093", BasicAuth: &BasicAuth{Username: "u", PasswordSecret: password}},
			wantErr: []string{"alertmanagerURL: \"http://alertmanager:9093\" must use https"}},
		{name: "a bearer token and basic auth",
			cfg:     Config{BaseURL: base, BearerTokenSecret: ref("token"), BasicAuth: &BasicAuth{Username: "u", PasswordSecret: password}},
			wantErr: []string{"set one of bearerTokenSecret and basicAuth"}},
		{name: "basic auth without a user or a password", cfg: Config{BaseURL: base, BasicAuth: &BasicAuth{}},
			wantErr: []string{"basicAuth.username: required", "basicAuth.passwordSecret: required"}},
		{name: "a client certificate without its key", cfg: Config{BaseURL: base, TLS: &TLS{CertSecret: ref("cert")}},
			wantErr: []string{"tls: set both certSecret and keySecret"}},
		{name: "an empty tls section", cfg: Config{BaseURL: base, TLS: &TLS{}}, wantErr: []string{"tls: set certSecret and keySecret, caSecret, or both"}},
		{name: "a CA for an http base URL", cfg: Config{BaseURL: "http://prometheus:9090", TLS: &TLS{CASecret: ref("ca")}},
			wantErr: []string{"tls: needs https, but baseURL uses http"}},
		{name: "a client certificate over http", cfg: Config{BaseURL: "http://prometheus:9090", TLS: &TLS{CertSecret: ref("cert"), KeySecret: ref("key")}},
			wantErr: []string{"must use https, because credentials are sent", "tls: needs https"}},
		{name: "credentials in headers", cfg: Config{BaseURL: base, Headers: map[string]string{"authorization": "Bearer x", "Cookie": "a=b"}},
			wantErr: []string{"headers.Cookie: credentials come from Secrets", "headers.authorization: credentials come from Secrets"}},
		{name: "a header Sidekick sets", cfg: Config{BaseURL: base, Headers: map[string]string{"User-Agent": "x"}},
			wantErr: []string{"headers.User-Agent: Sidekick sets this header itself"}},
		{name: "a header name that is not a token", cfg: Config{BaseURL: base, Headers: map[string]string{"X Org": "a"}},
			wantErr: []string{`headers.X Org: "X Org" is not a valid header name`}},
		{name: "a header value with a line break", cfg: Config{BaseURL: base, Headers: map[string]string{"X-Scope-OrgID": "a\r\nHost: evil"}},
			wantErr: []string{"headers.X-Scope-OrgID: the value must not hold control characters"}},
		{name: "a Grafana without a data source", cfg: Config{BaseURL: base, Links: Links{GrafanaURL: "https://grafana.example.com"}},
			wantErr: []string{"links.grafanaDatasource: required with links.grafanaURL"}},
		{name: "a data source without a Grafana", cfg: Config{BaseURL: base, Links: Links{GrafanaDatasource: "prom"}},
			wantErr: []string{"links.grafanaDatasource: only with links.grafanaURL"}},
		{name: "a data source UID that is not one", cfg: Config{BaseURL: base, Links: Links{GrafanaURL: "https://g.example.com", GrafanaDatasource: "a b"}},
			wantErr: []string{`links.grafanaDatasource: "a b" is not a Grafana data source UID`}},
		{name: "a web UI URL that is not absolute", cfg: Config{BaseURL: base, Links: Links{PrometheusURL: "/prometheus"}},
			wantErr: []string{"links.prometheusURL: \"/prometheus\" must be an absolute http or https URL"}},
		{name: "a timeout above the HTTP client's", cfg: Config{BaseURL: base, Settings: source.Settings{Timeout: 2 * time.Minute}},
			wantErr: []string{"timeout: 2m0s is above 1m0s"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}
