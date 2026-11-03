// Package kubeaccess gives an action the access of one service account in
// the cluster Sidekick runs in, without Sidekick holding that account's
// credentials. Sidekick's own account may only ask the API server for
// short-lived tokens of the actions' account (TokenRequest), and each
// operation of an action asks for a new one and acts with it.
//
// What it relies on, as the public source of Kubernetes (tag v1.37.1) shows
// it: a token for a service account comes from the TokenRequest API, which
// gives tokens that live at least 10 minutes
// (pkg/apis/authentication/validation/validation.go), and RBAC can limit the
// request to named accounts because the account is in the URL
// (staging/src/k8s.io/apiserver/pkg/endpoints/request/requestinfo.go).
package kubeaccess

import (
	"context"
	"fmt"
	"os"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

const (
	// namespaceFile holds the namespace of the pod, which is the namespace
	// of the service account the action asks tokens for.
	namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	// tokenSeconds is how long a token lives: 10 minutes, the shortest the
	// API server allows (pkg/apis/authentication/validation, MinTokenAgeSec).
	// One token serves the calls of one operation.
	tokenSeconds int64 = 10 * 60
	// Client-side rate limits, more than client-go's defaults of 5 and 10.
	clientQPS   = 20
	clientBurst = 40
	// redacted replaces a token in an error text.
	redacted = "[redacted]"
)

// Cluster reaches the Kubernetes API of the cluster Sidekick runs in. It
// holds Sidekick's own credentials, which can only ask for tokens of one
// service account, the actions' account.
type Cluster struct {
	// own is the REST config of Sidekick's service account.
	own *rest.Config
	// account and namespace name the service account the tokens are for.
	account   string
	namespace string
}

// Access is the access of one operation, with the rights of the actions'
// service account.
type Access struct {
	// Config is a REST config that sends the token of the actions' account.
	// It asks for JSON, which client-go's typed clients do not by default.
	Config *rest.Config
	// Secrets are the credentials in use. Pass them to Redact for every
	// error that may be shown to anyone.
	Secrets []string
}

// InCluster returns the cluster Sidekick runs in, for the service account
// named account in Sidekick's namespace. It makes no call to the API server,
// and fails outside a cluster.
func InCluster(account string) (*Cluster, error) {
	own, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("use the pod's service account to ask for tokens: %w", err)
	}

	namespace, err := os.ReadFile(namespaceFile)
	if err != nil {
		return nil, fmt.Errorf("read the namespace of the pod: %w", err)
	}

	return NewCluster(own, account, strings.TrimSpace(string(namespace))), nil
}

// NewCluster returns a cluster from the REST config of Sidekick's own
// account and the service account, in namespace, that tokens are asked for.
// InCluster is the way to get one in production; this is for a caller that
// has the config already, such as a test with a fake API server.
func NewCluster(own *rest.Config, account, namespace string) *Cluster {
	return &Cluster{own: own, account: account, namespace: namespace}
}

// Open asks the API server for a token of the actions' service account and
// returns the access that uses it. Errors have the credentials cut out.
func (c *Cluster) Open(ctx context.Context) (*Access, error) {
	secrets := c.ownSecrets()

	own, err := kubernetes.NewForConfig(configFor(c.own))
	if err != nil {
		return nil, Redact(fmt.Errorf("build the client for token requests: %w", err), secrets)
	}

	expires := tokenSeconds
	request := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &expires}}

	granted, err := own.CoreV1().ServiceAccounts(c.namespace).CreateToken(ctx, c.account, request, metav1.CreateOptions{})
	if err != nil {
		return nil, Redact(fmt.Errorf("ask for a token of service account %s/%s: %w", c.namespace, c.account, err), secrets)
	}

	if granted.Status.Token == "" {
		return nil, fmt.Errorf("the API server gave no token for service account %s/%s", c.namespace, c.account)
	}

	secrets = append(secrets, granted.Status.Token)

	acting := rest.AnonymousClientConfig(c.own)
	acting.BearerToken = granted.Status.Token

	return &Access{Config: configFor(acting), Secrets: secrets}, nil
}

// Redact returns err with every secret cut out of its text. It unwraps to
// err, so callers can still tell what kind of error it is. A client-go
// error does not carry the request headers, but a message from the server
// or a proxy could echo one.
func Redact(err error, secrets []string) error {
	text := err.Error()
	cleaned := text

	for _, secret := range secrets {
		if secret != "" {
			cleaned = strings.ReplaceAll(cleaned, secret, redacted)
		}
	}

	return &redactedError{text: cleaned, err: err}
}

// redactedError is an error whose text has the tokens cut out. It still
// unwraps to the original, so callers can tell a conflict from a refusal.
type redactedError struct {
	text string
	err  error
}

// Error implements error.
func (e *redactedError) Error() string {
	return e.text
}

// Unwrap returns the error that was redacted.
func (e *redactedError) Unwrap() error {
	return e.err
}

// ownSecrets returns the credentials of Sidekick's own account.
func (c *Cluster) ownSecrets() []string {
	if c.own.BearerToken == "" {
		return nil
	}

	return []string{c.own.BearerToken}
}

// configFor returns a copy of rc with Sidekick's user agent and rate limits.
// It asks for JSON, because client-go's typed clients send protobuf by
// default, which a test server cannot read, and a call is too rare for the
// size of the answer to matter.
func configFor(rc *rest.Config) *rest.Config {
	rc = rest.CopyConfig(rc)
	rc.ContentType = runtime.ContentTypeJSON
	rc.AcceptContentTypes = runtime.ContentTypeJSON
	rc.UserAgent = "sidekick/" + buildinfo.Version
	rc.QPS = clientQPS
	rc.Burst = clientBurst

	return rc
}
