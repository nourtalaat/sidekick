// Package kubernetesrollback is the action kubernetes.rollback: it puts a
// Deployment, StatefulSet or DaemonSet of the cluster Sidekick runs in back
// to an earlier revision through the Kubernetes API, after an approver
// approved it, and it can put the revision that ran before back. It does
// what kubectl rollout undo does, without running kubectl. It is
// approval-only (it embeds action.ReadWriteGate) and shows both revisions
// (action.RevisionSnapshot).
//
// What the action relies on, as the public source of Kubernetes (tag
// v1.37.1) shows it, with paths in github.com/kubernetes/kubernetes:
//
//   - The revisions of a Deployment are the ReplicaSets it controls, each
//     with the number in the annotation deployment.kubernetes.io/revision
//     (pkg/controller/deployment/util/deployment_util.go, RevisionAnnotation
//     and Revision; a missing annotation reads as 0). The Deployment's own
//     template matches the ReplicaSet that its controller treats as new,
//     which is compared ignoring the pod-template-hash label, and the oldest
//     one when several match (same file, FindNewReplicaSet, EqualIgnoreHash).
//   - kubectl goes back by setting spec.template of the Deployment to the
//     template of the ReplicaSet with the pod-template-hash label removed
//     (staging/src/k8s.io/kubectl/pkg/polymorphichelpers/rollback.go,
//     DeploymentRollbacker). It also replaces all annotations of the
//     Deployment, which this action does not: it changes only spec.template,
//     so the change-cause annotation of the older revision is not restored.
//   - After the template changes back, the Deployment controller finds the
//     older ReplicaSet as the new one and raises its revision to the highest
//     of the others plus one (pkg/controller/deployment/sync.go,
//     getNewReplicaSet, and SetNewReplicaSetAnnotations). The ReplicaSet that
//     ran before keeps its number. So a revision has another number after a
//     rollback, and this action finds revisions by name and template, and
//     keeps the number for people to read only.
//   - The controller removes old ReplicaSets beyond spec.revisionHistoryLimit
//     (sync.go, cleanupDeployment), so with 0 no earlier revision is kept.
//   - The revisions of a StatefulSet or DaemonSet are the ControllerRevisions
//     that match its selector and that it controls, with the number in
//     Revision. Data is a strategic merge patch,
//     {"spec":{"template":{"$patch":"replace", ...}}}
//     (pkg/controller/statefulset/stateful_set_utils.go and
//     pkg/controller/daemon/update.go, getPatch), and kubectl applies it as
//     a strategic merge patch (rollback.go, StatefulSetRollbacker and
//     DaemonSetRollbacker; polymorphichelpers/history.go lists the history).
//     A rollback gives the equal revision the next number
//     (pkg/controller/statefulset/stateful_set_control.go,
//     getStatefulSetRevisions; pkg/controller/daemon/update.go,
//     constructHistory).
//   - A strategic merge patch replaces a map whole with the directive
//     $patch: replace (staging/src/k8s.io/apimachinery/pkg/util/strategicpatch/patch.go).
//     A patch that holds metadata.resourceVersion is applied to the stored
//     object (staging/src/k8s.io/apiserver/pkg/endpoints/handlers/patch.go,
//     strategicPatchObject), and the update is refused with 409 when the
//     version differs (staging/src/k8s.io/apiserver/pkg/registry/generic/registry/store.go,
//     in Update). A JSON patch with a test operation would answer 422, so
//     this action uses a strategic merge patch for all three kinds.
//   - RBAC can limit get and patch by name, because the name is in the URL,
//     and a list only when the request has the field selector metadata.name
//     (staging/src/k8s.io/apiserver/pkg/endpoints/request/requestinfo.go).
//     This action lists by label selector, so list on replicasets and
//     controllerrevisions cannot be limited by name.
//   - The managed fields of an object name the manager of each set of
//     fields and the time of the entry; entries of a subresource are about
//     the status (staging/src/k8s.io/apimachinery/pkg/apis/meta/v1/types.go,
//     ManagedFieldsEntry). Only the live workload has them: an earlier
//     revision has no record of who made it.
//   - Argo CD (tag v3.5.7) tracks by the annotation
//     argocd.argoproj.io/tracking-id by default (common/common.go,
//     AnnotationKeyAppInstance; docs/user-guide/resource_tracking.md) or by
//     a label whose key is configurable; the
//     default key is app.kubernetes.io/instance (LabelKeyAppInstance) and
//     argocd.argoproj.io/instance is the recommended one
//     (docs/user-guide/annotations-and-labels.md). Flux labels what it
//     applies with kustomize.toolkit.fluxcd.io/name (kustomize-controller
//     v1.10.1, internal/controller/kustomization_controller.go, SetOwnerLabels)
//     and helm.toolkit.fluxcd.io/name (helm-controller v1.7.1,
//     internal/postrender/common_renderer.go, originLabels).
//   - A token for a service account comes from the TokenRequest API; see
//     package kubeaccess.
package kubernetesrollback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: kubernetes.rollback".
const Type = "kubernetes.rollback"

