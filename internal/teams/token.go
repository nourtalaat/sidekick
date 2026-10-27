package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Scopes the bot asks Microsoft Entra ID for.
const (
	// BotFrameworkScope is for the Bot Framework Connector, which posts
	// and edits messages.
	BotFrameworkScope = "https://api.botframework.com/.default"
	// GraphScope is for Microsoft Graph, which reads channel messages.
	GraphScope = "https://graph.microsoft.com/.default"
)

// DefaultLoginURL is the Microsoft Entra ID authority host of the public
// cloud.
const DefaultLoginURL = "https://login.microsoftonline.com/"

// FederatedTokenFileEnv is the variable the AKS workload identity webhook
// sets to the path of the service account token it projects into the pod.
const FederatedTokenFileEnv = "AZURE_FEDERATED_TOKEN_FILE"

// refreshBefore is how long before its expiry a token is replaced, so no
// call starts with a token that runs out on the way.
const refreshBefore = 5 * time.Minute

// maxTokenResponseBytes is the largest token response read: 64 KiB.
const maxTokenResponseBytes = 64 << 10

// The form values of a client credentials request.
const (
	grantClientCredentials = "client_credentials"
	assertionTypeJWTBearer = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
)

var (
	errNoCredentials       = errors.New("set a client secret or workload identity")
	errTwoCredentials      = errors.New("set a client secret or workload identity, not both")
	errNoAppID             = errors.New("the app ID is required")
	errNoTenantID          = errors.New("the tenant ID is required")
	errNoScope             = errors.New("the scope is required")
	errNoSecrets           = errors.New("no secrets resolver to read the client secret with")
	errNoTokenFile         = errors.New("workload identity needs " + FederatedTokenFileEnv + `; is the pod labeled azure.workload.identity/use: "true"?`)
	errEmptyFederatedToken = errors.New("the federated token file is empty")
	errNoAccessToken       = errors.New("Microsoft Entra ID returned no access token or no expiry")
	errTokenTooLarge       = errors.New("the token response is larger than 64 KiB")
)

// Credentials say who the bot is and how it proves it.
type Credentials struct {
	// AppID is the client ID of the bot's app registration or managed
	// identity.
	AppID string
	// TenantID is the Microsoft Entra tenant tokens are asked from.
	TenantID string
	// ClientSecret holds a client secret of the app registration. Set it
	// or WorkloadIdentity.
	ClientSecret registry.SecretRef
	// WorkloadIdentity proves the bot's identity with the federated token
	// of AKS workload identity instead of a secret.
	WorkloadIdentity bool
}

// TokenOptions set up a Tokens. Credentials and Scope are required;
// Secrets is required with a client secret.
type TokenOptions struct {
	Credentials Credentials
	// Scope is what the tokens are for, such as BotFrameworkScope.
	Scope string
	// Secrets reads the client secret on every token request, so a
	// rotated secret is used without a restart.
	Secrets registry.Secrets
	// HTTP sends the token requests. Nil means httpclient.New().
	HTTP *http.Client
	// LoginURL is the authority host, ending in "/". Empty means
	// DefaultLoginURL. It must be https, except on a loopback host.
	LoginURL string
	// FederatedTokenFile is the path of the federated token. Empty means
	// the value of FederatedTokenFileEnv.
	FederatedTokenFile string
}

// Tokens gets and caches the bot's access tokens for one scope. It is
// safe for concurrent use: when several calls find the token expired, one
// request runs and the others wait for its token.
type Tokens struct {
	creds     Credentials
	scope     string
	secrets   registry.Secrets
	client    *http.Client
	tokenURL  string
	tokenFile string
	now       func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// tokenResponse is the answer of the token endpoint, a success or an
// error.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       string `json:"error"`
	ErrorCodes  []int  `json:"error_codes"`
}

// Token returns a token that is valid for at least refreshBefore, asking
// for a new one when the cached one is missing or about to expire.
func (t *Tokens) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && t.now().Before(t.expires.Add(-refreshBefore)) {
		return t.token, nil
	}

	token, expires, err := t.request(ctx)
	if err != nil {
		return "", fmt.Errorf("get a token for %s: %w", t.scope, err)
	}

	t.token, t.expires = token, expires

	return token, nil
}

// Invalidate drops token from the cache, so the next call to Token asks
// for a new one, for example after a 401 answer. It does nothing when the
// cache holds another token, because another call has replaced it.
func (t *Tokens) Invalidate(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token == token {
		t.token = ""
	}
}

