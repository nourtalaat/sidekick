package httpauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// errEmptySecret is returned when a credential secret holds nothing.
var errEmptySecret = errors.New("the secret is empty")

// Auth adds what every request carries: the extra headers, and a bearer
// token or basic auth read from Secrets.
type Auth struct {
	secrets registry.Secrets
	bearer  *registry.SecretRef
	basic   *BasicAuth
	headers map[string]string
}

// NewAuth returns the Auth for the keys of c. secrets may be nil when c
// reads no secret.
func NewAuth(c Config, secrets registry.Secrets) *Auth {
	return &Auth{secrets: secrets, bearer: c.BearerTokenSecret, basic: c.BasicAuth, headers: c.Headers}
}

// Apply adds the headers and the credentials to req. The secrets are read
// for every request, so a rotated one needs no restart.
func (a *Auth) Apply(ctx context.Context, req *http.Request) error {
	for name, value := range a.headers {
		req.Header.Set(name, value)
	}

	switch {
	case a.bearer != nil:
		raw, err := a.read(ctx, *a.bearer)
		if err != nil {
			return fmt.Errorf("read the bearer token: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(raw))
	case a.basic != nil:
		raw, err := a.read(ctx, a.basic.PasswordSecret)
		if err != nil {
			return fmt.Errorf("read the basic auth password: %w", err)
		}

		req.SetBasicAuth(a.basic.Username, strings.TrimRight(raw, "\r\n"))
	}

	return nil
}

// read returns the text of a secret, which must not be empty.
func (a *Auth) read(ctx context.Context, ref registry.SecretRef) (string, error) {
	raw, err := a.secrets.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s/%s: %w", ref.Name, ref.Key, err)
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return "", fmt.Errorf("secret %s/%s: %w", ref.Name, ref.Key, errEmptySecret)
	}

	return string(raw), nil
}