var (
	errNoSnapshot  = errors.New("the proposal has no snapshot")
	errTarget      = errors.New("the snapshot is of another workload")
	errNotRollback = errors.New("the snapshot is not of a rollback")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "namespace", "name"],
  "properties": {
    "kind": {"type": "string", "enum": ["Deployment", "StatefulSet", "DaemonSet"], "description": "The kind of the workload."},
    "namespace": {"type": "string", "minLength": 1, "maxLength": 63, "description": "The namespace of the workload."},
    "name": {"type": "string", "minLength": 1, "maxLength": 253, "description": "The name of the workload."},
    "revisionsBack": {"type": "integer", "minimum": 1, "maximum": 10, "description": "How many revisions back to go. Default 1, the revision before the running one."}
  }
}`)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// RevisionsBack is nil when the model gave none, which means 1.
	RevisionsBack *int `json:"revisionsBack"`
}

// Rollback is one configured kubernetes.rollback action. It embeds the
// gate, so the built instance reports that it is approval-only, and the
// revision limit.
type Rollback struct {
	action.ReadWriteGate
	action.RevisionLimit

	name    string
	limits  Limits
	cluster *kubeaccess.Cluster
}

var (
	_ action.Action      = (*Rollback)(nil)
	_ action.Snapshotter = (*Rollback)(nil)
	_ action.Undoer      = (*Rollback)(nil)
)

// New builds the action from its validated config. It makes no call to the
// API server, but it fails outside a cluster, because the action uses the
// pod's service account to ask for tokens.
func New(_ context.Context, name string, cfg Config, _ registry.Deps) (action.Action, error) {
	cl, err := kubeaccess.InCluster(cfg.serviceAccount())
	if err != nil {
		return nil, fmt.Errorf("prepare the Kubernetes client: %w", err)
	}

	return newRollback(name, cfg, cl), nil
}

// newRollback builds the action around a way to reach the cluster.
func newRollback(name string, cfg Config, cl *kubeaccess.Cluster) *Rollback {
	return &Rollback{
		ReadWriteGate: cfg.ReadWriteGate, RevisionLimit: cfg.RevisionLimit, name: name, limits: cfg.Limits, cluster: cl,
	}
}

// Spec implements action.Action. The description tells the model which
// workloads it may roll back and what makes the action refuse.
func (r *Rollback) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Puts a Deployment, StatefulSet or DaemonSet back on an earlier revision through the Kubernetes API, as kubectl rollout undo does. ")
	b.WriteString("It changes the pod template only, and goes back revisionsBack revisions before the running one (default 1, at most ")
	fmt.Fprintf(&b, "%d). ", r.Max())
	b.WriteString("It refuses a workload with no earlier revision, a paused Deployment, and a workload managed by Argo CD or Flux. ")
	b.WriteString("Allowed namespaces: " + strings.Join(r.limits.Namespaces, ", ") + ". ")
	b.WriteString("Allowed kinds: " + strings.Join(r.limits.kinds(), ", ") + ". ")
	b.WriteString("Allowed names: " + strings.Join(r.limits.Names, ", ") + ".")

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It runs without calling the cluster,
// so it checks what the parameters alone show: the kind, the namespace, the
// name and the number of revisions against the limits. A limit violation
// wraps action.ErrLimit.
func (r *Rollback) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := r.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the workload and its
// revisions, refuses what must not be rolled back, and returns the revision
// that runs now as Before and the one it goes back to as After.
func (r *Rollback) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	sess, err := openSession(ctx, r.cluster)
	if err != nil {
		return action.Snapshot{}, classify(fmt.Errorf("get access: %w", err))
	}

	w, history, err := sess.workload(ctx, p)
	if apierrors.IsNotFound(err) {
		return action.Snapshot{}, fmt.Errorf("%s %s/%s does not exist: %w", p.Kind, p.Namespace, p.Name, action.ErrLimit)
	}

	if err != nil {
		return action.Snapshot{}, classify(fmt.Errorf("read the workload: %w", err))
	}

	running, goesBackTo, err := choose(w, history, p.back())
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("choose the revision: %w", err)
	}

	snap, err := action.RevisionSnapshot(target(p), running.id(), w.describe(running, true), w.describe(goesBackTo, false))
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It reads the workload again and gives it
// the pod template of the revision the snapshot says it goes back to. When
// the workload has that template already, which happens when a worker died
// after the write, it writes nothing and succeeds. When the workload does
// not run the revision the approver read as running any more, it fails with
// action.ErrConflict. The patch holds the resourceVersion that was just
// read, so the API server answers 409 if the workload changes in between.
func (r *Rollback) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	revs, err := checkSnapshot(p, inv.Snapshot)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot: %w", err)
	}

	sess, err := openSession(ctx, r.cluster)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("get access: %w", err))
	}

	w, history, err := sess.workload(ctx, p)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("read the workload: %w", err))
	}

	goal, found := find(history, nameOf(revs.GoesBackTo.ID))
	if found && sameTemplate(goal.template, w.template) {
		return rolledBack(w, revs), nil
	}

	if err := checkExecutable(w, history, revs, goal, found); err != nil {
		return action.Result{}, err
	}

	if err := sess.rollTo(ctx, w, goal); err != nil {
		return action.Result{}, classify(fmt.Errorf("write the rollback: %w", err))
	}

	return rolledBack(w, revs), nil
}

// Undo implements action.Undoer. It puts back the revision that ran before
// the rollback, which snap holds as Before. When the workload has its
// template already it writes nothing and succeeds. When the workload does
// not run the revision the rollback wrote, and force is false, it writes
// nothing and returns an *action.ConflictError with the revision it runs.
func (r *Rollback) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	p, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	revs, err := checkSnapshot(p, &snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot: %w", err)
	}

	sess, err := openSession(ctx, r.cluster)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("get access: %w", err))
	}

	w, history, err := sess.workload(ctx, p)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("read the workload: %w", err))
	}

	restore, found := find(history, nameOf(revs.Running.ID))
	if !found {
		return action.Result{}, fmt.Errorf("the revision to restore (%s) is no longer in the history of %s, so it cannot be restored: %w",
			revs.Running.ID, w, action.ErrPermanent)
	}

	if sameTemplate(restore.template, w.template) {
		return restored(w, revs), nil
	}

	if reason := w.refusal(); reason != "" {
		return action.Result{}, fmt.Errorf("%s: %w", reason, action.ErrPermanent)
	}

	wrote, found := find(history, nameOf(revs.GoesBackTo.ID))
	if !force && (!found || !sameTemplate(wrote.template, w.template)) {
		return action.Result{}, r.conflictWith(w, history)
	}

	if err := sess.rollTo(ctx, w, restore); err != nil {
		return action.Result{}, classify(fmt.Errorf("write the restore: %w", err))
	}

	return restored(w, revs), nil
}

// back returns how many revisions back the parameters ask for.
func (p Params) back() int {
	if p.RevisionsBack == nil {
		return action.DefaultMaxRevisionsBack
	}

	return *p.RevisionsBack
}

// parse decodes the parameters and checks them against the limits.
func (r *Rollback) parse(raw json.RawMessage) (Params, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return Params{}, fmt.Errorf("decode the parameters: %w", err)
	}

	if !slices.Contains(r.limits.kinds(), p.Kind) {
		return Params{}, fmt.Errorf("kind %q is not one of limits.kinds (%s): %w", p.Kind, strings.Join(r.limits.kinds(), ", "), action.ErrLimit)
	}

	if !slices.Contains(r.limits.Namespaces, p.Namespace) {
		return Params{}, fmt.Errorf("namespace %q is not one of limits.namespaces: %w", p.Namespace, action.ErrLimit)
	}

	if !slices.Contains(r.limits.Names, p.Name) {
		return Params{}, fmt.Errorf("name %q is not one of limits.names: %w", p.Name, action.ErrLimit)
	}

	if err := r.CheckRequest(p.back()); err != nil {
		return Params{}, fmt.Errorf("check revisionsBack: %w", err)
	}

	return p, nil
}

// choose picks the running revision and the one it goes back to, or says
// why the workload cannot be rolled back. Every refusal wraps action.ErrLimit.
func choose(w workload, history []revision, back int) (running, goesBackTo revision, err error) {
	if reason := w.refusal(); reason != "" {
		return revision{}, revision{}, fmt.Errorf("%s: %w", reason, action.ErrLimit)
	}

	if w.observed < w.generation {
		return revision{}, revision{}, fmt.Errorf("%s has a change its controller has not processed yet (generation %d, observed %d). "+
			"Read it again in a minute before proposing a rollback: %w", w, w.generation, w.observed, action.ErrLimit)
	}

	running, ok := current(w, history)
	if !ok {
		return revision{}, revision{}, fmt.Errorf("%s has no %s for its current pod template yet, so its running revision cannot be told. "+
			"Read it again in a minute: %w", w, revisionObject(w), action.ErrLimit)
	}

	before := earlier(running, history)

	switch {
	case len(before) == 0:
		return revision{}, revision{}, fmt.Errorf("%s has no earlier revision to go back to: only its current revision is recorded "+
			"(check spec.revisionHistoryLimit, or it was deployed once). Do not propose a rollback for it: %w", w, action.ErrLimit)
	case len(before) < back:
		return revision{}, revision{}, fmt.Errorf("%s has only %s, so it cannot go back %d. Ask for revisionsBack %d or less: %w",
			w, earlierRevisions(len(before)), back, len(before), action.ErrLimit)
	}

	return running, before[back-1], nil
}

// checkExecutable checks that the rollback the snapshot describes can still
// be made on the workload as it is now. Everything it finds wraps
// action.ErrConflict.
func checkExecutable(w workload, history []revision, revs *action.Revisions, goal revision, found bool) error {
	if !found {
		return conflict("the revision to go back to (%s) is no longer in the history of %s", revs.GoesBackTo.ID, w)
	}

	if reason := w.refusal(); reason != "" {
		return conflict("%s", reason)
	}

	if got := images(goal.template.Spec.Containers); got != revs.GoesBackTo.Version {
		return conflict("the revision to go back to (%s) has other images than the approver read: %s", revs.GoesBackTo.ID, got)
	}

	if now, ok := current(w, history); !ok || now.name != nameOf(revs.Running.ID) {
		return conflict("%s changed since the proposal: its pod template is no longer the one of %s", w, revs.Running.ID)
	}

	return nil
}

// conflictWith returns the conflict of an Undo, with the revision the
// workload runs as its live definition, in the form of Snapshot.Before.
func (*Rollback) conflictWith(w workload, history []revision) error {
	live := w.describeUnrecorded()
	if now, ok := current(w, history); ok {
		live = w.describe(now, true)
	}

	snap, err := action.RevisionSnapshot(target(Params{Kind: w.kind, Namespace: w.namespace, Name: w.name}), "", live, live)
	if err != nil {
		return fmt.Errorf("describe the live revision: %w: %w", action.ErrConflict, err)
	}

	return &action.ConflictError{Live: snap.Before}
}

// checkSnapshot checks that the snapshot of an invocation is the snapshot of
// a rollback of the workload the parameters name, and returns its revisions.
func checkSnapshot(p Params, snap *action.Snapshot) (*action.Revisions, error) {
	switch {
	case snap == nil:
		return nil, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	case snap.Target != target(p):
		return nil, fmt.Errorf("%w: %w", action.ErrPermanent, errTarget)
	case !snap.Revision:
		return nil, fmt.Errorf("%w: %w", action.ErrPermanent, errNotRollback)
	}

	revs, err := snap.Revisions()
	if err != nil {
		return nil, fmt.Errorf("%w: read the revisions: %w", action.ErrPermanent, err)
	}

	return revs, nil
}

// target names the workload, as Snapshot.Target.
func target(p Params) string {
	return "kubernetes:" + strings.ToLower(p.Kind) + "/" + p.Namespace + "/" + p.Name
}

// nameOf returns the name of the revision object from a revision ID.
func nameOf(id string) string {
	name, _, _ := strings.Cut(id, ":")

	return name
}

// conflict returns an error that wraps action.ErrConflict.
func conflict(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), action.ErrConflict)
}

// rolledBack is the result of a rollback that is in place.
func rolledBack(w workload, revs *action.Revisions) action.Result {
	return action.Result{Summary: fmt.Sprintf("Rolled back %s from %s to %s.", w, revs.Running.ID, revs.GoesBackTo.ID)}
}

// restored is the result of an undo that is in place.
func restored(w workload, revs *action.Revisions) action.Result {
	return action.Result{Summary: fmt.Sprintf("Restored %s to %s.", w, revs.Running.ID)}
}

// revisionObject names what holds the revisions of a workload.
func revisionObject(w workload) string {
	if w.kind == kindDeployment {
		return "ReplicaSet"
	}

	return "ControllerRevision"
}

// earlierRevisions writes a number of earlier revisions.
func earlierRevisions(n int) string {
	if n == 1 {
		return "1 earlier revision"
	}

	return fmt.Sprintf("%d earlier revisions", n)
}
