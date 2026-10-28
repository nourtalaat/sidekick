package prometheus

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// emptyVector is an instant query answer with no series.
const emptyVector = `{"resultType":"vector","result":[]}`

func TestSourceSendsCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		secrets  map[string]string
		tool     string
		input    string
		path     string
		answer   string
		wantAuth string
	}{
		{
			name:     "a bearer token from its secret, and the extra headers",
			cfg:      Config{BearerTokenSecret: ref("token"), Headers: map[string]string{"X-Scope-OrgID": "tenant-a"}},
			secrets:  map[string]string{"token": "abc123\n"},
			tool:     toolQuery,
			input:    `{"query":"up"}`,
			path:     queryPath,
			answer:   `{"status":"success","data":` + emptyVector + `}`,
			wantAuth: "Bearer abc123",
		},
		{
			name: "basic auth with the password from its secret",
			cfg: Config{BasicAuth: &BasicAuth{Username: "sidekick", PasswordSecret: *ref("password")},
				Headers: map[string]string{"X-Scope-OrgID": "tenant-a"}},
			secrets:  map[string]string{"password": "p@ss word\n"},
			tool:     toolQuery,
			input:    `{"query":"up"}`,
			path:     queryPath,
			answer:   `{"status":"success","data":` + emptyVector + `}`,
			wantAuth: "Basic " + base64.StdEncoding.EncodeToString([]byte("sidekick:p@ss word")),
		},
		{
			name: "Alertmanager gets the same credentials and headers",
			cfg: Config{AlertmanagerURL: "alertmanager", BearerTokenSecret: ref("token"),
				Headers: map[string]string{"X-Scope-OrgID": "tenant-a"}},
			secrets:  map[string]string{"token": "abc123"},
			tool:     toolListAlerts,
			input:    `{}`,
			path:     amAlertsPath,
			answer:   `[]`,
			wantAuth: "Bearer abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, tt.path, body(tt.answer))
			api.ExpectAllCalled()

			cfg := tt.cfg
			if cfg.AlertmanagerURL != "" {
				cfg.AlertmanagerURL = api.URL() + "/alertmanager"
			}

			s := newTestSource(t, api, cfg, secretsOf(t, tt.secrets))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})
			require.NoError(t, err)

			req := api.Requests()[0]
			require.Equal(t, tt.wantAuth, req.Header.Get("Authorization"))
			require.Equal(t, "tenant-a", req.Header.Get("X-Scope-OrgID"))
			require.Equal(t, "application/json", req.Header.Get("Accept"))
			require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "sidekick/"))
		})
	}
}

func TestSourceSecretFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   []byte
		err     error
		wantErr string
	}{
		{name: "an empty token", value: []byte(" \n"), wantErr: "secret sidekick-prometheus/token: the secret is empty"},
		{name: "a token that cannot be read", err: errors.New("no such file"), wantErr: "no such file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokenRef := registry.SecretRef{Name: "sidekick-prometheus", Key: "token"}
			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), tokenRef).Return(tt.value, tt.err)

			api := fakehttp.New(t)
			s := newTestSource(t, api, Config{BearerTokenSecret: &tokenRef}, secrets)

			_, err := s.Call(t.Context(), source.Call{Tool: toolQuery, Input: json.RawMessage(`{"query":"up"}`)})
			require.ErrorContains(t, err, tt.wantErr)

			_, isToolErr := errors.AsType[*source.ToolError](err)
			require.False(t, isToolErr)
			require.Empty(t, api.Requests(), "no request is sent without the credential")
		})
	}
}

func TestSourceTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// clientCert sends a client certificate.
		clientCert bool
		// caPEMs are what the CA secret holds on each call, in order; nil
		// means no CA secret. "server" stands for the server's certificate.
		caPEMs []string
		// wantErrs are the errors of the calls, in order; "" is a success.
		wantErrs []string
	}{
		{name: "a client certificate from its secrets", clientCert: true, wantErrs: []string{""}},
		{name: "a CA from its secret", caPEMs: []string{"server"}, wantErrs: []string{""}},
		{name: "a changed CA is picked up on the next call", caPEMs: []string{"other", "server"},
			wantErrs: []string{"certificate signed by unknown authority", ""}},
		{name: "a CA secret without a certificate", caPEMs: []string{"not a certificate"}, wantErrs: []string{errNoCertificates.Error()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t, fakehttp.WithClientCertificates())
			secrets := registry.NewMockSecrets(gomock.NewController(t))
			cfg := Config{TLS: &TLS{}}

			if tt.clientCert {
				certPEM, keyPEM := fakehttp.NewCertificate(t, "sidekick")
				cfg.TLS.CertSecret, cfg.TLS.KeySecret = ref("cert"), ref("key")
				secrets.EXPECT().Resolve(gomock.Any(), *ref("cert")).Return(certPEM, nil).AnyTimes()
				secrets.EXPECT().Resolve(gomock.Any(), *ref("key")).Return(keyPEM, nil).AnyTimes()
			}

			if tt.caPEMs != nil {
				cfg.TLS.CASecret = ref("ca")
				otherPEM, _ := fakehttp.NewCertificate(t, "other")

				for _, ca := range tt.caPEMs {
					value := []byte(ca)

					switch ca {
					case "server":
						value = api.CertificatePEM()
					case "other":
						value = otherPEM
					}

					secrets.EXPECT().Resolve(gomock.Any(), *ref("ca")).Return(value, nil)
				}
			}

			s := newTestSource(t, api, cfg, secrets)

			successes := 0

			for _, wantErr := range tt.wantErrs {
				if wantErr == "" {
					successes++

					api.Handle(http.MethodGet, queryPath, success(emptyVector))
				}

				_, err := s.Call(t.Context(), source.Call{Tool: toolQuery, Input: json.RawMessage(`{"query":"up"}`)})
				if wantErr != "" {
					require.ErrorContains(t, err, wantErr)

					continue
				}

				require.NoError(t, err)
			}

			reqs := api.Requests()
			require.Len(t, reqs, successes)

			if tt.clientCert {
				require.NotNil(t, reqs[0].ClientCertificate)
				require.Equal(t, "sidekick", reqs[0].ClientCertificate.Subject.CommonName)
			}
		})
	}
}

func TestSourceFailures(t *testing.T) {
	t.Parallel()

	runCalls(t, []callCase{
		{name: "a refused token is a failure of the source", tool: toolQuery, input: `{"query":"up"}`,
			routes:  []route{{http.MethodGet, queryPath, []fakehttp.Response{{Status: http.StatusUnauthorized, Body: []byte("Unauthorized\n")}}}},
			wantErr: "Prometheus answered 401: Unauthorized"},
		{name: "a rate limit is a failure of the source", tool: toolQuery, input: `{"query":"up"}`,
			routes:  []route{{http.MethodGet, queryPath, []fakehttp.Response{failed(http.StatusTooManyRequests, "", "too many requests")}}},
			wantErr: "Prometheus answered 429"},
		{name: "a query that timed out is a failure of the source", tool: toolQuery, input: `{"query":"up"}`,
			routes:  []route{{http.MethodGet, queryPath, []fakehttp.Response{failed(http.StatusServiceUnavailable, "timeout", "query timed out")}}},
			wantErr: "Prometheus answered 503 timeout: query timed out"},
		{name: "a proxy error page is a failure with its text", tool: toolQueryRange, input: `{"query":"up"}`,
			routes:  []route{{http.MethodGet, queryRangePath, []fakehttp.Response{{Status: http.StatusBadGateway, Body: []byte("<html>502 Bad Gateway</html>")}}}},
			wantErr: "Prometheus answered 502: <html>502 Bad Gateway</html>"},
		{name: "a query the server cannot run is a tool error", tool: toolQueryRange, input: `{"query":"up"}`,
			routes:        []route{{http.MethodGet, queryRangePath, []fakehttp.Response{failed(http.StatusBadRequest, "bad_data", "exceeded maximum resolution")}}},
			wantToolError: "Prometheus answered 400 bad_data: exceeded maximum resolution"},
		{name: "a forbidden tenant is a tool error", tool: toolQuery, input: `{"query":"up"}`,
			routes:        []route{{http.MethodGet, queryPath, []fakehttp.Response{{Status: http.StatusForbidden, Body: []byte("no org id")}}}},
			wantToolError: "Prometheus answered 403: no org id"},
		{name: "an answer that is not JSON is a failure", tool: toolQuery, input: `{"query":"up"}`,
			routes:  []route{{http.MethodGet, queryPath, []fakehttp.Response{body(`<html>`)}}},
			wantErr: "decode the answer of Prometheus"},
	})
}

