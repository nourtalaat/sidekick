package prometheusrule

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
)

// fieldManager names Sidekick as the writer in the managed fields.
const fieldManager = "sidekick"

// rulesResource is the PrometheusRule resource of the Prometheus Operator.
var rulesResource = schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}

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

// openSession asks for a token of the actions' service account and returns
// a session that uses it.
func openSession(ctx context.Context, cl *kubeaccess.Cluster) (*session, error) {
	access, err := cl.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("get a token: %w", err)
	}

	client, err := dynamic.NewForConfig(access.Config)
	if err != nil {
		return nil, kubeaccess.Redact(fmt.Errorf("build the client for the rules: %w", err), access.Secrets)
	}

	return &session{rules: client.Resource(rulesResource), secrets: access.Secrets}, nil
}

// get reads a PrometheusRule.
func (s *session) get(ctx context.Context, namespace, name string) (resource, error) {
	obj, err := s.rules.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return resource{}, kubeaccess.Redact(fmt.Errorf("read PrometheusRule %s/%s: %w", namespace, name, err), s.secrets)
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
		return resource{}, kubeaccess.Redact(fmt.Errorf("patch PrometheusRule %s/%s: %w", namespace, name, err), s.secrets)
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
