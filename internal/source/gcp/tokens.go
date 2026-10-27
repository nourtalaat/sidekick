package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// scope is the OAuth scope of every token. The GKE and Service Health APIs
// take no narrower scope, so the IAM roles of the identity decide what the
// source can read.
const scope = "https://www.googleapis.com/auth/cloud-platform"

// secretTimeout bounds the read of the credential file from its secret.
const secretTimeout = 10 * time.Second

// allowedTypes are the credential file types credentialsSecret may hold.
var allowedTypes = []google.CredentialsType{google.ExternalAccount, google.ServiceAccount}

// errNotJSON is returned for a credential file that is not JSON.
var errNotJSON = errors.New("the file is not JSON")

// defaultTokens finds Application Default Credentials on first use and
// keeps them. On GKE they come from the metadata server, which Workload
// Identity answers for the pod's service account.
type defaultTokens struct {
	ctx context.Context

	mu     sync.Mutex
	source oauth2.TokenSource
}

// secretTokens makes each new token from the credential file in a secret,
// read again each time, so a rotated file needs no restart.
type secretTokens struct {
	ctx     context.Context
	ref     registry.SecretRef
	secrets registry.Secrets
}

// Token returns a token from the default credentials, finding them first
// when this is the first call or the last search failed.
func (t *defaultTokens) Token() (*oauth2.Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.source == nil {
		creds, err := google.FindDefaultCredentials(t.ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("find Application Default Credentials: %w", err)
		}

		t.source = creds.TokenSource
	}

	token, err := t.source.Token()
	if err != nil {
		return nil, fmt.Errorf("get a token from the default credentials: %w", err)
	}

	return token, nil
}

// Token reads the credential file, checks its type and makes a token.
func (t *secretTokens) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(t.ctx, secretTimeout)
	defer cancel()

	data, err := t.secrets.Resolve(ctx, t.ref)
	if err != nil {
		return nil, fmt.Errorf("resolve secret %s/%s: %w", t.ref.Name, t.ref.Key, err)
	}

	kind, err := credentialsType(data)
	if err != nil {
		return nil, fmt.Errorf("read the credential file in secret %s/%s: %w", t.ref.Name, t.ref.Key, err)
	}

	creds, err := google.CredentialsFromJSONWithType(t.ctx, data, kind, scope)
	if err != nil {
		return nil, fmt.Errorf("load the credential file in secret %s/%s: %w", t.ref.Name, t.ref.Key, err)
	}

	token, err := creds.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("get a token from the credential file: %w", err)
	}

	return token, nil
}

// newTokens returns the token source of a source: one from the credential
// file in ref when it is set, otherwise from Application Default
// Credentials. Token exchanges go through client. Tokens are kept until
// shortly before they expire.
func newTokens(ref *registry.SecretRef, secrets registry.Secrets, client *http.Client) oauth2.TokenSource {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)

	if ref == nil {
		return &defaultTokens{ctx: ctx}
	}

	return oauth2.ReuseTokenSource(nil, &secretTokens{ctx: ctx, ref: *ref, secrets: secrets})
}

// credentialsType returns the type a credential file names, when it is one
// of allowedTypes. It never returns the file's content in an error.
func credentialsType(data []byte) (google.CredentialsType, error) {
	var file struct {
		Type google.CredentialsType `json:"type"`
	}

	// The decode error is left out: it can quote part of the file.
	if json.Unmarshal(data, &file) != nil {
		return "", errNotJSON
	}

	if slices.Contains(allowedTypes, file.Type) {
		return file.Type, nil
	}

	return "", fmt.Errorf("the file has type %q; use %q (workload identity federation) or %q",
		source.CutText(string(file.Type), maxNameLen), google.ExternalAccount, google.ServiceAccount)
}
