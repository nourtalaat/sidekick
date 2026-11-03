// Package helmrollback is the action helm.rollback: it puts a Helm release
// back on an earlier revision with Helm's own Go library, the code that
// "helm rollback" runs, after an approver approved it, and it can undo that.
// It is approval-only, and it works on the cluster Sidekick runs in only.
// docs/plugins/actions.md ("Rollback actions") says how a rollback action
// shows its two revisions and limits how far back it goes.
//
// It uses Helm v4.3.1 (module helm.sh/helm/v4). Helm v3 gets security fixes
// only until 2026-11-11 (https://helm.sh/blog/helm-4-released/), and both
// lines need the same k8s.io module versions as Sidekick.
//
// What the action relies on, as the public source of Helm at the tag v4.3.1
// (https://github.com/helm/helm/blob/v4.3.1/<file>) and of Kubernetes at the
// tag v1.37.1 show it:
//
//   - pkg/action/history.go: NewHistory(cfg).Run(name) returns every
//     revision of a release, in no promised order, so the action sorts them.
//     A release with no record gives driver.ErrReleaseNotFound
//     (pkg/storage/driver/secrets.go, Query). A record that cannot be
//     decoded is skipped there, without an error.
//   - pkg/action/rollback.go: NewRollback(cfg) has Version (the revision to
//     go back to), Timeout, WaitStrategy, WaitOptions, DisableHooks,
//     ForceReplace, MaxHistory and Description, and Run(name) takes no
//     context. Run writes revision current+1, with status pending-rollback,
//     RollbackRevision set to the target and the chart, values, manifest and
//     hooks of the target record, before it touches any object. So the
//     chart of the target need not exist anywhere but in the history, and a
//     target that the history no longer holds fails with "release has no N
//     version". Hooks run unless DisableHooks is set, and the timeout is used
//     for the hooks and for the wait one after the other.
//   - pkg/storage/storage.go and pkg/storage/driver/secrets.go: a revision is
//     stored under the key sh.helm.release.v1.<name>.v<N>, and Create fails
//     with driver.ErrReleaseExists when the key exists. Two operations that
//     start from the same revision cannot both write the next one; this is
//     the only precondition Helm gives. With MaxHistory 0 Helm removes no
//     revision.
//   - pkg/release/common/status.go: the statuses are unknown, deployed,
//     uninstalled, superseded, failed, uninstalling, pending-install,
//     pending-upgrade and pending-rollback. superseded is set on the old
//     revision when an upgrade (pkg/action/upgrade.go, line 514) or a
//     rollback (pkg/action/rollback.go, line 316) succeeds. A failed upgrade
//     (upgrade.go, failRelease) marks only the new revision failed and leaves
//     the revision before it deployed, although the objects in the cluster
//     are the failed revision's. So the revision that runs is the newest one
//     when it is deployed or failed, as "helm rollback" counts from the
//     newest revision (rollback.go, prepareRollback), and the revisions to go
//     back to are the earlier ones with status superseded or deployed.
//   - pkg/action/rollback.go, performRollback: when the wait fails or times
//     out, the new revision gets the status failed and the description
//     "Release \"x\" failed: ...", and the old revision stays deployed. When
//     applying the objects fails, the old revision becomes superseded and
//     the new one failed. When a pre-rollback hook fails, nothing updates the
//     new revision, which stays pending-rollback. A rollback that works
//     makes the new revision deployed and the old ones superseded.
//   - pkg/kube/client.go and pkg/kube/options.go: a rollback needs a wait
//     strategy (an empty one fails); StatusWatcherStrategy waits for the
//     objects and HookOnlyStrategy only for the hooks; kube.WithWaitContext
//     ends the waits with a context.
//   - pkg/action/action.go, Configuration.Init(getter, namespace, driver):
//     the driver is "secret" (the default), "configmap", "memory" or "sql",
//     and the library reads no HELM_DRIVER environment variable. The
//     namespace of Helm's client comes from the getter's raw config loader
//     (pkg/kube/client.go, namespace).
//   - pkg/kube/client.go, update: a rollback reads each object of the target
//     revision (get), creates the missing ones, patches the others, reads
//     and deletes the objects that only the old revision has, so the
//     account needs get, create, patch and delete on every kind in the
//     chart, and the wait needs list and watch. The release records need
//     list, get, create and update.
//   - pkg/release/v1 (Release, Info) and pkg/chart/v2 (Metadata): the facts
//     of a revision, among them Info.LastDeployed, Info.Description,
//     Info.RollbackRevision and the chart name, version and app version.
//   - Kubernetes, plugin/pkg/auth/authorizer/rbac/bootstrappolicy/policy.go
//     (editRules): the built-in edit ClusterRole covers those verbs on
//     Secrets, ConfigMaps and the common workload kinds, and reads every
//     Secret of a namespace, but it does not cover Roles, RoleBindings or
//     custom resources.
//   - A token for the service account comes from the TokenRequest API
//     (see package kubeaccess).
package helmrollback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/release/common"
	"helm.sh/helm/v4/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: helm.rollback".
