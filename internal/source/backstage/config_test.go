package backstage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	const base = "https://backstage.example.com"

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the smallest config", cfg: Config{BaseURL: base, TokenSecret: testTokenRef}},
		{
			name: "every key",
			cfg: Config{
				BaseURL:     base + "/",
				AppURL:      "http://backstage-web.internal:3000",
				TokenSecret: testTokenRef,
				Namespace:   "platform",
				Settings:    source.Settings{Timeout: 45 * time.Second},
			},
		},
		{name: "plain http on localhost", cfg: Config{BaseURL: "http://localhost:7007", TokenSecret: testTokenRef}},
		{
			name: "nothing set",
			want: []string{"baseURL: required, for example https://backstage.example.com", "tokenSecret: required"},
		},
		{
			name: "a base URL that is not absolute",
			cfg:  Config{BaseURL: "backstage.example.com", TokenSecret: testTokenRef},
			want: []string{`baseURL: "backstage.example.com" must be an absolute https URL`},
		},
		{
			name: "a base URL with a query",
			cfg:  Config{BaseURL: base + "?x=1", TokenSecret: testTokenRef},
			want: []string{`baseURL: "https://backstage.example.com?x=1" must be an absolute https URL`},
		},
		{
			name: "a plain http base URL on the network",
			cfg:  Config{BaseURL: "http://backstage.example.com", TokenSecret: testTokenRef},
			want: []string{`baseURL: "http://backstage.example.com" must use https; plain http is only allowed for localhost`},
		},
		{
			name: "an app URL that is not absolute",
			cfg:  Config{BaseURL: base, AppURL: "/catalog", TokenSecret: testTokenRef},
			want: []string{`appURL: "/catalog" must be an absolute http or https URL`},
		},
		{
			name: "a bad namespace",
			cfg:  Config{BaseURL: base, TokenSecret: testTokenRef, Namespace: "a b"},
			want: []string{`namespace: "a b" is not a valid catalog namespace`},
		},
		{
			name: "a timeout above the HTTP client's limit",
			cfg:  Config{BaseURL: base, TokenSecret: testTokenRef, Settings: source.Settings{Timeout: 61 * time.Second}},
			want: []string{"timeout: 1m1s is above 1m0s, the longest the HTTP client waits for response headers"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.Equal(t, tt.want, strings.Split(err.Error(), "\n"))
		})
	}
}

func TestConfigURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cfg           Config
		wantAPI       string
		wantApp       string
		wantNamespace string
	}{
		{
			name:          "one host for both, with the defaults",
			cfg:           Config{BaseURL: "https://backstage.example.com/"},
			wantAPI:       "https://backstage.example.com/api/catalog",
			wantApp:       "https://backstage.example.com",
			wantNamespace: "default",
		},
		{
			name:          "a separate web app and a namespace",
			cfg:           Config{BaseURL: "https://backstage-api.example.com", AppURL: "https://backstage.example.com/", Namespace: "platform"},
			wantAPI:       "https://backstage-api.example.com/api/catalog",
			wantApp:       "https://backstage.example.com",
			wantNamespace: "platform",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantAPI, tt.cfg.apiURL())
			require.Equal(t, tt.wantApp, tt.cfg.appURL())
			require.Equal(t, tt.wantNamespace, tt.cfg.namespace())
		})
	}
}
