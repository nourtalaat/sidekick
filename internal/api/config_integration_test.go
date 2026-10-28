//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetConfig(t *testing.T) {
	t.Parallel()

	s := setup(t)

	tests := []struct {
		name       string
		session    string
		token      string
		wantStatus int
	}{
		{name: "an admin's session", session: adSession, wantStatus: http.StatusOK},
		{name: "an admin token", token: "skt_admin", wantStatus: http.StatusOK},
		{name: "a viewer is denied", session: viSession, wantStatus: http.StatusForbidden},
		{name: "a viewer token is denied", token: "skt_viewer", wantStatus: http.StatusForbidden},
		{name: "an expired session", session: viExpiredSession, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var rec *httptest.ResponseRecorder
			if tt.token != "" {
				rec = s.doWithToken(t, http.MethodGet, "/api/v1/config", tt.token, nil)
			} else {
				rec = s.do(t, http.MethodGet, "/api/v1/config", tt.session, "", nil)
			}

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				return
			}

			var got struct {
				File struct {
					Database struct {
						URLSecret map[string]string `json:"urlSecret"`
					} `json:"database"`
					OIDC struct {
						ClientSecret map[string]string   `json:"clientSecret"`
						Roles        map[string][]string `json:"roles"`
					} `json:"oidc"`
				} `json:"file"`
				Runtime struct {
					Listeners []struct {
						Name    string         `json:"name"`
						Enabled bool           `json:"enabled"`
						Version int            `json:"version"`
						Config  map[string]any `json:"config"`
					} `json:"listeners"`
					Routes []struct {
						Name    string `json:"name"`
						Enabled bool   `json:"enabled"`
					} `json:"routes"`
				} `json:"runtime"`
			}

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, map[string]string{"name": "sidekick-db", "key": "url"}, got.File.Database.URLSecret)
			require.Equal(t, map[string]string{"name": "sidekick-oidc", "key": "clientSecret"}, got.File.OIDC.ClientSecret)

			admins := got.File.OIDC.Roles["admin"]
			require.Len(t, admins, 2)
			require.Equal(t, "sre-admins", admins[0])
			require.NotContains(t, admins[1], "lead@example.com", "strings the redaction rules match are redacted")

			require.Len(t, got.Runtime.Listeners, 1)
			require.Equal(t, "ops", got.Runtime.Listeners[0].Name)
			require.True(t, got.Runtime.Listeners[0].Enabled)
			require.Equal(t, 2, got.Runtime.Listeners[0].Version)
			require.Equal(t, "#ops", got.Runtime.Listeners[0].Config["channel"])

			require.Len(t, got.Runtime.Routes, 1)
			require.Equal(t, "checkout", got.Runtime.Routes[0].Name)
			require.False(t, got.Runtime.Routes[0].Enabled)

			require.False(t, strings.Contains(rec.Body.String(), "client-secret"), "no secret value is shown")
		})
	}
}
