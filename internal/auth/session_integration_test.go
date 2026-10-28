//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The sessions of fixture/users.sql, as cookie values.
var (
	liveSession    = sessionValue(0x01)
	expiredSession = sessionValue(0x02)
	unknownSession = sessionValue(0x03)
)

// The CSRF token of the live session in fixture/users.sql.
const liveCSRF = "csrf-live"

var boID = uuid.MustParse("01920000-0000-7000-8000-000000000020")

func TestAuthenticate(t *testing.T) {
	t.Parallel()

	e := setup(t)

	tests := []struct {
		name    string
		method  string
		cookie  string
		csrf    string
		wantErr error
	}{
		{name: "a live session reads", method: http.MethodGet, cookie: liveSession},
		{name: "a live session changes something with its CSRF token", method: http.MethodPost, cookie: liveSession, csrf: liveCSRF},
		{name: "a change without the CSRF token", method: http.MethodPost, cookie: liveSession, wantErr: auth.ErrCSRF},
		{name: "a change with another CSRF token", method: http.MethodDelete, cookie: liveSession, csrf: "csrf-expired", wantErr: auth.ErrCSRF},
		{name: "an expired session", method: http.MethodGet, cookie: expiredSession, wantErr: auth.ErrUnauthenticated},
		{name: "an unknown session", method: http.MethodGet, cookie: unknownSession, wantErr: auth.ErrUnauthenticated},
		{name: "a cookie that is not a session ID", method: http.MethodGet, cookie: "not base64!", wantErr: auth.ErrUnauthenticated},
		{name: "no cookie", method: http.MethodGet, wantErr: auth.ErrUnauthenticated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var c *http.Cookie
			if tt.cookie != "" {
				c = newSessionCookie(tt.cookie)
			}

			p, err := e.svc.Authenticate(requestWith(t, tt.method, c, tt.csrf))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, auth.Principal{
				Kind: auth.KindUser, ID: boID, Name: "Bo", Email: "bo@example.com", Role: roles.Responder, CSRFToken: liveCSRF,
			}, p)
		})
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cookie      string
		csrf        string
		wantStatus  int
		wantCleared bool
		wantLive    bool
	}{
		{name: "with the CSRF token", cookie: liveSession, csrf: liveCSRF, wantStatus: http.StatusNoContent, wantCleared: true},
		{name: "without the CSRF token", cookie: liveSession, wantStatus: http.StatusForbidden, wantLive: true},
		{name: "an expired session", cookie: expiredSession, wantStatus: http.StatusNoContent, wantCleared: true, wantLive: true},
		{name: "no session", wantStatus: http.StatusNoContent, wantCleared: true, wantLive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := setup(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, auth.LogoutPath, nil)
			if tt.cookie != "" {
				req.AddCookie(newSessionCookie(tt.cookie))
			}

			if tt.csrf != "" {
				req.Header.Set(auth.CSRFHeader, tt.csrf)
			}

			rec := httptest.NewRecorder()
			e.mux.ServeHTTP(rec, req)
			require.Equal(t, tt.wantStatus, rec.Code)

			if c := cookie(rec, sessionCookie); tt.wantCleared {
				require.NotNil(t, c)
				require.Negative(t, c.MaxAge)
			} else {
				require.Nil(t, c)
			}

			_, err := e.svc.Authenticate(requestWith(t, http.MethodGet, newSessionCookie(liveSession), ""))
			if tt.wantLive {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, auth.ErrUnauthenticated)
			}
		})
	}
}

// TestQueryPlans runs EXPLAIN ANALYZE on the users, sessions and API
// token queries over 5,000 users with a session each and 500 tokens.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy.sql")

	now := time.Now()
	userID := uuid.MustParse("01920000-0000-7000-8000-000000000100")
	hash := []byte("0123456789abcdef0123456789abcdef")
	tokenID := uuid.MustParse("01920000-0000-7000-9000-000000000100")

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex string
		// seqScan allows a sequential scan, for a query that reads a
		// small table whole.
		seqScan bool
	}{
		{
			name: "ReleaseUserEmail",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ReleaseUserEmail(ctx, db.ReleaseUserEmailParams{Email: "User2500@example.com", OidcIssuer: "https://idp.example.com", OidcSubject: "user-1"})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "users_email",
		},
		{
			name: "LockUserBySubject",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockUserBySubject(ctx, db.LockUserBySubjectParams{OidcIssuer: "https://idp.example.com", OidcSubject: "user-2500"})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "users_subject",
		},
		{
			name: "InsertUser",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertUser(ctx, db.InsertUserParams{
					ID: uuid.New(), OidcIssuer: "https://idp.example.com", OidcSubject: "user-new", Name: "New", Groups: []string{}, Role: "viewer", Now: now,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "users_subject",
		},
		{
			name: "UpdateUserLogin",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpdateUserLogin(ctx, db.UpdateUserLoginParams{ID: userID, Name: "User", Groups: []string{}, Role: "viewer", Now: now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "users_pkey",
		},
		{
			name: "CreateSession",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.CreateSession(ctx, db.CreateSessionParams{IDHash: hash, UserID: userID, CsrfToken: "csrf", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "Insert on sessions",
		},
		{
			name: "GetSession",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetSession(ctx, db.GetSessionParams{IDHash: hash, Now: now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "sessions_pkey",
		},
		{
			name: "DeleteSession",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.DeleteSession(ctx, hash)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "sessions_pkey",
		},
		{
			name: "DeleteExpiredSessions",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DeleteExpiredSessions(ctx, now)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "sessions_expires_at",
		},
		{
			name: "CreateAPIToken",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CreateAPIToken(ctx, db.CreateAPITokenParams{
					ID: uuid.New(), Name: "new", TokenHash: hash, Role: "viewer", CreatedBy: userID, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "Insert on api_tokens",
		},
		{
			name: "GetActiveAPIToken",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetActiveAPIToken(ctx, db.GetActiveAPITokenParams{TokenHash: hash, Now: now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "api_tokens_hash",
		},
		{
			name: "TouchAPIToken",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.TouchAPIToken(ctx, db.TouchAPITokenParams{ID: tokenID, Now: now, LastUsedBefore: now.Add(-time.Minute)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "api_tokens_pkey",
		},
		{
			name: "RevokeAPIToken",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.RevokeAPIToken(ctx, db.RevokeAPITokenParams{ID: tokenID, Now: now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "api_tokens_pkey",
		},
		{
			name: "ListAPITokens",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListAPITokens(ctx)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "api_tokens",
			seqScan:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)
			require.Contains(t, explain.Plan, tt.wantIndex)

			if !tt.seqScan {
				require.NotContains(t, explain.Plan, "Seq Scan")
			}
		})
	}
}

// sessionValue returns the cookie value of a session ID of 32 bytes of b.
func sessionValue(b byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// newSessionCookie returns the session cookie with value, with the
// attributes the server sets on it.
func newSessionCookie(value string) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}
