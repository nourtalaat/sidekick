// Package azurecontainerapp is the action azure.container_app_rollback: it
// moves all the traffic of an Azure Container App back to an earlier
// revision through Azure Resource Manager, after an approver approved the
// change, and it can move the traffic to the revision it came from. It is
// approval-only: it embeds action.ReadWriteGate, so a read-write route may
// list it only when the operator set allowReadWrite. It is a rollback
// action as docs/plugins/actions.md describes: Snapshot shows both
// revisions, Execute fails with action.ErrConflict when the app changed
// since the proposal, and Undo puts the traffic back.
//
// What the action relies on from Azure, as the REST API specification
// (github.com/Azure/azure-rest-api-specs, commit
// 9d258d7333f6dd7ae3a5bdbb9b9c2f671713bac8, specification/app/resource-manager/
// Microsoft.App/ContainerApps/stable/2026-07-01/openapi.json and
// ContainerApp.tsp) and the Go SDK (github.com/Azure/azure-sdk-for-go, tag
// sdk/resourcemanager/appcontainers/armappcontainers/v5.0.0, which calls
// API version 2026-07-01, and azcore v1.23.3) show:
//
//   - activeRevisionsMode is Multiple or Single, and Single is the default.
//     In Single mode Azure sends all traffic to the latest revision and
//     revision weights cannot be used, so the action refuses.
//   - configuration.ingress.traffic is a list of {revisionName, weight,
//     latestRevision, label}. latestRevision marks a weight that follows
//     the "latest stable revision", which the action reads as
//     properties.latestReadyRevisionName and checks against the traffic
//     weights that the revisions list reports.
//   - The revisions list gives each revision's active, createdTime,
//     trafficWeight and template.containers[].image. The order of revisions
//     is by createdTime.
//   - PATCH on the container app is "JSON Merge Patch": objects merge and a
//     list is replaced whole, so a body with only ingress.traffic (and the
//     location the app has, as the specification's own example includes
//     one) changes the traffic and nothing else. It is a long-running
//     operation (200 or 202 with a Location header) that the SDK's poller
//     waits for. A PUT is not used because the answer of a read has no
//     secret values to send back.
//   - The specification has no ETag or If-Match for container apps, and the
//     SDK exposes neither, so Snapshot.Version is a hash of the traffic and
//     the latest revision names. A change between the read and the PATCH,
//     which takes moments, is not caught.
//   - The SDK registers the resource provider by itself when Azure answers
//     MissingSubscriptionRegistration, which writes outside the role, so
//     the client turns that off (arm.ClientOptions.DisableRPRegistration).
//   - The role needs Microsoft.App/containerApps/read, .../write and
//     .../revisions/read (the permissions reference of the Azure docs).
package azurecontainerapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/azureauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: azure.container_app_rollback".
const Type = "azure.container_app_rollback"

// portalURL is the root of the Azure portal.
const portalURL = "https://portal.azure.com"

