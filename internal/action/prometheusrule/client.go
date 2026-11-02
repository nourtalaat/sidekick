package prometheusrule

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
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
	// One token serves the calls of one Snapshot, Execute or Undo.
	tokenSeconds int64 = 10 * 60
	// fieldManager names Sidekick as the writer in the managed fields.
	fieldManager = "sidekick"
	// Client-side rate limits, more than client-go's defaults of 5 and 10.
	clientQPS   = 20
	clientBurst = 40
	// redacted replaces a token in an error text.
	redacted = "[redacted]"
)

// rulesResource is the PrometheusRule resource of the Prometheus Operator.
var rulesResource = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}

// cluster reaches the Kubernetes API of the cluster Sidekick runs in. It
// holds Sidekick's own credentials, which can only ask for tokens of the
// actions' service account, and gives each operation a session with a token
// of that account.
type cluster struct {
	// own is the REST config of Sidekick's service account.
	own *rest.Config
	// account and namespace name the service account the tokens are for.
	account   string
	namespace string
}

// session is the access of one operation, with the rights of the actions'
// service account.
type session struct {
	rules dynamic.NamespaceableResourceInterface
	// secrets are the tokens in use, which no error text may carry.
	secrets []string
}

// resource is the part of a PrometheusRule the action uses.
type resource struct {
	// version is metadata.resourceVersion. It changes on every write to the
	// resource, spec or status.
	version string
	groups  groupList
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

// inCluster returns the cluster Sidekick runs in, for the service account
// named account in Sidekick's namespace. It fails outside a cluster.
func inCluster(account string) (*cluster, error) {
	own, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("use the pod's service account to ask for tokens: %w", err)
	}

	namespace, err := os.ReadFile(namespaceFile)
	if err != nil {
		return nil, fmt.Errorf("read the namespace of the pod: %w", err)
	}

	return &cluster{own: own, account: account, namespace: strings.TrimSpace(string(namespace))}, nil
}

// open asks for a token of the actions' service account and returns a
// session that uses it.
func (c *cluster) open(ctx context.Context) (*session, error) {
	secrets := c.ownSecrets()

	own, err := kubernetes.NewForConfig(c.configFor(c.own))
	if err != nil {
		return nil, redact(fmt.Errorf("build the client for token requests: %w", err), secrets)
	}

	expires := tokenSeconds
	request := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &expires}}

	granted, err := own.CoreV1().ServiceAccounts(c.namespace).CreateToken(ctx, c.account, request, metav1.CreateOptions{})
	if err != nil {
		return nil, redact(fmt.Errorf("ask for a token of service account %s/%s: %w", c.namespace, c.account, err), secrets)
	}

	if granted.Status.Token == "" {
		return nil, fmt.Errorf("the API server gave no token for service account %s/%s", c.namespace, c.account)
	}

	secrets = append(secrets, granted.Status.Token)

	acting := rest.AnonymousClientConfig(c.own)
	acting.BearerToken = granted.Status.Token

	client, err := dynamic.NewForConfig(c.configFor(acting))
	if err != nil {
		return nil, redact(fmt.Errorf("build the client for the rules: %w", err), secrets)
	}

	return &session{rules: client.Resource(rulesResource), secrets: secrets}, nil
}

// ownSecrets returns the credentials of Sidekick's own account.
func (c *cluster) ownSecrets() []string {
	if c.own.BearerToken == "" {
		return nil
	}

	return []string{c.own.BearerToken}
}

// configFor returns a copy of rc with Sidekick's user agent and rate limits.
// It asks for JSON, because client-go's typed clients send protobuf by
// default, which a test server cannot read, and a call is too rare for the
// size of the answer to matter.
func (*cluster) configFor(rc *rest.Config) *rest.Config {
	rc = rest.CopyConfig(rc)
	rc.ContentType = runtime.ContentTypeJSON
	rc.AcceptContentTypes = runtime.ContentTypeJSON
	rc.UserAgent = "sidekick/" + buildinfo.Version
	rc.QPS = clientQPS
	rc.Burst = clientBurst

	return rc
}

// get reads a PrometheusRule.
func (s *session) get(ctx context.Context, namespace, name string) (resource, error) {
	obj, err := s.rules.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return resource{}, redact(fmt.Errorf("read PrometheusRule %s/%s: %w", namespace, name, err), s.secrets)
	}

	res, err := toResource(obj.Object, obj.GetResourceVersion())
	if err != nil {
		return resource{}, fmt.Errorf("read PrometheusRule %s/%s: %w", namespace, name, err)
	}

	return res, nil
}

// patch replaces spec.groups of a PrometheusRule with groups, with a JSON
// merge patch that holds resourceVersion version, so the API server answers
// 409 when the resource changed since. Merge patch replaces a list whole,
// which is why groups is the whole list. With dryRun the API server runs
// everything but the write. It returns the resource as the server would
// store it, or has stored it.
func (s *session) patch(ctx context.Context, namespace, name string, groups groupList, version string, dryRun bool) (resource, error) {
	list, err := groups.marshal()
	if err != nil {
		return resource{}, fmt.Errorf("make the patch: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"metadata": map[string]string{"resourceVersion": version},
		"spec":     map[string]json.RawMessage{"groups": list},
	})
	if err != nil {
		return resource{}, fmt.Errorf("make the patch: %w", err)
	}

	opts := metav1.PatchOptions{FieldManager: fieldManager}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}

	obj, err := s.rules.Namespace(namespace).Patch(ctx, name, types.MergePatchType, body, opts)
	if err != nil {
		return resource{}, redact(fmt.Errorf("patch PrometheusRule %s/%s: %w", namespace, name, err), s.secrets)
	}

	res, err := toResource(obj.Object, obj.GetResourceVersion())
	if err != nil {
		return resource{}, fmt.Errorf("read the patched PrometheusRule %s/%s: %w", namespace, name, err)
	}

	return res, nil
}

// toResource takes the groups out of the content of a PrometheusRule.
func toResource(content map[string]any, version string) (resource, error) {
	var groups json.RawMessage

	if spec, ok := content["spec"].(map[string]any); ok {
		raw, err := json.Marshal(spec["groups"])
		if err != nil {
			return resource{}, fmt.Errorf("encode spec.groups: %w", err)
		}

		groups = raw
	}

	list, err := parseGroups(groups)
	if err != nil {
		return resource{}, fmt.Errorf("read spec.groups: %w", err)
	}

	return resource{version: version, groups: list}, nil
}

// redact returns err with every secret cut out of its text. It unwraps to
// err, so callers can still tell what kind of error it is. A client-go
// error does not carry the request headers, but a message from the server
// or a proxy could echo one.
func redact(err error, secrets []string) error {
	text := err.Error()
	cleaned := text

	for _, secret := range secrets {
		if secret != "" {
			cleaned = strings.ReplaceAll(cleaned, secret, redacted)
		}
	}

	return &redactedError{text: cleaned, err: err}
}

// isConflict reports whether the API server refused a write because the
// resource changed.
func isConflict(err error) bool {
	return apierrors.IsConflict(err)
}

// isRejected reports whether the API server refused the request in a way
// that repeating it cannot change: the resource does not exist, the account
// may not do it, or the object is not valid.
func isRejected(err error) bool {
	return apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) ||
		apierrors.IsInvalid(err) || apierrors.IsBadRequest(err)
}
