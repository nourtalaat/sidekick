//go:generate mockgen -source authenticator.go -destination mocks_authenticator.go -package api -write_package_comment=false
package api

import (
	"net/http"

	"github.com/nourtalaat/sidekick/internal/auth"
)

// Authenticator finds the caller of an API request.
type Authenticator interface {
	// Authenticate returns the caller of r from its session cookie or API
	// token. The error wraps auth.ErrUnauthenticated when r has neither or
	// they are not valid, and auth.ErrCSRF when a request that changes
	// something lacks the session's CSRF token.
	Authenticate(r *http.Request) (auth.Principal, error)
}
