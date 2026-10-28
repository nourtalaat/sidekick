//go:build integration

package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/auth/authtest"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// sessionCookie is the name of the session cookie.
const sessionCookie = "__Host-sidekick_session"

func TestLogin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// query is added to /auth/login.
		query string
		// claims changes the ID token's claims.
		claims func(authtest.Claims)
		// tamper changes the query the provider sends back.
		tamper       func(url.Values) url.Values
		wantStatus   int
		wantLocation string
		wantDetail   string
		wantRole     roles.Role
		wantEmail    *string
	}{
		{
			name: "a good login starts a session", wantStatus: http.StatusSeeOther, wantLocation: "/",
			wantRole: roles.Admin, wantEmail: ptr("ana@example.com"),
		},
		{
			name: "the browser goes back to the page the login started from", query: "?return=/investigations/1",
			wantStatus: http.StatusSeeOther, wantLocation: "/investigations/1", wantRole: roles.Admin, wantEmail: ptr("ana@example.com"),
		},
		{
			name: "a return path off the site goes to the home page", query: "?return=//evil.example.com/x",
			wantStatus: http.StatusSeeOther, wantLocation: "/", wantRole: roles.Admin, wantEmail: ptr("ana@example.com"),
		},
		{
			name: "a groups claim that is one string", claims: func(c authtest.Claims) { c["groups"] = "engineering" },
			wantStatus: http.StatusSeeOther, wantLocation: "/", wantRole: roles.Responder, wantEmail: ptr("ana@example.com"),
		},
		{
			name: "an email marked unverified is not kept", claims: func(c authtest.Claims) { c["email_verified"] = false },
			wantStatus: http.StatusSeeOther, wantLocation: "/", wantRole: roles.Admin,
		},
		{
			name: "a token without email_verified keeps the email", claims: func(c authtest.Claims) { delete(c, "email_verified") },
			wantStatus: http.StatusSeeOther, wantLocation: "/", wantRole: roles.Admin, wantEmail: ptr("ana@example.com"),
		},
		{
			name: "a bad nonce", claims: func(c authtest.Claims) { c["nonce"] = "not-the-logins" },
			wantStatus: http.StatusUnauthorized, wantDetail: "The ID token is not for this login. Sign in again.",
		},
		{
			name: "a token for another client", claims: func(c authtest.Claims) { c["aud"] = "someone-else" },
			wantStatus: http.StatusUnauthorized, wantDetail: "The ID token is not valid. Sign in again.",
		},
		{
			name: "a token from another issuer", claims: func(c authtest.Claims) { c["iss"] = "https://evil.example.com" },
			wantStatus: http.StatusUnauthorized, wantDetail: "The ID token is not valid. Sign in again.",
		},
		{
			name: "an expired token", claims: func(c authtest.Claims) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
			wantStatus: http.StatusUnauthorized, wantDetail: "The ID token is not valid. Sign in again.",
		},
		{
			name: "no group gives a role", claims: func(c authtest.Claims) { c["groups"] = []string{"sales"} },
			wantStatus: http.StatusForbidden, wantDetail: "None of your groups gives you a Sidekick role. Ask an admin to add one to oidc.roles.",
		},
		{
			name: "a state that is not the login's", tamper: func(q url.Values) url.Values { q.Set("state", "forged"); return q },
			wantStatus: http.StatusBadRequest, wantDetail: "This login expired or started in another browser. Sign in again.",
		},
		{
			name: "a code the provider does not know", tamper: func(q url.Values) url.Values { q.Set("code", "forged"); return q },
			wantStatus: http.StatusUnauthorized, wantDetail: "The identity provider did not accept the login. Sign in again.",
		},
		{
			name: "the provider refused the login",
			tamper: func(q url.Values) url.Values {
				return url.Values{"error": {"access_denied"}, "state": {q.Get("state")}}
			},
			wantStatus: http.StatusUnauthorized, wantDetail: "The identity provider did not sign you in: access_denied.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := setup(t)
			if tt.claims != nil {
				e.provider.SetClaims(tt.claims)
			}

			start := e.startLogin(t, tt.query)
			require.Equal(t, http.StatusFound, start.Code)

			login := cookie(start, "__Host-sidekick_login")
			require.NotNil(t, login)
			require.True(t, login.Secure)
			require.True(t, login.HttpOnly)
			require.Equal(t, http.SameSiteLaxMode, login.SameSite)

			query := e.provider.Authorize(start.Header().Get("Location"))
			if tt.tamper != nil {
				query = tt.tamper(query)
			}

			rec := e.callback(t, query, start)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			cleared := cookie(rec, "__Host-sidekick_login")
			require.NotNil(t, cleared)
			require.Negative(t, cleared.MaxAge)

			if tt.wantDetail != "" {
				require.Equal(t, tt.wantDetail, detailOf(t, rec))
				require.Nil(t, cookie(rec, sessionCookie))

				return
			}

			require.Equal(t, tt.wantLocation, rec.Header().Get("Location"))

			session := cookie(rec, sessionCookie)
			require.NotNil(t, session)
			require.True(t, session.Secure)
			require.True(t, session.HttpOnly)
			require.Equal(t, http.SameSiteLaxMode, session.SameSite)
			require.Equal(t, int(auth.SessionLifetime.Seconds()), session.MaxAge)

			p, err := e.svc.Authenticate(requestWith(t, http.MethodGet, session, ""))
			require.NoError(t, err)
			require.Equal(t, auth.KindUser, p.Kind)
			require.Equal(t, tt.wantRole, p.Role)
			require.Equal(t, "Ana", p.Name)
			require.NotEmpty(t, p.CSRFToken)

			var email *string

			require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT email FROM users WHERE oidc_subject = 'user-1'").Scan(&email))
			require.Equal(t, tt.wantEmail, email)

			var oldEmail *string

			require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT email FROM users WHERE oidc_subject = 'old-ana'").Scan(&oldEmail))

			if tt.wantEmail != nil {
				require.Nil(t, oldEmail, "the newer login takes the email over")
			} else {
				require.Equal(t, ptr("ANA@example.com"), oldEmail)
			}

			require.Equal(t, []auditRow{{Action: auth.ActionUserCreated, Details: `{"new": "` + roleName(tt.wantRole) + `"}`}}, auditRows(t, e))
		})
	}
}

