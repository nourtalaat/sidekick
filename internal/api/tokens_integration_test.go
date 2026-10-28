//go:build integration

package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTokenBody is the body of POST /tokens.
func newTokenBody(name, role string, expires time.Time) string {
	return fmt.Sprintf(`{"name":%q,"role":%q,"expiresAt":%q}`, name, role, expires.Format(time.RFC3339))
}

func TestCreateToken(t *testing.T) {
	t.Parallel()

	inMonth := time.Now().Add(30 * 24 * time.Hour)

	tests := []struct {
		name       string
		session    string
		csrf       string
		token      string
		body       string
		wantStatus int
		wantDetail string
	}{
		{
			name: "an admin creates a viewer token", session: adSession, csrf: adCSRF,
			body: newTokenBody("nightly bench", "viewer", inMonth), wantStatus: http.StatusCreated,
		},
		{
			name: "no CSRF token", session: adSession,
			body: newTokenBody("x", "viewer", inMonth), wantStatus: http.StatusForbidden,
			wantDetail: "Send the session's CSRF token in the X-CSRF-Token header.",
		},
		{
			name: "a viewer is denied", session: viSession, csrf: viCSRF,
			body: newTokenBody("x", "viewer", inMonth), wantStatus: http.StatusForbidden,
			wantDetail: "This needs the admin role.",
		},
		{
			name: "an admin token cannot create tokens", token: "skt_admin",
			body: newTokenBody("x", "viewer", inMonth), wantStatus: http.StatusForbidden,
			wantDetail: "Only a signed-in user can create or revoke API tokens.",
		},
		{
			name: "an expiry too far away", session: adSession, csrf: adCSRF,
			body: newTokenBody("x", "viewer", time.Now().Add(400*24*time.Hour)), wantStatus: http.StatusBadRequest,
			wantDetail: "The expiry must be in the future and at most a year away.",
		},
		{
			name: "an unknown role", session: adSession, csrf: adCSRF,
			body: newTokenBody("x", "owner", inMonth), wantStatus: http.StatusBadRequest,
			wantDetail: "The role must be viewer, responder, approver or admin, and at most your own.",
		},
		{
			name: "a body that is not JSON", session: adSession, csrf: adCSRF,
			body: "{", wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setup(t)

			var rec *httptest.ResponseRecorder
			if tt.token != "" {
				rec = s.doWithToken(t, http.MethodPost, "/api/v1/tokens", tt.token, strings.NewReader(tt.body))
			} else {
				rec = s.do(t, http.MethodPost, "/api/v1/tokens", tt.session, tt.csrf, strings.NewReader(tt.body))
			}

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusCreated {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				if tt.wantDetail != "" {
					require.Equal(t, tt.wantDetail, detailOf(t, rec.Body.Bytes()))
				}

				return
			}

			var created struct {
				Secret string `json:"secret"`
				Token  struct {
					Name      string `json:"name"`
					Role      string `json:"role"`
					CreatedBy string `json:"createdBy"`
				} `json:"token"`
			}

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
			require.True(t, strings.HasPrefix(created.Secret, "skt_"))
			require.Equal(t, "nightly bench", created.Token.Name)
			require.Equal(t, "viewer", created.Token.Role)
			require.Equal(t, "01920000-0000-7000-8000-000000000040", created.Token.CreatedBy)

			me := s.doWithToken(t, http.MethodGet, "/api/v1/me", created.Secret, nil)
			require.Equal(t, http.StatusOK, me.Code)
			require.Contains(t, me.Body.String(), `"kind":"token"`)
			require.NotContains(t, me.Body.String(), "csrfToken")
		})
	}
}

func TestListTokens(t *testing.T) {
	t.Parallel()

	s := setup(t)

	rec := s.do(t, http.MethodGet, "/api/v1/tokens", adSession, "", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var list struct {
		Tokens []map[string]any `json:"tokens"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Tokens, 2)
	require.Equal(t, "ops bot", list.Tokens[0]["name"])
	require.Equal(t, "dashboards", list.Tokens[1]["name"])

	for _, tok := range list.Tokens {
		require.NotContains(t, tok, "tokenHash")
		require.NotContains(t, tok, "secret")
	}

	denied := s.doWithToken(t, http.MethodGet, "/api/v1/tokens", "skt_viewer", nil)
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.Equal(t, "This needs the admin role.", detailOf(t, denied.Body.Bytes()))
}

func TestRevokeToken(t *testing.T) {
	t.Parallel()

	s := setup(t)

	const path = "/api/v1/tokens/01920000-0000-7000-8000-000000000041"

	rec := s.do(t, http.MethodDelete, path, adSession, adCSRF, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	again := s.do(t, http.MethodDelete, path, adSession, adCSRF, nil)
	require.Equal(t, http.StatusNotFound, again.Code)

	gone := s.doWithToken(t, http.MethodGet, "/api/v1/me", "skt_viewer", nil)
	require.Equal(t, http.StatusUnauthorized, gone.Code)

	bad := s.do(t, http.MethodDelete, "/api/v1/tokens/not-a-uuid", adSession, adCSRF, nil)
	require.Equal(t, http.StatusBadRequest, bad.Code)
	require.Equal(t, "application/problem+json", bad.Header().Get("Content-Type"))
}

// detailOf returns the detail of a problem details body.
func detailOf(t *testing.T, body []byte) string {
	t.Helper()

	var p struct {
		Detail string `json:"detail"`
	}

	require.NoError(t, json.Unmarshal(body, &p))

	return p.Detail
}
