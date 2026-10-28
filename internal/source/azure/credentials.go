package azure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// refreshMargin is how long before it expires a token is replaced.
const refreshMargin = 5 * time.Minute

// secretTimeout bounds the read of the client secret from its secret.
const secretTimeout = 10 * time.Second

// errEmptySecret is returned when the client secret's secret holds
// nothing.
var errEmptySecret = errors.New("the client secret is empty")

// tokens keeps one token per scope until shortly before it expires.
type tokens struct {
	cred azcore.TokenCredential
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]azcore.AccessToken
}

// workloadCredential sets up AKS Workload Identity on first use, from the
// variables and token file the webhook gives the pod, so a pod without
// them fails its first call rather than its start.
type workloadCredential struct {
	options azidentity.WorkloadIdentityCredentialOptions

	mu   sync.Mutex
	cred *azidentity.WorkloadIdentityCredential
}

// secretCredential makes each new token with the client secret in a
// secret, read again each time, so a rotated secret needs no restart.
type secretCredential struct {
	tenantID string
	clientID string
	ref      registry.SecretRef
	secrets  registry.Secrets
	options  azidentity.ClientSecretCredentialOptions
}

// token returns a token for scope, making a new one when there is none or
// it expires within refreshMargin.
func (t *tokens) token(ctx context.Context, scope string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if cached, ok := t.cache[scope]; ok && t.now().Before(cached.ExpiresOn.Add(-refreshMargin)) {
		return cached.Token, nil
	}

	token, err := t.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
	if err != nil {
		return "", fmt.Errorf("get a token for %s: %w", scope, err)
	}

	t.cache[scope] = token

	return token.Token, nil
}

// GetToken implements azcore.TokenCredential.
func (w *workloadCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	cred, err := w.credential()
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("get the workload identity credential: %w", err)
	}

	token, err := cred.GetToken(ctx, opts)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("get a workload identity token: %w", err)
	}

	return token, nil
}

// credential returns the workload identity credential, setting it up when
// this is the first call or the last attempt failed.
func (w *workloadCredential) credential() (*azidentity.WorkloadIdentityCredential, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cred == nil {
		cred, err := azidentity.NewWorkloadIdentityCredential(&w.options)
		if err != nil {
			return nil, fmt.Errorf("set up AKS Workload Identity: %w", err)
		}

		w.cred = cred
	}

	return w.cred, nil
}

// GetToken implements azcore.TokenCredential.
func (s *secretCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	readCtx, cancel := context.WithTimeout(ctx, secretTimeout)
	defer cancel()

	raw, err := s.secrets.Resolve(readCtx, s.ref)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("resolve secret %s/%s: %w", s.ref.Name, s.ref.Key, err)
	}

	secret := string(bytes.TrimSpace(raw))
	if secret == "" {
		return azcore.AccessToken{}, errEmptySecret
	}

	cred, err := azidentity.NewClientSecretCredential(s.tenantID, s.clientID, secret, &s.options)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("set up the client secret credential: %w", err)
	}

	token, err := cred.GetToken(ctx, opts)
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("get a token with the client secret: %w", err)
	}

	return token, nil
}

// newTokens returns a token cache for cred.
func newTokens(cred azcore.TokenCredential) *tokens {
	return &tokens{cred: cred, now: time.Now, cache: map[string]azcore.AccessToken{}}
}

// newCredential returns the credential of a config: a client secret when
// clientSecret is set, AKS Workload Identity otherwise. Token requests to
// Microsoft Entra go through client.
func newCredential(cfg Config, secrets registry.Secrets, client *http.Client) azcore.TokenCredential {
	options := azcore.ClientOptions{Transport: client}

	if cfg.ClientSecret != nil {
		return &secretCredential{
			tenantID: cfg.TenantID,
			clientID: cfg.ClientID,
			ref:      *cfg.ClientSecret,
			secrets:  secrets,
			options:  azidentity.ClientSecretCredentialOptions{ClientOptions: options},
		}
	}

	return &workloadCredential{options: azidentity.WorkloadIdentityCredentialOptions{
		ClientOptions: options,
		TenantID:      cfg.TenantID,
		ClientID:      cfg.ClientID,
	}}
}