// request asks the token endpoint for a token with the client credentials
// grant.
func (t *Tokens) request(ctx context.Context) (string, time.Time, error) {
	form, err := t.form(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build the token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("make the token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	sent := t.now()

	resp, err := t.client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("send the token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body tokenResponse

	data, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxTokenResponseBytes, errTokenTooLarge))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read the token response: %w", err)
	}

	if err := json.Unmarshal(data, &body); err != nil {
		return "", time.Time{}, fmt.Errorf("decode the token response (HTTP %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, refusal(resp.StatusCode, body)
	}

	if body.AccessToken == "" || body.ExpiresIn <= 0 {
		return "", time.Time{}, errNoAccessToken
	}

	return body.AccessToken, sent.Add(time.Duration(body.ExpiresIn) * time.Second), nil
}

// form returns the body of a token request, with the client secret or the
// federated token, both read anew so rotated ones are used.
func (t *Tokens) form(ctx context.Context) (url.Values, error) {
	form := url.Values{
		"grant_type": {grantClientCredentials},
		"client_id":  {t.creds.AppID},
		"scope":      {t.scope},
	}

	if !t.creds.WorkloadIdentity {
		secret, err := secrets.ResolveText(ctx, t.secrets, t.creds.ClientSecret)
		if err != nil {
			return nil, fmt.Errorf("read the client secret: %w", err)
		}

		form.Set("client_secret", secret)

		return form, nil
	}

	data, err := os.ReadFile(t.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read the federated token file: %w", err)
	}

	assertion := strings.TrimSpace(string(data))
	if assertion == "" {
		return nil, errEmptyFederatedToken
	}

	form.Set("client_assertion_type", assertionTypeJWTBearer)
	form.Set("client_assertion", assertion)

	return form, nil
}

// NewTokens checks opts and returns a Tokens. It asks for no token yet.
func NewTokens(opts TokenOptions) (*Tokens, error) {
	c := opts.Credentials

	switch {
	case c.AppID == "":
		return nil, errNoAppID
	case c.TenantID == "":
		return nil, errNoTenantID
	case opts.Scope == "":
		return nil, errNoScope
	case c.WorkloadIdentity && c.ClientSecret != (registry.SecretRef{}):
		return nil, errTwoCredentials
	case !c.WorkloadIdentity && c.ClientSecret == (registry.SecretRef{}):
		return nil, errNoCredentials
	case !c.WorkloadIdentity && opts.Secrets == nil:
		return nil, errNoSecrets
	}

	loginURL := opts.LoginURL
	if loginURL == "" {
		loginURL = DefaultLoginURL
	}

	if err := CheckBaseURL(loginURL); err != nil {
		return nil, fmt.Errorf("login URL: %w", err)
	}

	t := &Tokens{
		creds:    c,
		scope:    opts.Scope,
		secrets:  opts.Secrets,
		client:   opts.HTTP,
		tokenURL: loginURL + url.PathEscape(c.TenantID) + "/oauth2/v2.0/token",
		now:      time.Now,
	}

	if t.client == nil {
		t.client = httpclient.New()
	}

	if c.WorkloadIdentity {
		t.tokenFile = opts.FederatedTokenFile
		if t.tokenFile == "" {
			t.tokenFile = os.Getenv(FederatedTokenFileEnv)
		}

		if t.tokenFile == "" {
			return nil, errNoTokenFile
		}
	}

	return t, nil
}

// CheckBaseURL reports whether raw is an absolute URL ending in "/" that
// is https, or http on a loopback host, so credentials never cross the
// network unencrypted.
func CheckBaseURL(raw string) error {
	if !strings.HasSuffix(raw, "/") {
		return fmt.Errorf(`%q does not end in "/"`, raw)
	}

	return checkSecureURL(raw)
}

// refusal describes an error answer of the token endpoint. It names the
// OAuth error and the first AADSTS code, never the request.
func refusal(status int, body tokenResponse) error {
	what := body.Error
	if what == "" {
		what = "no error code"
	}

	if len(body.ErrorCodes) > 0 {
		return fmt.Errorf("Microsoft Entra ID refused the token request (HTTP %d): %s (AADSTS%d)", status, what, body.ErrorCodes[0])
	}

	return fmt.Errorf("Microsoft Entra ID refused the token request (HTTP %d): %s", status, what)
}

// checkSecureURL reports whether raw is an absolute https URL, or an http
// one on a loopback host.
func checkSecureURL(raw string) error {
	u, err := url.Parse(raw)

	switch {
	case err != nil:
		return fmt.Errorf("parse %q: %w", raw, err)
	case u.Host == "":
		return fmt.Errorf("%q is not an absolute URL", raw)
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && httpclient.IsLoopbackHost(u.Hostname()):
		return nil
	default:
		return fmt.Errorf("%q must use https, or http only on a loopback host", raw)
	}
}
