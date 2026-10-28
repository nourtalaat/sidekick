package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// errUnknownRole means a stored user has a role this version does not
// know.
var errUnknownRole = errors.New("the user's stored role is not a known role")

// Authenticate returns the caller of r: the API token in its
// Authorization header when it has one, else the signed-in user of its
// session cookie. It returns an error wrapping ErrUnauthenticated when r
// has no token or session that is valid now, and ErrCSRF when r changes
// something with a session but without the session's CSRF token in the
// CSRFHeader header. A token needs no CSRF token, since browsers never
// send one on their own.
func (s *Service) Authenticate(r *http.Request) (Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		p, err := s.tokenPrincipal(r, h)
		if err != nil {
			return Principal{}, fmt.Errorf("authenticate: %w", err)
		}

		return p, nil
	}

	p, _, err := s.session(r)
	if err != nil {
		return Principal{}, fmt.Errorf("authenticate: %w", err)
	}

	return p, nil
}

// Logout ends the session of r and clears its cookie. It needs the
// session's CSRF token, so another site cannot sign a user out. A request
// without a valid session only has its cookie cleared.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	_, hash, err := s.session(r)

	switch {
	case errors.Is(err, ErrCSRF):
		problem.Write(w, r, http.StatusForbidden, "Send the session's CSRF token in the "+CSRFHeader+" header.")

		return
	case errors.Is(err, ErrUnauthenticated):
	case err != nil:
		s.log.ErrorContext(ctx, "could not read a session to sign out", "error", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.InternalDetail)

		return
	default:
		if err := db.New(s.pool).DeleteSession(ctx, hash); err != nil {
			s.log.ErrorContext(ctx, "could not delete a session", "error", err)
			problem.Write(w, r, http.StatusInternalServerError, problem.InternalDetail)

			return
		}
	}

	clearCookie(w, sessionCookie)
	w.WriteHeader(http.StatusNoContent)
}

// session returns the caller of r and the hash of its session ID.
func (s *Service) session(r *http.Request) (Principal, []byte, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Principal{}, nil, fmt.Errorf("no session cookie: %w", ErrUnauthenticated)
	}

	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != tokenBytes {
		return Principal{}, nil, fmt.Errorf("a session cookie that is not a session ID: %w", ErrUnauthenticated)
	}

	hash := hashSessionID(raw)

	row, err := db.New(s.pool).GetSession(r.Context(), db.GetSessionParams{IDHash: hash, Now: s.now()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, nil, fmt.Errorf("no session, or an expired one: %w", ErrUnauthenticated)
	}

	if err != nil {
		return Principal{}, nil, fmt.Errorf("read the session: %w", err)
	}

	if changes(r.Method) && subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(row.CsrfToken)) != 1 {
		return Principal{}, nil, ErrCSRF
	}

	role := config.RoleName(row.Role).Role()
	if role == 0 {
		return Principal{}, nil, fmt.Errorf("user %s: %w", row.UserID, errUnknownRole)
	}

	p := Principal{Kind: KindUser, ID: row.UserID, Name: row.Name, Role: role, CSRFToken: row.CsrfToken}
	if row.Email != nil {
		p.Email = *row.Email
	}

	return p, hash, nil
}

// changes reports whether a request with method can change something, so
// needs the CSRF token.
func changes(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// newSessionID returns a new random session ID, as the cookie value and
// as raw bytes.
func newSessionID() (cookie string, raw []byte) {
	raw = make([]byte, tokenBytes)
	_, _ = rand.Read(raw)

	return base64.RawURLEncoding.EncodeToString(raw), raw
}

// hashSessionID returns the hash of a session ID, which is all the
// database keeps of it.
func hashSessionID(raw []byte) []byte {
	sum := sha256.Sum256(raw)

	return sum[:]
}

// clearCookie tells the browser to drop the cookie name.
func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}
