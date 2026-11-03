package datadogapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
)

var (
	apiRef = registry.SecretRef{Name: "datadog", Key: "apiKey"}
	appRef = registry.SecretRef{Name: "datadog", Key: "appKey"}
)

func TestCheckConnection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		site    string
		url     string
		apiKey  registry.SecretRef
		appKey  registry.SecretRef
		wantErr string
	}{
		{name: "default site", apiKey: apiRef, appKey: appRef},
		{name: "a regional site", site: "us3.datadoghq.com", apiKey: apiRef, appKey: appRef},
		{name: "an https url", url: "https://api.datadoghq.eu", apiKey: apiRef, appKey: appRef},
		{name: "http on loopback", url: "http://127.0.0.1:9", apiKey: apiRef, appKey: appRef},
		{name: "site and url", site: "datadoghq.eu", url: "https://api.datadoghq.eu", apiKey: apiRef, appKey: appRef, wantErr: "url: cannot be set together with site"},
		{name: "http off loopback", url: "http://api.example.com", apiKey: apiRef, appKey: appRef, wantErr: "must use https"},
		{name: "a site that is not a host", site: "not a site", apiKey: apiRef, appKey: appRef, wantErr: "site:"},
		{name: "no api key", appKey: appRef, wantErr: "apiKeySecret: required"},
		{name: "no app key", apiKey: apiRef, wantErr: "appKeySecret: required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := CheckConnection(tt.site, tt.url, tt.apiKey, tt.appKey)

			if tt.wantErr == "" {
				require.Empty(t, errs)

				return
			}

			require.Len(t, errs, 1)
			require.ErrorContains(t, errs[0], tt.wantErr)
		})
	}
}

func TestAddresses(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://api.datadoghq.com", APIURL("", ""), "the default site")
	require.Equal(t, "https://api.us3.datadoghq.com", APIURL("us3.datadoghq.com", ""))
	require.Equal(t, "http://127.0.0.1:9", APIURL("", "http://127.0.0.1:9/"), "a url replaces the site")

	require.Equal(t, "app.datadoghq.com", AppHost(""))
	require.Equal(t, "app.datadoghq.eu", AppHost("datadoghq.eu"))
	require.Equal(t, "us3.datadoghq.com", AppHost("us3.datadoghq.com"))
}
