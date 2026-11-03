// Package azureauth sets up the Microsoft Entra credential of the plugins
// that call Azure: the Azure source and the Azure actions.
//
// A credential is a client secret, which is read from a secret again for
// each new token so a rotated secret needs no restart, or AKS Workload
// Identity, which is set up on first use from the variables and token file
// the AKS webhook gives the pod. Neither makes a network call when it is
// built.
package azureauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// secretTimeout bounds the read of the client secret from its secret.
const secretTimeout = 10 * time.Second

// errEmptySecret is returned when the client secret's secret holds
// nothing.
var errEmptySecret = errors.New("the client secret is empty")

// guidPattern is the shape of a subscription, tenant, client or workspace
// ID.
var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Identity is the Microsoft Entra identity an Azure plugin calls Azure
// with. A plugin copies its three config keys into it.
type Identity struct {
	// TenantID is the Microsoft Entra tenant. Empty means AZURE_TENANT_ID,
	// which the AKS Workload Identity webhook sets.
	TenantID string
	// ClientID is the client ID of the identity. Empty means
	// AZURE_CLIENT_ID, which the webhook sets. Required with ClientSecret.
	ClientID string
	// ClientSecret holds the client secret of an app registration, for
	// running outside AKS. Nil means AKS Workload Identity.
	ClientSecret *registry.SecretRef
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

// Check returns the problems with the identity's keys, each starting with
// its key: tenantID and clientID must be GUIDs when set, and clientSecret
// needs both.
func (id Identity) Check() []error {
	var errs []error

	for _, f := range []struct{ key, value string }{{"tenantID", id.TenantID}, {"clientID", id.ClientID}} {
		if f.value != "" && !IsGUID(f.value) {
			errs = append(errs, fmt.Errorf("%s: %q is not a GUID", f.key, f.value))
		}
	}

	if id.ClientSecret != nil && (id.TenantID == "" || id.ClientID == "") {
		errs = append(errs, errors.New("clientSecret: needs tenantID and clientID"))
	}

	return errs
}

// NewCredential returns the credential of an identity: a client secret
// when ClientSecret is set, AKS Workload Identity otherwise. It makes no
// network call. Token requests to Microsoft Entra go through client.
func NewCredential(id Identity, secrets registry.Secrets, client *http.Client) azcore.TokenCredential {
	options := azcore.ClientOptions{Transport: client}

	if id.ClientSecret != nil {
		return &secretCredential{
			tenantID: id.TenantID,
			clientID: id.ClientID,
			ref:      *id.ClientSecret,
			secrets:  secrets,
			options:  azidentity.ClientSecretCredentialOptions{ClientOptions: options},
		}
	}

	return &workloadCredential{options: azidentity.WorkloadIdentityCredentialOptions{
		ClientOptions: options,
		TenantID:      id.TenantID,
		ClientID:      id.ClientID,
	}}
}

// IsGUID reports whether s has the shape of a subscription, tenant, client
// or workspace ID.
func IsGUID(s string) bool {
	return guidPattern.MatchString(s)
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