func TestLoginAgainWithAnotherRole(t *testing.T) {
	t.Parallel()

	e := setup(t)
	require.Equal(t, http.StatusSeeOther, e.login(t).Code)
	require.Equal(t, http.StatusSeeOther, e.login(t).Code, "the same role writes no audit row")

	e.provider.SetClaims(func(c authtest.Claims) { c["groups"] = []string{"engineering"}; c["name"] = "Ana B" })

	rec := e.login(t)
	require.Equal(t, http.StatusSeeOther, rec.Code)

	p, err := e.svc.Authenticate(requestWith(t, http.MethodGet, cookie(rec, sessionCookie), ""))
	require.NoError(t, err)
	require.Equal(t, roles.Responder, p.Role)
	require.Equal(t, "Ana B", p.Name)

	require.Equal(t, []auditRow{
		{Action: auth.ActionUserCreated, Details: `{"new": "admin"}`},
		{Action: auth.ActionUserRoleChanged, Details: `{"new": "responder", "old": "admin"}`},
	}, auditRows(t, e))

	var users, sessions int

	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE oidc_subject = 'user-1'").Scan(&users))
	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions s JOIN users u ON u.id = s.user_id WHERE u.oidc_subject = 'user-1'").Scan(&sessions))
	require.Equal(t, 1, users)
	require.Equal(t, 3, sessions)
}

func TestLoginRemovesExpiredSessions(t *testing.T) {
	t.Parallel()

	e := setup(t)
	require.Equal(t, http.StatusSeeOther, e.login(t).Code)

	var expired int

	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE expires_at <= now()").Scan(&expired))
	require.Zero(t, expired)
}

func TestCallbackWithoutLoginCookie(t *testing.T) {
	t.Parallel()

	e := setup(t)

	rec := e.callback(t, url.Values{"code": {"abc"}, "state": {"xyz"}}, httptest.NewRecorder())
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "This login expired or started in another browser. Sign in again.", detailOf(t, rec))
}

// auditRow is one audit log entry, as the tests compare it.
type auditRow struct {
	Action  string
	Details string
}

// auditRows returns the audit log of the user the fake provider signs in,
// oldest first.
func auditRows(t *testing.T, e env) []auditRow {
	t.Helper()

	rows, err := e.pool.Query(t.Context(), `
		SELECT a.action, a.details::text FROM audit_log a
		JOIN users u ON u.id::text = a.actor_id
		WHERE u.oidc_subject = 'user-1' ORDER BY a.id`)
	require.NoError(t, err)

	var got []auditRow

	for rows.Next() {
		var r auditRow

		require.NoError(t, rows.Scan(&r.Action, &r.Details))

		got = append(got, r)
	}

	require.NoError(t, rows.Err())

	return got
}

// detailOf returns the detail of the problem details rec answered with.
func detailOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	var body struct {
		Detail string `json:"detail"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body.Detail
}

// requestWith returns a request to the API with the session cookie c and,
// when csrf is set, the CSRF header.
func requestWith(t *testing.T, method string, c *http.Cookie, csrf string) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/me", nil)
	if c != nil {
		req.AddCookie(c)
	}

	if csrf != "" {
		req.Header.Set(auth.CSRFHeader, csrf)
	}

	return req
}

func roleName(r roles.Role) string {
	return map[roles.Role]string{roles.Viewer: "viewer", roles.Responder: "responder", roles.Approver: "approver", roles.Admin: "admin"}[r]
}

func ptr(s string) *string {
	return &s
}
