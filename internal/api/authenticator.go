//go:generate mockgen -source authenticator.go -destination mocks_authenticator.go -package api -write_package_comment=false
package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Authenticator finds the caller of an API request.
type Authenticator interface {
	// Authenticate returns the caller of r from its session cookie or API
	// token. The error wraps auth.ErrUnauthenticated when r has neither or
	// they are not valid, and auth.ErrCSRF when a request that changes
	// something lacks the session's CSRF token.
	Authenticate(r *http.Request) (auth.Principal, error)
}

// TokenManager creates, lists and revokes API tokens. *auth.Service is
// one.
type TokenManager interface {
	CreateToken(ctx context.Context, by auth.Principal, nt auth.NewToken) (string, db.ApiToken, error)
	ListTokens(ctx context.Context) ([]db.ApiToken, error)
	RevokeToken(ctx context.Context, by auth.Principal, id uuid.UUID) (db.ApiToken, error)
}