var (
	errNoSecrets    = errors.New("no secrets resolver to read the client secret with")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
	errNotRollback  = errors.New("the snapshot is not the snapshot of a rollback")
	errTargetDiffer = errors.New("the snapshot is of another container app")
	errNoMode       = errors.New("the snapshot does not say how traffic was set")
	errNotActive    = errors.New("the revision to restore is not active any more; activate it in Azure and undo again")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["app"],
  "properties": {
    "subscription": {"type": "string", "description": "The ID of the subscription. Leave it out when only one is allowed."},
    "resourceGroup": {"type": "string", "description": "The name of the resource group. Leave it out when only one is allowed."},
    "app": {"type": "string", "description": "The name of the container app."},
    "revisionsBack": {"type": "integer", "minimum": 1, "description": "How many revisions back to go, counted by the time the revisions were created. Default 1."}
  }
}`)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	Subscription  string `json:"subscription"`
	ResourceGroup string `json:"resourceGroup"`
	App           string `json:"app"`
	// RevisionsBack is how many revisions back to go. Nil means 1.
	RevisionsBack *int `json:"revisionsBack"`
}

// Rollback is one configured azure.container_app_rollback action.
type Rollback struct {
	action.ReadWriteGate
	action.RevisionLimit

	name   string
	limits Limits
	arms   *arms
}

var (
	_ action.Action      = (*Rollback)(nil)
	_ action.Snapshotter = (*Rollback)(nil)
	_ action.Undoer      = (*Rollback)(nil)
)

// New builds the action from its validated config. It makes no network
// call: the credential is set up, and the first token made, by the first
// call to Azure, so Azure being down does not stop Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	if cfg.ClientSecret != nil && deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	cred := azureauth.NewCredential(cfg.identity(), deps.Secrets, hc)

	return newRollback(name, cfg, cred, &arm.ClientOptions{
		ClientOptions:         azcore.ClientOptions{Transport: hc},
		DisableRPRegistration: true,
	}), nil
}

// newRollback builds the action with the credential and client options of
// Azure Resource Manager. Tests give a fake credential and an endpoint.
func newRollback(name string, cfg Config, cred azcore.TokenCredential, opts *arm.ClientOptions) *Rollback {
	return &Rollback{
		ReadWriteGate: cfg.ReadWriteGate,
		RevisionLimit: cfg.RevisionLimit,
		name:          name,
		limits:        cfg.Limits,
		arms:          &arms{cred: cred, opts: opts, pollEvery: pollInterval, pollTimeout: pollTimeout},
	}
}

// Spec implements action.Action. The description tells the model what the
// action does and which apps it may touch.
func (r *Rollback) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Moves all the traffic of an Azure Container App back to an earlier revision, counted by the time revisions were created. ")
	b.WriteString("It refuses an app in single revision mode, an app with no ingress, traffic that is split between revisions, and a revision that is inactive. ")
	fmt.Fprintf(&b, "It goes back at most %d revisions. ", r.Max())
	b.WriteString("Subscriptions allowed: " + strings.Join(r.limits.Subscriptions, ", ") + ". ")
	b.WriteString("Resource groups allowed: " + strings.Join(r.limits.ResourceGroups, ", ") + ". ")
	b.WriteString("Apps allowed: " + strings.Join(r.limits.Apps, ", ") + ".")

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It runs without calling Azure, so it
// checks what the parameters alone show: how many revisions back against
// maxRevisionsBack, and the subscription, resource group and app against
// the limits. A violation wraps action.ErrLimit.
func (r *Rollback) Validate(_ context.Context, raw json.RawMessage) error {
	if _, _, err := r.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the app and its
// revisions, works out which revision runs and which is the one to go back
// to, and refuses a state the action does not change. It changes nothing.
func (r *Rollback) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	s, back, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	l, err := r.arms.read(ctx, s)
	if statusOf(err) == http.StatusNotFound {
		return action.Snapshot{}, refuse("container app %s does not exist in resource group %s of subscription %s.", s.app, s.resourceGroup, s.subscription)
	}

	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the container app: %w", err)
	}

	run, err := l.running(s.app)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("find the running revision: %w", err)
	}

	target, err := l.target(s.app, run, back)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("find the revision to go back to: %w", err)
	}

	runningNotes, goesBackNotes := notesByName, notesMoves
	if run.latest {
		runningNotes, goesBackNotes = notesFollowsLatest, notesPinned
	}

	snap, err := action.RevisionSnapshot(s.target(), l.version(&run), describe(l.revision(run.name), runningNotes), describe(target, goesBackNotes))
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the app, and moves all the traffic to the revision the snapshot says,
// unless the traffic or the latest revision changed since the snapshot
// (action.ErrConflict). When all the traffic is on that revision already,
// which happens when a worker died after the change, it writes nothing and
// succeeds. While Azure is still updating the app it fails without the
// permanent mark, so the job tries again.
func (r *Rollback) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	s, _, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	snap, revs, err := rollbackSnapshot(inv.Snapshot, s)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot: %w", err)
	}

	l, err := r.arms.read(ctx, s)
	if err != nil {
		return action.Result{}, fail("read the container app", err)
	}

	if l.inProgress() {
		return action.Result{}, errInProgress
	}

	run, runErr := l.running(s.app)
	if runErr == nil && run.name == revs.GoesBackTo.ID {
		return moved(s, revs, snap.Version), nil
	}

	if l.version(runningOrNil(run, runErr)) != snap.Version {
		return action.Result{}, fmt.Errorf("container app %s: its traffic or latest revision changed since the proposal: %w", s.app, action.ErrConflict)
	}

	if !isActive(l.revision(revs.GoesBackTo.ID)) {
		return action.Result{}, fmt.Errorf("container app %s: revision %s is not active any more: %w", s.app, revs.GoesBackTo.ID, action.ErrConflict)
	}

	list := shift(l.traffic(), destination{revision: revs.GoesBackTo.ID})
	if err := r.arms.setTraffic(ctx, s, l.app.Location, list); err != nil {
		return action.Result{}, fail("move the traffic", err)
	}

	return moved(s, revs, snap.Version), nil
}

// Undo implements action.Undoer. It moves all the traffic back to the
// revision that ran before the rollback, from snap, and sets it the way it
// was: by name, or following the latest revision while the revision to
// restore is still the latest one that is ready. When the traffic is on that
// revision already it writes nothing and succeeds. When the traffic is not
// all on the revision the rollback moved it to, and force is false, it
// writes nothing and returns an *action.ConflictError with the revision
// that runs now.
func (r *Rollback) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	s, _, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	snap, revs, err := rollbackSnapshot(&snap, s)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot: %w", err)
	}

	mode, ok := modeOf(snap.Version)
	if !ok {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoMode)
	}

	l, err := r.arms.read(ctx, s)
	if err != nil {
		return action.Result{}, fail("read the container app", err)
	}

	if l.inProgress() {
		return action.Result{}, errInProgress
	}

	run, runErr := l.running(s.app)
	if runErr == nil && run.name == revs.Running.ID {
		return restored(s, revs, mode == trafficLatest && run.latest), nil
	}

	if (runErr != nil || run.name != revs.GoesBackTo.ID) && !force {
		return action.Result{}, &action.ConflictError{Live: liveDocument(s, l, run, runErr)}
	}

	if !isActive(l.revision(revs.Running.ID)) {
		return action.Result{}, fmt.Errorf("revision %s: %w: %w", revs.Running.ID, action.ErrPermanent, errNotActive)
	}

	latest := mode == trafficLatest && l.latestReadyName() == revs.Running.ID

	list := shift(l.traffic(), destination{revision: revs.Running.ID, latest: latest})
	if err := r.arms.setTraffic(ctx, s, l.app.Location, list); err != nil {
		return action.Result{}, fail("restore the traffic", err)
	}

	return restored(s, revs, latest), nil
}

// moved is the result of a rollback.
func moved(s scope, revs *action.Revisions, version string) action.Result {
	text := fmt.Sprintf("Moved all traffic of container app %s from revision %s to revision %s.", s.app, revs.Running.ID, revs.GoesBackTo.ID)

	if mode, _ := modeOf(version); mode == trafficLatest {
		text += " Traffic is now pinned to that revision by name, so a new revision gets no traffic until someone moves it."
	}

	return action.Result{Summary: text, Links: []action.Link{s.link()}}
}

// parse decodes the parameters, checks them against the limits, and returns
// the app they name and how many revisions back to go.
func (r *Rollback) parse(raw json.RawMessage) (scope, int, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return scope{}, 0, fmt.Errorf("decode the parameters: %w", err)
	}

	back := 1
	if p.RevisionsBack != nil {
		back = *p.RevisionsBack
	}

	if err := r.CheckRequest(back); err != nil {
		return scope{}, 0, fmt.Errorf("check the number of revisions back: %w", err)
	}

	subscription, err := pickOne("subscription", p.Subscription, r.limits.Subscriptions)
	if err != nil {
		return scope{}, 0, fmt.Errorf("check the subscription: %w", err)
	}

	group, err := pickOne("resourceGroup", p.ResourceGroup, r.limits.ResourceGroups)
	if err != nil {
		return scope{}, 0, fmt.Errorf("check the resource group: %w", err)
	}

	app := strings.ToLower(p.App)
	if app == "" || !r.limits.allowsApp(app) {
		return scope{}, 0, fmt.Errorf("app %q is not one of limits.apps: %w", p.App, action.ErrLimit)
	}

	return scope{subscription: subscription, resourceGroup: group, app: app}, back, nil
}

// target returns the Snapshot.Target of the app, in lower case.
func (s scope) target() string {
	return strings.ToLower(fmt.Sprintf("azure:containerapp/%s/%s/%s", s.subscription, s.resourceGroup, s.app))
}

// link returns the link to the app in the Azure portal.
func (s scope) link() action.Link {
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", s.subscription, s.resourceGroup, s.app)

	return action.Link{Title: "Container app " + s.app + " in the Azure portal", URL: portalURL + "/#resource" + id}
}

// restored is the result of an undo. latest says traffic follows the latest
// revision again.
func restored(s scope, revs *action.Revisions, latest bool) action.Result {
	text := fmt.Sprintf("Moved all traffic of container app %s back to revision %s.", s.app, revs.Running.ID)

	if latest {
		text += " Traffic follows the latest revision again."
	}

	return action.Result{Summary: text, Links: []action.Link{s.link()}}
}

// pickOne returns the value the model gave, as the config spells it, or the
// only entry of allowed when the model gave none.
func pickOne(key, given string, allowed []string) (string, error) {
	if given == "" {
		if len(allowed) == 1 {
			return allowed[0], nil
		}

		return "", fmt.Errorf("%w: %s: name one of limits.%ss", action.ErrParams, key, key)
	}

	for _, a := range allowed {
		if strings.EqualFold(a, given) {
			return a, nil
		}
	}

	return "", fmt.Errorf("%s %q is not one of limits.%ss: %w", key, given, key, action.ErrLimit)
}

// rollbackSnapshot checks that snap is the snapshot of a rollback of the
// app, and returns it with its two revisions. The error wraps
// action.ErrPermanent, because a later attempt reads the same snapshot.
func rollbackSnapshot(snap *action.Snapshot, s scope) (action.Snapshot, *action.Revisions, error) {
	if snap == nil {
		return action.Snapshot{}, nil, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if snap.Target != s.target() {
		return action.Snapshot{}, nil, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	revs, err := snap.Revisions()
	if err != nil {
		return action.Snapshot{}, nil, fmt.Errorf("%w: read the revisions: %w", action.ErrPermanent, err)
	}

	if revs == nil {
		return action.Snapshot{}, nil, fmt.Errorf("%w: %w", action.ErrPermanent, errNotRollback)
	}

	return *snap, revs, nil
}

// runningOrNil returns a pointer to run, or nil when err says the running
// revision could not be told.
func runningOrNil(run running, err error) *running {
	if err != nil {
		return nil
	}

	return &run
}

// liveDocument returns the revision document that Undo shows an approver
// as the live state: the revision that runs, or a note that traffic is not
// on one revision. It is nil when it cannot be made.
func liveDocument(s scope, l live, run running, runErr error) json.RawMessage {
	rev := action.Revision{Kind: revisionKind, ID: "none, traffic is not on one revision", Notes: l.trafficText()}

	if runErr == nil {
		notes := notesByName
		if run.latest {
			notes = notesFollowsLatest
		}

		rev = describe(l.revision(run.name), notes)
	}

	snap, err := action.RevisionSnapshot(s.target(), "", rev, rev)
	if err != nil {
		return nil
	}

	return snap.Before
}
