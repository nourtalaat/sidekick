//go:build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	themeURL = "/api/v1/me/theme"
	meURL    = "/api/v1/me"
)

func TestSetMyTheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantTheme  string
		wantDetail string
	}{
		{name: "system", body: `{"theme":"system"}`, wantStatus: http.StatusNoContent, wantTheme: "system"},
		{name: "light", body: `{"theme":"light"}`, wantStatus: http.StatusNoContent, wantTheme: "light"},
		{name: "dark", body: `{"theme":"dark"}`, wantStatus: http.StatusNoContent, wantTheme: "dark"},
		{name: "another value", body: `{"theme":"purple"}`, wantStatus: http.StatusUnprocessableEntity, wantDetail: "The theme must be system, light or dark."},
		{name: "a different case", body: `{"theme":"Dark"}`, wantStatus: http.StatusUnprocessableEntity, wantDetail: "The theme must be system, light or dark."},
		{name: "an empty value", body: `{"theme":""}`, wantStatus: http.StatusUnprocessableEntity, wantDetail: "The theme must be system, light or dark."},
		{name: "no theme", body: `{}`, wantStatus: http.StatusUnprocessableEntity, wantDetail: "The theme must be system, light or dark."},
		{name: "a body that is not JSON", body: `dark`, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setup(t)

			rec := s.do(t, http.MethodPut, themeURL, viSession, viCSRF, strings.NewReader(tt.body))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantDetail != "" {
				require.JSONEq(t, `{"type":"about:blank","title":"Unprocessable Entity","status":422,"detail":"`+tt.wantDetail+`","instance":"`+themeURL+`","code":"validation",`+
					`"errors":[{"field":"theme","message":"`+tt.wantDetail+`"}]}`, rec.Body.String())
			}

			require.Equal(t, tt.wantTheme, themeOf(t, s, "Vi"), "the stored theme")
		})
	}
}

func TestSetMyThemeIsReturnedByGetMe(t *testing.T) {
	t.Parallel()

	s := setup(t)

	require.Equal(t, http.StatusNoContent, s.do(t, http.MethodPut, themeURL, viSession, viCSRF, strings.NewReader(`{"theme":"dark"}`)).Code)

	rec := s.do(t, http.MethodGet, meURL, viSession, "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t,
		`{"kind":"user","id":"01920000-0000-7000-8000-000000000030","name":"Vi","email":"vi@example.com","role":"viewer","csrfToken":"csrf-vi","theme":"dark"}`,
		rec.Body.String())

	require.Equal(t, http.StatusNoContent, s.do(t, http.MethodPut, themeURL, viSession, viCSRF, strings.NewReader(`{"theme":"light"}`)).Code)
	require.Equal(t, "light", themeOf(t, s, "Vi"), "a second choice replaces the first")
	require.Empty(t, themeOf(t, s, "Ad"), "another user keeps no theme")
}

func TestSetMyThemeNeedsASession(t *testing.T) {
	t.Parallel()

	const body = `{"theme":"dark"}`

	tests := []struct {
		name       string
		session    string
		csrf       string
		token      string
		wantStatus int
		wantDetail string
	}{
		{name: "no CSRF header", session: viSession, wantStatus: http.StatusForbidden, wantDetail: "Send the session's CSRF token in the X-CSRF-Token header."},
		{name: "a wrong CSRF token", session: viSession, csrf: adCSRF, wantStatus: http.StatusForbidden, wantDetail: "Send the session's CSRF token in the X-CSRF-Token header."},
		{name: "no session", wantStatus: http.StatusUnauthorized, wantDetail: "Sign in, or send an API token."},
		{name: "an expired session", session: viExpiredSession, csrf: "csrf-vi-old", wantStatus: http.StatusUnauthorized, wantDetail: "Sign in, or send an API token."},
		{name: "a viewer token", token: "skt_viewer", wantStatus: http.StatusForbidden, wantDetail: "API tokens have no theme."},
		{name: "an admin token", token: "skt_admin", wantStatus: http.StatusForbidden, wantDetail: "API tokens have no theme."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setup(t)

			rec := s.do(t, http.MethodPut, themeURL, tt.session, tt.csrf, strings.NewReader(body))
			if tt.token != "" {
				rec = s.doWithToken(t, http.MethodPut, themeURL, tt.token, strings.NewReader(body))
			}

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tt.wantDetail)
			require.Empty(t, themeOf(t, s, "Vi"), "nothing is stored")
		})
	}
}

func TestGetMeOfATokenHasNoTheme(t *testing.T) {
	t.Parallel()

	s := setup(t)

	_, err := s.pool.Exec(t.Context(), `UPDATE users SET theme = 'dark' WHERE name = 'Ad'`)
	require.NoError(t, err)

	rec := s.doWithToken(t, http.MethodGet, meURL, "skt_admin", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "theme")
}

func TestSetMyThemeWritesNoAuditRow(t *testing.T) {
	t.Parallel()

	s := setup(t)

	count := func() int {
		var n int

		require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log").Scan(&n))

		return n
	}

	before := count()

	require.Equal(t, http.StatusNoContent, s.do(t, http.MethodPut, themeURL, viSession, viCSRF, strings.NewReader(`{"theme":"dark"}`)).Code)
	require.Equal(t, before, count())
}

// themeOf returns the stored theme of the user with name, empty for none.
func themeOf(t *testing.T, s server, name string) string {
	t.Helper()

	var theme *string

	require.NoError(t, s.pool.QueryRow(t.Context(), `SELECT theme FROM users WHERE name = $1`, name).Scan(&theme))

	if theme == nil {
		return ""
	}

	return *theme
}
