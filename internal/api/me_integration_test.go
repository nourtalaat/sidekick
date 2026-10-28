//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetMe(t *testing.T) {
	t.Parallel()

	s := setup(t)

	tests := []struct {
		name       string
		method     string
		session    string
		csrf       string
		wantStatus int
		wantBody   string
	}{
		{
			name: "a viewer's session", method: http.MethodGet, session: viSession, wantStatus: http.StatusOK,
			wantBody: `{"kind":"user","id":"01920000-0000-7000-8000-000000000030","name":"Vi","email":"vi@example.com","role":"viewer","csrfToken":"csrf-vi"}`,
		},
		{
			name: "an expired session", method: http.MethodGet, session: viExpiredSession, wantStatus: http.StatusUnauthorized,
			wantBody: problem(http.StatusUnauthorized, "Sign in, or send an API token.", "/api/v1/me"),
		},
		{
			name: "no session", method: http.MethodGet, wantStatus: http.StatusUnauthorized,
			wantBody: problem(http.StatusUnauthorized, "Sign in, or send an API token.", "/api/v1/me"),
		},
		{
			name: "a change without the CSRF token", method: http.MethodPost, session: viSession, wantStatus: http.StatusForbidden,
			wantBody: problem(http.StatusForbidden, "Send the session's CSRF token in the X-CSRF-Token header.", "/api/v1/me"),
		},
		{
			name: "a change with the CSRF token reaches the routes", method: http.MethodPost, session: viSession, csrf: viCSRF,
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   problem(http.StatusMethodNotAllowed, "This path does not take this method.", "/api/v1/me"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, tt.method, "/api/v1/me", tt.session, tt.csrf, nil)
			require.Equal(t, tt.wantStatus, rec.Code)
			require.JSONEq(t, tt.wantBody, rec.Body.String())
		})
	}
}

func TestGetMeAfterLogin(t *testing.T) {
	t.Parallel()

	s := setup(t)

	rec := s.do(t, http.MethodGet, "/api/v1/me", s.login(t), "", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var me struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		Role      string `json:"role"`
		CSRFToken string `json:"csrfToken"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	require.Equal(t, "user", me.Kind)
	require.Equal(t, "Ana", me.Name)
	require.Equal(t, "ana@example.com", me.Email)
	require.Equal(t, "admin", me.Role)
	require.NotEmpty(t, me.CSRFToken)
}
