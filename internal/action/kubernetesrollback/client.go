package kubernetesrollback

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	appsclient "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
)

const (
	// fieldManager names Sidekick as the writer in the managed fields.
	fieldManager = "sidekick"
	// pageSize is how many objects one list call asks for.
	pageSize = 200
)

// session is the access of one operation, with the rights of the actions'
// service account.
type session struct {
	apps appsclient.AppsV1Interface
	// secrets are the tokens in use, which no error text may carry.
	secrets []string
}

// openSession asks for a token of the actions' service account and returns
// a session that uses it.
func openSession(ctx context.Context, cl *kubeaccess.Cluster) (*session, error) {
	access, err := cl.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("get a token: %w", err)
	}

	client, err := kubernetes.NewForConfig(access.Config)
	if err != nil {
		return nil, kubeaccess.Redact(fmt.Errorf("build the client for the workloads: %w", err), access.Secrets)
	}

	return &session{apps: client.AppsV1(), secrets: access.Secrets}, nil
}

// workload reads the workload that p names, with the history of its
// revisions.
func (s *session) workload(ctx context.Context, p Params) (workload, []revision, error) {
	w, err := s.get(ctx, p)
	if err != nil {
		return workload{}, nil, fmt.Errorf("read the %s: %w", p.Kind, err)
	}

	revs, err := s.history(ctx, w)
	if err != nil {
		return workload{}, nil, fmt.Errorf("read the revisions of %s: %w", w, err)
	}

	return w, revs, nil
}

// get reads one workload.
func (s *session) get(ctx context.Context, p Params) (workload, error) {
	var (
		w   workload
		err error
	)

	switch p.Kind {
	case kindDeployment:
		var d *appsv1.Deployment

		if d, err = s.apps.Deployments(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{}); err == nil {
			w = fromDeployment(d)
		}
	case kindStatefulSet:
		var st *appsv1.StatefulSet

		if st, err = s.apps.StatefulSets(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{}); err == nil {
			w = fromStatefulSet(st)
		}
	case kindDaemonSet:
		var ds *appsv1.DaemonSet

		if ds, err = s.apps.DaemonSets(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{}); err == nil {
			w = fromDaemonSet(ds)
		}
	default:
		return workload{}, fmt.Errorf("kind %q: %w", p.Kind, action.ErrLimit)
	}

	if err != nil {
		return workload{}, s.redact(fmt.Errorf("get %s %s/%s: %w", p.Kind, p.Namespace, p.Name, err))
	}

	return w, nil
}

// history lists the revisions the workload controls: the ReplicaSets of a
// Deployment, and the ControllerRevisions of a StatefulSet or DaemonSet.
func (s *session) history(ctx context.Context, w workload) ([]revision, error) {
	selector, err := metav1.LabelSelectorAsSelector(w.selector)
	if err != nil {
		return nil, fmt.Errorf("read the selector: %w: %w", action.ErrLimit, err)
	}

	if selector.Empty() {
		return nil, fmt.Errorf("%s has no selector: %w", w, action.ErrLimit)
	}

	if w.kind == kindDeployment {
		return s.replicaSets(ctx, w, selector)
	}

	return s.controllerRevisions(ctx, w, selector)
}

// replicaSets lists the ReplicaSets the Deployment controls.
func (s *session) replicaSets(ctx context.Context, w workload, selector labels.Selector) ([]revision, error) {
	var revs []revision

	opts := metav1.ListOptions{LabelSelector: selector.String(), Limit: pageSize}

	for {
		page, err := s.apps.ReplicaSets(w.namespace).List(ctx, opts)
		if err != nil {
			return nil, s.redact(fmt.Errorf("list ReplicaSets in %s: %w", w.namespace, err))
		}

		for i := range page.Items {
			if rs := &page.Items[i]; controlledBy(rs.OwnerReferences, w.uid) {
				revs = append(revs, fromReplicaSet(rs))
			}
		}

		if page.Continue == "" {
			return revs, nil
		}

		opts.Continue = page.Continue
	}
}

// controllerRevisions lists the ControllerRevisions the workload controls.
func (s *session) controllerRevisions(ctx context.Context, w workload, selector labels.Selector) ([]revision, error) {
	var revs []revision

	opts := metav1.ListOptions{LabelSelector: selector.String(), Limit: pageSize}

	for {
		page, err := s.apps.ControllerRevisions(w.namespace).List(ctx, opts)
		if err != nil {
			return nil, s.redact(fmt.Errorf("list ControllerRevisions in %s: %w", w.namespace, err))
		}

		for i := range page.Items {
			cr := &page.Items[i]
			if !controlledBy(cr.OwnerReferences, w.uid) {
				continue
			}

			rev, err := fromControllerRevision(cr)
			if err != nil {
				return nil, fmt.Errorf("ControllerRevision %s: %w: %w", cr.Name, action.ErrLimit, err)
			}

			revs = append(revs, rev)
		}

		if page.Continue == "" {
			return revs, nil
		}

		opts.Continue = page.Continue
	}
}

// rollTo gives the workload the pod template of revision rev.
func (s *session) rollTo(ctx context.Context, w workload, rev revision) error {
	body, err := patchBody(w, rev)
	if err != nil {
		return fmt.Errorf("make the patch: %w", err)
	}

	if err := s.patch(ctx, w, body); err != nil {
		return fmt.Errorf("patch the workload: %w", err)
	}

	return nil
}

// patch applies a strategic merge patch to the workload. The body holds the
// resourceVersion of w, so the API server answers 409 when the workload
// changed since it was read.
func (s *session) patch(ctx context.Context, w workload, body []byte) error {
	opts := metav1.PatchOptions{FieldManager: fieldManager}

	var err error

	switch w.kind {
	case kindDeployment:
		_, err = s.apps.Deployments(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, body, opts)
	case kindStatefulSet:
		_, err = s.apps.StatefulSets(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, body, opts)
	case kindDaemonSet:
		_, err = s.apps.DaemonSets(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, body, opts)
	default:
		return fmt.Errorf("kind %q: %w", w.kind, action.ErrLimit)
	}

	if err != nil {
		return s.redact(fmt.Errorf("patch %s: %w", w, err))
	}

	return nil
}

// redact cuts the tokens of the session out of an error text.
func (s *session) redact(err error) error {
	return kubeaccess.Redact(err, s.secrets)
}

// controlledBy reports whether the controller reference among refs points
// at the object with the given UID.
func controlledBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return ref.UID == uid
		}
	}

	return false
}

// classify tells Sidekick what an error from the cluster means for the
// proposal: a conflict, a refusal that repeating cannot change, or a failure
// to try again. An error that carries a sentinel already is left as it is.
func classify(err error) error {
	switch {
	case errors.Is(err, action.ErrConflict), errors.Is(err, action.ErrPermanent), errors.Is(err, action.ErrLimit):
		return fmt.Errorf("not tried again: %w", err)
	case apierrors.IsConflict(err):
		return fmt.Errorf("%w: %w", action.ErrConflict, err)
	case isRejected(err):
		return fmt.Errorf("%w: %w", action.ErrPermanent, err)
	default:
		return fmt.Errorf("will be tried again: %w", err)
	}
}

// isRejected reports whether the API server refused the request in a way
// that repeating it cannot change: the workload does not exist, the account
// may not do it, or the patch is not valid.
func isRejected(err error) bool {
	return apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) ||
		apierrors.IsInvalid(err) || apierrors.IsBadRequest(err)
}
