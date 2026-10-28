//go:build integration

package auth_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The admin and the tokens of fixture/tokens.sql.
var (
	adID          = uuid.MustParse("01920000-0000-7000-8000-000000000040")
	activeTokenID = uuid.MustParse("01920000-0000-7000-8000-000000000041")
	admin         = auth.Principal{Kind: auth.KindUser, ID: adID, Name: "Ad", Role: roles.Admin}
)

// setupTokens returns an env with fixture/tokens.sql loaded too.
func setupTokens(t *testing.T) env {
	t.Helper()

	e := setup(t)
	storetest.LoadFixture(t, e.pool, "fixture/tokens.sql")

	return e
}

func TestAuthenticateToken(t *testing.T) {
	t.Parallel()

	e := setupTokens(t)

	tests := []struct {
		name    string
		method  string
		header  string
		cookie  string
		wantErr error
	}{
		{name: "a live token reads", method: http.MethodGet, header: "Bearer skt_active"},
		{name: "a live token changes something without a CSRF token", method: http.MethodPost, header: "bearer skt_active"},
		{name: "a token wins over a session cookie", method: http.MethodGet, header: "Bearer skt_active", cookie: liveSession},
		{name: "a revoked token", method: http.MethodGet, header: "Bearer skt_revoked", wantErr: auth.ErrUnauthenticated},
		{name: "an expired token", method: http.MethodGet, header: "Bearer skt_expired", wantErr: auth.ErrUnauthenticated},
		{name: "an unknown token", method: http.MethodGet, header: "Bearer skt_unknown", wantErr: auth.ErrUnauthenticated},
		{name: "a token without the prefix", method: http.MethodGet, header: "Bearer active", wantErr: auth.ErrUnauthenticated},
		{name: "another scheme", method: http.MethodGet, header: "Basic c2t0X2FjdGl2ZQ==", wantErr: auth.ErrUnauthenticated},
		{name: "a bad token does not fall back to the cookie", method: http.MethodGet, header: "Bearer skt_unknown", cookie: liveSession, wantErr: auth.ErrUnauthenticated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var c *http.Cookie
			if tt.cookie != "" {
				c = newSessionCookie(tt.cookie)
			}

			req := requestWith(t, tt.method, c, "")
			req.Header.Set("Authorization", tt.header)

			p, err := e.svc.Authenticate(req)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, auth.Principal{Kind: auth.KindToken, ID: activeTokenID, Name: "ci bench", Role: roles.Approver}, p)
		})
	}
}

func TestAuthenticateTokenRecordsUse(t *testing.T) {
	t.Parallel()

	e := setupTokens(t)

	req := requestWith(t, http.MethodGet, nil, "")
	req.Header.Set("Authorization", "Bearer skt_active")

	_, err := e.svc.Authenticate(req)
	require.NoError(t, err)

	var used *time.Time

	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT last_used_at FROM api_tokens WHERE id = $1", activeTokenID).Scan(&used))
	require.NotNil(t, used)
}

func TestCreateToken(t *testing.T) {
	t.Parallel()

	inMonth := time.Now().Add(30 * 24 * time.Hour)
	responder := auth.Principal{Kind: auth.KindUser, ID: boID, Role: roles.Responder}
	token := auth.Principal{Kind: auth.KindToken, ID: activeTokenID, Role: roles.Admin}

	tests := []struct {
		name    string
		by      auth.Principal
		new     auth.NewToken
		wantErr error
	}{
		{name: "an admin makes a viewer token", by: admin, new: auth.NewToken{Name: " nightly bench ", Role: roles.Viewer, ExpiresAt: inMonth}},
		{name: "a token cannot make tokens", by: token, new: auth.NewToken{Name: "x", Role: roles.Viewer, ExpiresAt: inMonth}, wantErr: auth.ErrTokenByToken},
		{name: "a role above the creator's", by: responder, new: auth.NewToken{Name: "x", Role: roles.Admin, ExpiresAt: inMonth}, wantErr: auth.ErrTokenRole},
		{name: "a role that is not a role", by: admin, new: auth.NewToken{Name: "x", ExpiresAt: inMonth}, wantErr: auth.ErrTokenRole},
		{name: "an expiry in the past", by: admin, new: auth.NewToken{Name: "x", Role: roles.Viewer, ExpiresAt: time.Now().Add(-time.Hour)}, wantErr: auth.ErrTokenExpiry},
		{name: "an expiry too far away", by: admin, new: auth.NewToken{Name: "x", Role: roles.Viewer, ExpiresAt: time.Now().Add(auth.MaxTokenLifetime + time.Hour)}, wantErr: auth.ErrTokenExpiry},
		{name: "an empty name", by: admin, new: auth.NewToken{Name: "  ", Role: roles.Viewer, ExpiresAt: inMonth}, wantErr: auth.ErrTokenName},
		{name: "a name too long", by: admin, new: auth.NewToken{Name: strings.Repeat("n", auth.MaxTokenNameLength+1), Role: roles.Viewer, ExpiresAt: inMonth}, wantErr: auth.ErrTokenName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := setupTokens(t)

			secret, row, err := e.svc.CreateToken(t.Context(), tt.by, tt.new)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.True(t, strings.HasPrefix(secret, auth.TokenPrefix))
			require.Equal(t, "nightly bench", row.Name)
			require.Equal(t, "viewer", row.Role)
			require.Equal(t, adID, row.CreatedBy)
			require.NotContains(t, string(row.TokenHash), secret)

			req := requestWith(t, http.MethodGet, nil, "")
			req.Header.Set("Authorization", "Bearer "+secret)

			p, err := e.svc.Authenticate(req)
			require.NoError(t, err)
			require.Equal(t, auth.Principal{Kind: auth.KindToken, ID: row.ID, Name: "nightly bench", Role: roles.Viewer}, p)

			var action, actor, details string

			require.NoError(t, e.pool.QueryRow(t.Context(),
				"SELECT action, actor_id, details::text FROM audit_log WHERE target_id = $1", row.ID.String()).Scan(&action, &actor, &details))
			require.Equal(t, auth.ActionTokenCreated, action)
			require.Equal(t, adID.String(), actor)
			require.NotContains(t, details, secret)
		})
	}
}

func TestRevokeToken(t *testing.T) {
	t.Parallel()

	e := setupTokens(t)

	row, err := e.svc.RevokeToken(t.Context(), admin, activeTokenID)
	require.NoError(t, err)
	require.NotNil(t, row.RevokedAt)

	_, err = e.svc.RevokeToken(t.Context(), admin, activeTokenID)
	require.ErrorIs(t, err, auth.ErrNoToken)

	_, err = e.svc.RevokeToken(t.Context(), auth.Principal{Kind: auth.KindToken, Role: roles.Admin}, activeTokenID)
	require.ErrorIs(t, err, auth.ErrTokenByToken)

	req := requestWith(t, http.MethodGet, nil, "")
	req.Header.Set("Authorization", "Bearer skt_active")

	_, err = e.svc.Authenticate(req)
	require.ErrorIs(t, err, auth.ErrUnauthenticated)

	var actions int

	require.NoError(t, e.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM audit_log WHERE action = $1 AND target_id = $2", auth.ActionTokenRevoked, activeTokenID.String()).Scan(&actions))
	require.Equal(t, 1, actions)

	tokens, err := e.svc.ListTokens(t.Context())
	require.NoError(t, err)
	require.Len(t, tokens, 3)
}