const Type = "helm.rollback"

// maxNamespaceLength is the longest name of a namespace.
const maxNamespaceLength = 63

var (
	errNoSnap = errors.New("the proposal has no snapshot")
	errTarget = errors.New("the snapshot is of another release")
	errShape  = errors.New("the snapshot is not the one of a rollback to an earlier revision")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate. maxBack is the most revisions back
// the config allows.
func paramsSchema(maxBack int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["release", "namespace"],
  "properties": {
    "release": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The name of the Helm release."},
    "namespace": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The namespace of the Helm release."},
    "revisionsBack": {"type": "integer", "minimum": 1, "maximum": %d, "description": "How many revisions to go back. Counts the earlier revisions with status superseded or deployed; failed ones are skipped. Default 1."}
  }
}`, maxReleaseNameLength, maxNamespaceLength, maxBack))
}

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	Release       string `json:"release"`
	Namespace     string `json:"namespace"`
	RevisionsBack *int   `json:"revisionsBack"`
}

// Rollback is one configured helm.rollback action. It embeds the gate and
// the revision limit, so the built instance reports that it is approval-only
// and how far back it may go.
type Rollback struct {
	action.ReadWriteGate
	action.RevisionLimit

	name        string
	limits      Limits
	store       string
	wait        bool
	waitTimeout time.Duration
	open        opener
}

var (
	_ action.Action      = (*Rollback)(nil)
	_ action.Snapshotter = (*Rollback)(nil)
	_ action.Undoer      = (*Rollback)(nil)
)

// request is a parsed proposal.
type request struct {
	release   string
	namespace string
	back      int
}

// plan is what a snapshot says: the revision that ran when it was taken and
// the one it goes back to, as Helm numbers them.
type plan struct {
	running int
	target  int
}

// New builds the action from its validated config. It makes no call to the
// API server, but it fails outside a cluster, because the action uses the
// pod's service account to ask for tokens.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	cluster, err := kubeaccess.InCluster(cfg.serviceAccount())
	if err != nil {
		return nil, fmt.Errorf("prepare the Kubernetes client: %w", err)
	}

	return newRollback(name, cfg, clusterOpener(cluster, cfg.driver(), deps.Logger)), nil
}

// newRollback builds the action around a way to open Helm.
func newRollback(name string, cfg Config, open opener) *Rollback {
	return &Rollback{
		ReadWriteGate: cfg.ReadWriteGate,
		RevisionLimit: cfg.RevisionLimit,
		name:          name,
		limits:        cfg.Limits,
		store:         storeKind(cfg.driver()),
		wait:          cfg.wait(),
		waitTimeout:   cfg.waitTimeout(),
		open:          open,
	}
}

// Spec implements action.Action. The description tells the model which
// releases it may touch and what "back" means.
func (r *Rollback) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Puts a Helm release back on an earlier revision, as helm rollback does; Helm makes a new revision with the old chart, values and manifests. ")
	b.WriteString("The revision that runs is the newest one, and revisionsBack counts the earlier revisions with status superseded or deployed, skipping failed ones. ")
	b.WriteString("A release with an operation that is running or was interrupted, or with no earlier revision, is refused. ")
	b.WriteString("A person approves it, and the proposal shows both revisions. ")
	b.WriteString("Allowed namespaces: " + strings.Join(r.limits.Namespaces, ", ") + ". ")
	b.WriteString("Allowed releases: " + strings.Join(r.limits.Releases, ", ") + " (* stands for any run of characters). ")
	fmt.Fprintf(&b, "At most %d revisions back.", r.Max())

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: paramsSchema(r.Max())}
}

// Validate implements action.Action. It runs without calling the cluster, so
// it checks what the parameters alone show: the namespace, the release name
// and how far back. A limit violation wraps action.ErrLimit.
func (r *Rollback) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := r.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the history of the
// release, finds the revision that runs and the one that goes back, and
// refuses what the limits or the history do not allow. The version of the
// snapshot is the number of the revision that runs.
func (r *Rollback) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	req, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	sess, err := r.open(ctx, req.namespace)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("open Helm: %w", err)
	}

	revs, err := sess.history(req.release)
	if err != nil {
		return action.Snapshot{}, explain(req, fmt.Errorf("read the history: %w", err))
	}

	running, target, err := choose(req.release, req.namespace, revs, req.back)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("choose the revisions: %w", err)
	}

	snap, err := action.RevisionSnapshot(targetOf(req), strconv.Itoa(running.version), running.document(), target.document())
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the history, and rolls back to the revision of the snapshot, unless the
// release changed since (action.ErrConflict). When the rollback is in place
// already, which happens when a worker died after Helm made it, it changes
// nothing and succeeds. A rollback that Helm started and that ended as
// failed, or that an interruption left pending, is a permanent failure that
// needs a person, because repeating it would make more revisions.
func (r *Rollback) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	req, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	pl, err := checkSnapshot(req, inv.Snapshot)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot: %w", err)
	}

	sess, revs, err := r.read(ctx, req)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("read the release: %w", err))
	}

	done, err := checkExecute(req.release, r.store, revs, pl.running, pl.target)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the release with the snapshot: %w", err)
	}

	made := revs[len(revs)-1]

	if !done {
		description := fmt.Sprintf("Rollback to %d by Sidekick (proposal %s)", pl.target, inv.ProposalID)

		made, err = r.apply(ctx, sess, req, revs, rollbackOptions{target: pl.target, wait: r.wait, description: description})
		if err != nil {
			return action.Result{}, fmt.Errorf("roll back: %w", err)
		}
	}

	return action.Result{Summary: fmt.Sprintf("Rolled back Helm release %q in namespace %q from revision %d to revision %d (Helm made revision %d).",
		req.release, req.namespace, pl.running, pl.target, made.version)}, nil
}

// Undo implements action.Undoer. It rolls back to the revision that ran
// before the rollback, which Helm makes a new revision again. When the
// release is restored already it changes nothing and succeeds. When someone
// changed the release since the rollback, and force is false, it changes
// nothing and returns an *action.ConflictError with the revision that runs
// now. An undo does not wait for the objects, because the person who asked
// for it waits for the answer.
func (r *Rollback) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	req, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	pl, err := checkSnapshot(req, &snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot: %w", err)
	}

	sess, revs, err := r.read(ctx, req)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("read the release: %w", err))
	}

	state, live, err := checkUndo(req.release, r.store, revs, pl.running, pl.target)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the release with the snapshot: %w", err)
	}

	if state == undoConflict && !force {
		raw, err := liveDocument(req, live)
		if err != nil {
			return action.Result{}, fmt.Errorf("describe the revision that runs: %w", err)
		}

		return action.Result{}, &action.ConflictError{Live: raw}
	}

	made := live

	if state != undoDone {
		description := fmt.Sprintf("Rollback to %d by Sidekick (undo of proposal %s)", pl.running, inv.UndoOf)

		made, err = r.apply(ctx, sess, req, revs, rollbackOptions{target: pl.running, wait: false, description: description})
		if err != nil {
			return action.Result{}, fmt.Errorf("roll back: %w", err)
		}
	}

	return action.Result{Summary: fmt.Sprintf("Rolled back Helm release %q in namespace %q to the state of revision %d (Helm made revision %d).",
		req.release, req.namespace, pl.running, made.version)}, nil
}

// parse decodes the parameters and checks them against the limits.
func (r *Rollback) parse(raw json.RawMessage) (request, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return request{}, fmt.Errorf("decode the parameters: %w", err)
	}

	if !slices.Contains(r.limits.Namespaces, p.Namespace) {
		return request{}, fmt.Errorf("namespace %q is not one of limits.namespaces: %w", p.Namespace, action.ErrLimit)
	}

	if err := chartutil.ValidateReleaseName(p.Release); err != nil {
		return request{}, fmt.Errorf("release %q is not a valid Helm release name: %w: %w", p.Release, action.ErrParams, err)
	}

	if !slices.ContainsFunc(r.limits.Releases, func(pattern string) bool { return matches(pattern, p.Release) }) {
		return request{}, fmt.Errorf("release %q is not allowed by limits.releases: %w", p.Release, action.ErrLimit)
	}

	back := 1
	if p.RevisionsBack != nil {
		back = *p.RevisionsBack
	}

	if err := r.CheckRequest(back); err != nil {
		return request{}, fmt.Errorf("check revisionsBack: %w", err)
	}

	return request{release: p.Release, namespace: p.Namespace, back: back}, nil
}

// read opens Helm and reads the history of the release of the request.
func (r *Rollback) read(ctx context.Context, req request) (*session, []revision, error) {
	sess, err := r.open(ctx, req.namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("open Helm: %w", err)
	}

	revs, err := sess.history(req.release)
	if err != nil {
		return nil, nil, fmt.Errorf("read the history: %w", err)
	}

	return sess, revs, nil
}

// apply runs Helm's rollback and returns the revision it made. before is the
// history that was read right before. When Helm fails, the history says
// whether it wrote a revision: then the failure is permanent, because
// repeating it would make another revision. When it wrote nothing, the
// failure may pass.
func (r *Rollback) apply(ctx context.Context, sess *session, req request, before []revision, o rollbackOptions) (revision, error) {
	o.timeout = r.waitTimeout

	newest := before[len(before)-1]

	runErr := sess.rollback(ctx, req.release, o)

	after, err := sess.history(req.release)
	if err != nil {
		if runErr != nil {
			return revision{}, classify(runErr)
		}

		return revision{}, classify(fmt.Errorf("read the history after the rollback: %w", err))
	}

	var made revision
	if len(after) > 0 {
		made = after[len(after)-1]
	}

	switch {
	case made.version > newest.version && runErr != nil:
		return revision{}, r.failedRollback(req, made, runErr)
	case runErr != nil:
		return revision{}, classify(runErr)
	case made.version <= newest.version || made.status != common.StatusDeployed || made.rollbackRevision != o.target:
		return revision{}, fmt.Errorf("the rollback of release %q ended without an error, but its newest revision is %d (status %s) and not a rollback to revision %d: %w",
			req.release, made.version, made.status, o.target, action.ErrPermanent)
	default:
		return made, nil
	}
}

// failedRollback is the permanent failure for a rollback that Helm started
// and that wrote revision made, which did not end as deployed.
func (r *Rollback) failedRollback(req request, made revision, runErr error) error {
	if made.status.IsPending() {
		return fmt.Errorf("%w: %w", interrupted(req.release, r.store, made), runErr)
	}

	return fmt.Errorf("the rollback of release %q that Helm started made revision %d, which ended with status %s: %s: %w",
		req.release, made.version, made.status, made.description, action.ErrPermanent)
}

// explain adds, to a failure to read the history because the service
// account may not, what the operator has to do.
func explain(req request, err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("the service account of the action may not read the Helm release records in namespace %q; "+
			"the operator has to bind a Role to it, see the docs of helm.rollback: %w", req.namespace, err)
	}

	return fmt.Errorf("read the Helm release records in namespace %q: %w", req.namespace, err)
}

// checkSnapshot checks that the snapshot of an invocation is the one of a
// rollback of this release to an earlier revision, and returns the numbers
// of the two revisions.
func checkSnapshot(req request, snap *action.Snapshot) (plan, error) {
	if snap == nil {
		return plan{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnap)
	}

	if snap.Target != targetOf(req) {
		return plan{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTarget)
	}

	revisions, err := snap.Revisions()
	if err != nil || revisions == nil {
		return plan{}, fmt.Errorf("%w: %w", action.ErrPermanent, errShape)
	}

	running, errRunning := strconv.Atoi(revisions.Running.ID)
	target, errTargetID := strconv.Atoi(revisions.GoesBackTo.ID)

	if errRunning != nil || errTargetID != nil || target >= running || target < 1 || snap.Version != revisions.Running.ID {
		return plan{}, fmt.Errorf("%w: %w", action.ErrPermanent, errShape)
	}

	return plan{running: running, target: target}, nil
}

// liveDocument writes the revision that runs now as a revision document, for
// the conflict of an undo to show.
func liveDocument(req request, live revision) (json.RawMessage, error) {
	snap, err := action.RevisionSnapshot(targetOf(req), strconv.Itoa(live.version), live.document(), live.document())
	if err != nil {
		return nil, fmt.Errorf("make the document: %w", err)
	}

	return snap.Before, nil
}

// classify tells Sidekick what an error from the cluster or from Helm means
// for the proposal: a conflict, a refusal that repeating cannot change, or a
// failure to try again.
func classify(err error) error {
	switch {
	case errors.Is(err, driver.ErrReleaseExists):
		return fmt.Errorf("another Helm operation wrote the next revision first: %w: %w", action.ErrConflict, err)
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err), apierrors.IsNotFound(err), apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return fmt.Errorf("%w: %w", action.ErrPermanent, err)
	default:
		return fmt.Errorf("will be tried again: %w", err)
	}
}

// matches reports whether the release name matches an entry of
// limits.releases, in which "*" stands for any run of characters.
func matches(pattern, release string) bool {
	ok, err := path.Match(pattern, release)

	return err == nil && ok
}

// targetOf names the release, as Snapshot.Target.
func targetOf(req request) string {
	return "helm:release/" + req.namespace + "/" + req.release
}