func TestSourceAnswerTooLarge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		size int
	}{
		{name: "an answer over 16 MiB is a tool error that asks for a narrower query", size: maxResponseSize + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, queryPath, success(`{"resultType":"string","result":[1777888800,"`+strings.Repeat("x", tt.size)+`"]}`))

			s := newTestSource(t, api, Config{}, nil)

			_, err := s.Call(t.Context(), source.Call{Tool: toolQuery, Input: json.RawMessage(`{"query":"up"}`)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, "narrow the query")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "delete_series"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), Config{}, nil)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		withSecrets bool
		http        *http.Client
		wantErr     error
		wantText    string
	}{
		{name: "a token secret without a secrets resolver", cfg: Config{BaseURL: "https://prom.example.com", BearerTokenSecret: ref("token")},
			wantErr: errNoSecrets},
		{name: "a CA secret without a secrets resolver", cfg: Config{BaseURL: "https://prom.example.com", TLS: &TLS{CASecret: ref("ca")}},
			wantErr: errNoSecrets},
		{name: "tls with a transport whose TLS cannot be set", cfg: Config{BaseURL: "https://prom.example.com", TLS: &TLS{CASecret: ref("ca")}},
			withSecrets: true, http: &http.Client{Transport: otherTransport{}}, wantErr: errNoTransport},
		{name: "settings that do not check", cfg: Config{BaseURL: "https://prom.example.com", Settings: source.Settings{Concurrency: -1}},
			wantText: "concurrency: must not be negative"},
		{name: "no credentials need no secrets resolver", cfg: Config{BaseURL: "http://prometheus-operated.monitoring:9090/"}},
		{name: "every setting", withSecrets: true, cfg: Config{
			BaseURL: "https://mimir.example.com/prometheus", AlertmanagerURL: "https://mimir.example.com/alertmanager",
			BasicAuth: &BasicAuth{Username: "sidekick", PasswordSecret: *ref("password")},
			TLS:       &TLS{CertSecret: ref("cert"), KeySecret: ref("key"), CASecret: ref("ca")},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deps := registry.Deps{HTTP: tt.http}
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "prometheus", tt.cfg, deps)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)

				return
			case tt.wantText != "":
				require.ErrorContains(t, err, tt.wantText)

				return
			}

			require.NoError(t, err)

			tools, err := s.Tools(t.Context())
			require.NoError(t, err)
			require.Len(t, tools, len(toolList))
			require.NoError(t, s.Close())

			built, ok := s.(*Source)
			require.True(t, ok)
			require.Equal(t, strings.TrimSuffix(tt.cfg.BaseURL, "/"), built.prom.root)
			require.Equal(t, strings.TrimSuffix(tt.cfg.BaseURL, "/"), built.links.prometheus, "links go to the base URL by default")
			require.Equal(t, tt.cfg.AlertmanagerURL != "", built.alertmanager != nil)
		})
	}
}

func TestLinksQuery(t *testing.T) {
	t.Parallel()

	w := window{start: testNow.Add(-90 * time.Minute), end: testNow}
	explorePanes := `{"sidekick":{"datasource":"prom-main","queries":[{"refId":"A","expr":"rate(up[5m])","range":true,"instant":false,` +
		`"datasource":{"type":"prometheus","uid":"prom-main"}}],"range":{"from":"1777883400000","to":"1777888800000"}}}`

	tests := []struct {
		name    string
		links   links
		instant bool
		want    source.Evidence
	}{
		{
			name:  "a range query graphs in the Prometheus web UI",
			links: links{prometheus: "https://prom.example.com"},
			want: source.Evidence{Kind: source.EvidenceMetric, Title: "Q in Prometheus", At: testNow,
				URL: "https://prom.example.com/graph?g0.end_input=2026-05-04+10%3A00%3A00&g0.expr=rate%28up%5B5m%5D%29&g0.range_input=1h30m&g0.tab=0"},
		},
		{
			name:    "an instant query is a table at its time",
			links:   links{prometheus: "https://prom.example.com"},
			instant: true,
			want: source.Evidence{Kind: source.EvidenceMetric, Title: "Q in Prometheus", At: testNow,
				URL: "https://prom.example.com/graph?g0.expr=rate%28up%5B5m%5D%29&g0.moment_input=2026-05-04+10%3A00%3A00&g0.tab=1"},
		},
		{
			name:  "with a Grafana the query opens in Explore",
			links: links{prometheus: "https://prom.example.com", grafana: "https://grafana.example.com", datasource: "prom-main"},
			want: source.Evidence{Kind: source.EvidenceMetric, Title: "Q in Grafana Explore", At: testNow,
				URL: "https://grafana.example.com/explore?panes=" + url.QueryEscape(explorePanes) + "&schemaVersion=1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.links.query("Q", "rate(up[5m])", w, tt.instant))
		})
	}
}

func TestUIDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "less than a second", d: 0, want: "1s"},
		{name: "minutes and seconds", d: 90 * time.Second, want: "1m30s"},
		{name: "a day and an hour", d: 25 * time.Hour, want: "1d1h"},
		{name: "a week", d: maxRange, want: "7d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, uiDuration(tt.d))
		})
	}
}

// otherTransport is an http.RoundTripper that is not an *http.Transport.
type otherTransport struct {
	http.RoundTripper
}
