package azure

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// refreshMargin is how long before it expires a token is replaced.
const refreshMargin = 5 * time.Minute

// tokens keeps one token per scope until shortly before it expires.
type tokens struct {
	cred azcore.TokenCredential
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]azcore.AccessToken
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

// newTokens returns a token cache for cred.
func newTokens(cred azcore.TokenCredential) *tokens {
	return &tokens{cred: cred, now: time.Now, cache: map[string]azcore.AccessToken{}}
}
