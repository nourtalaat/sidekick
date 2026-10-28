package auth

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// The paths the login endpoints are served at.
const (
	LoginPath    = "/auth/login"
	CallbackPath = "/auth/callback"
	LogoutPath   = "/auth/logout"
)

// SessionLifetime is how long a session lasts after its login. It is not
// extended by use.
const SessionLifetime = 12 * time.Hour

// loginLifetime is how long a user has at the provider between
// /auth/login and /auth/callback.
const loginLifetime = 10 * time.Minute

// The cookies. The __Host- prefix makes browsers accept them only when
// they are Secure, for the whole site and without a Domain, so no other
// host can set them.
const (
	loginCookie   = "__Host-sidekick_login"
	sessionCookie = "__Host-sidekick_session"
)

// CSRFHeader is the header that requests changing something send the
// session's CSRF token in.
const CSRFHeader = "X-CSRF-Token"

// tokenBytes is the number of random bytes in session IDs, CSRF tokens,
// OAuth states and nonces.
const tokenBytes = 32

// errMissingOption is returned by New when a required option is not set.
var errMissingOption = errors.New("missing option")

// Options set up a Service. Every field but HTTP, Logger and Now is
// required.
type Options struct {
	// OIDC is the provider, the client and the group to role map.
	OIDC *config.OIDC
	// PublicURL is the address users reach Sidekick at. The provider
	// sends them back to PublicURL + CallbackPath.
	PublicURL string
	// Secrets resolves the client secret on every login, so a rotated
	// secret is used without a restart.
	Secrets registry.Secrets
	// Pool holds the users and sessions tables.
	Pool *pgxpool.Pool
	// Audit records new users and changed roles.
	Audit *audit.Writer
	// HTTP calls the provider. Nil means httpclient.New().
	HTTP *http.Client
	// Logger gets one line per login and the reasons logins fail. Nil
	// logs nothing.
	Logger *slog.Logger
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// Service runs the OIDC login and the sessions it starts, and finds the
// caller of API requests. It is safe for concurrent use.
type Service struct {
	cfg         *config.OIDC
	redirectURL string
	scopes      []string
	secrets     registry.Secrets
	pool        *pgxpool.Pool
	audit       *audit.Writer
	http        *http.Client
	log         *slog.Logger
	now         func() time.Time

	// mu guards provider, which is discovered at the first login so that
	// Sidekick starts while the provider is down.
	mu       sync.Mutex
	provider *oidc.Provider
}

// New returns a Service. It does not call the provider.
func New(opts Options) (*Service, error) {
	switch {
	case opts.OIDC == nil:
		return nil, fmt.Errorf("%w: OIDC", errMissingOption)
	case opts.PublicURL == "":
		return nil, fmt.Errorf("%w: PublicURL", errMissingOption)
	case opts.Secrets == nil:
		return nil, fmt.Errorf("%w: Secrets", errMissingOption)
	case opts.Pool == nil:
		return nil, fmt.Errorf("%w: Pool", errMissingOption)
	case opts.Audit == nil:
		return nil, fmt.Errorf("%w: Audit", errMissingOption)
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	scopes := []string{oidc.ScopeOpenID, "email", "profile"}
	for _, s := range opts.OIDC.Scopes {
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Service{
		cfg:         opts.OIDC,
		redirectURL: strings.TrimSuffix(opts.PublicURL, "/") + CallbackPath,
		scopes:      scopes,
		secrets:     opts.Secrets,
		pool:        opts.Pool,
		audit:       opts.Audit,
		http:        cmp.Or(opts.HTTP, httpclient.New()),
		log:         log,
		now:         now,
	}, nil
}

// Register adds the login endpoints to mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+LoginPath, s.Login)
	mux.HandleFunc("GET "+CallbackPath, s.Callback)
	mux.HandleFunc("POST "+LogoutPath, s.Logout)
}

// oidcProvider returns the provider, reading its discovery document the
// first time. A failed discovery is tried again at the next login.
func (s *Service) oidcProvider(ctx context.Context) (*oidc.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.provider != nil {
		return s.provider, nil
	}

	// The provider fetches its signing keys later with this context, so
	// it must outlive the request that discovers it.
	p, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), s.http), s.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover the OIDC provider %s: %w", s.cfg.Issuer, err)
	}

	s.provider = p

	return p, nil
}

// oauth returns the OAuth 2 client of the provider p.
func (s *Service) oauth(p *oidc.Provider, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.cfg.ClientID,
		ClientSecret: clientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  s.redirectURL,
		Scopes:       s.scopes,
	}
}

// clientSecret reads the client secret.
func (s *Service) clientSecret(ctx context.Context) (string, error) {
	secret, err := secrets.ResolveText(ctx, s.secrets, s.cfg.ClientSecret)
	if err != nil {
		return "", fmt.Errorf("resolve oidc.clientSecret: %w", err)
	}

	return secret, nil
}
