// Package auth finds who calls the web UI and the JSON API: a user signed
// in through OIDC, or an API token an admin created.
package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/roles"
)

// Kind says what a Principal is.
type Kind string

// Principal kinds.
const (
	// KindUser is a person signed in to the web UI.
	KindUser Kind = "user"
	// KindToken is an API token.
	KindToken Kind = "token"
)

var (
	// ErrUnauthenticated means a request has no valid session or token.
	ErrUnauthenticated = errors.New("not signed in")
	// ErrCSRF means a request that changes something lacks the session's
	// CSRF token, or has a wrong one.
	ErrCSRF = errors.New("missing or wrong CSRF token")
)

// Principal is the caller of a request.
type Principal struct {
	Kind Kind
	// ID is the user's or the token's ID.
	ID uuid.UUID
	// Name is the user's name or the token's name.
	Name string
	// Email is the user's email, empty for a token or when the identity
	// provider sent none that is verified.
	Email string
	Role  roles.Role
	// CSRFToken is the token a session's requests that change something
	// must send. It is empty for an API token.
	CSRFToken string
}

type principalKey struct{}

// WithPrincipal returns a copy of ctx that carries p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the caller that WithPrincipal put in ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)

	return p, ok
}
