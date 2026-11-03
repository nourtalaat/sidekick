// Package argocdrollback is the action argocd.rollback: it puts an Argo CD
// application back on an earlier deployed revision through Argo CD's
// rollback API, after an approver approved it, and it can undo its own
// rollback. It is approval-only: it embeds action.ReadWriteGate, so a route
// in read-write mode may list it only when the config sets allowReadWrite:
// true. It is a rollback action: it embeds action.RevisionLimit, and its
// snapshot holds the two revisions an approver reads. docs/plugins/actions.md
// explains both.
//
// Argo CD refuses a rollback while auto-sync is on, and the next sync would
// undo one anyway, so the action refuses then and says to propose a revert
// pull request. It never changes the sync policy.
//
// Undo is a second rollback, to the history entry that ran before the first
// one. Like every rollback it re-applies the history entry's own source, so
// Helm parameters and paths are the ones that ran then, and it skips hooks,
// so no PreSync job of the older revision runs again. It needs no more
// permission than the rollback.
//
// What the action relies on from Argo CD, as its public source at the tag
// v3.5.5 (github.com/argoproj/argo-cd) shows it. The calls and the history
// are described in internal/argocd/doc.go; the rest is:
//
//   - A rollback needs the RBAC action "applications, sync" on the
//     application, and reading it needs "applications, get". The action needs
//     no other permission, and in particular not "override", which only a sync
//     to a revision other than the target of the application can need
//     (Rollback and resolveSourceRevisions in server/application/application.go,
//     docs/operator-manual/rbac.md).
//   - Auto-sync is on when spec.syncPolicy.automated is set and its enabled
//     field is not false (SyncPolicy.IsAutomatedSyncEnabled in
//     pkg/apis/application/v1alpha1/types.go).
//   - After a rollback the application is out of sync with the target
//     revision of its spec until someone syncs it or changes the target,
//     because a refresh compares the live state with spec.source.targetRevision
//     (processAppRefreshQueueItem in controller/appcontroller.go).
//   - status.summary.images lists the images of the live resources, so it
//     describes the running revision only. A history entry keeps no images
//     (RevisionHistory in pkg/apis/application/v1alpha1/types.go).
//   - Argo CD gives no precondition to the rollback call, so a deploy between
//     the action's last read and the call is not detected.
//   - Whether Argo CD rolls an application that uses the source hydrator back
//     as the history says is not confirmed, so the action refuses it.
package argocdrollback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: argocd.rollback".
const Type = "argocd.rollback"

// How the action waits for the operation Argo CD starts. Execute has five
// minutes in all, and a run that does not finish is tried again, so one
// attempt waits well inside that.
const (
	defaultPollInterval = 2 * time.Second
	defaultWaitLimit    = 3 * time.Minute
)

// versionSeparator joins the ID and the revision of the version of a
// snapshot, "12:3f2a...".
const versionSeparator = ":"

// anotherOperation is the part of Argo CD's message for a call that meets an
// operation that runs already.
const anotherOperation = "another operation is already in progress"

// refuseAutoSync is what the model reads when auto-sync is on.
const refuseAutoSync = "Argo CD would sync the application back, and refuses a rollback while auto-sync is on. " +
	"Propose a revert pull request with `github.revert` or `gitlab.revert` instead."

var (
	errNoSnapshot   = errors.New("the proposal has no rollback snapshot")
	errTargetDiffer = errors.New("the snapshot is of another application")
)

// paramsSchemaFormat is the JSON Schema of Params. Its argument is the
// highest value of revisionsBack the config allows.
const paramsSchemaFormat = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["application"],
  "properties": {
    "application": {"type": "string", "minLength": 3, "maxLength": 507, "description": "The application as project/name, for example shop/checkout."},
    "revisionsBack": {"type": "integer", "minimum": 1, "maximum": %d, "description": "How many revisions back to go. 1, the default, is the revision deployed before the one that runs now."}
  }
}`

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	Application string `json:"application"`
	// RevisionsBack is nil for the default, 1.
	RevisionsBack *int `json:"revisionsBack,omitempty"`
}

// Rollback is one configured argocd.rollback action. It embeds the gate and
// the revision limit, so the built instance reports that it is approval-only
// and how far back it may go.
type Rollback struct {
	action.ReadWriteGate
	action.RevisionLimit

	name         string
	applications argocd.Allowlist
	schema       json.RawMessage
	client       *argocd.Client
	// pollEvery and waitFor are how often the action reads the application
	// while its operation runs, and for how long. Tests shorten them.
	pollEvery time.Duration
	waitFor   time.Duration
}

var (
	_ action.Action      = (*Rollback)(nil)
	_ action.Snapshotter = (*Rollback)(nil)
	_ action.Undoer      = (*Rollback)(nil)
)

// New builds the action from its validated config. It does not call Argo CD:
// a server that is down must not stop Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	client, err := argocd.New(argocd.Options{
		BaseURL:     cfg.BaseURL,
		TokenSecret: cfg.TokenSecret,
		Namespace:   cfg.Namespace,
		HTTP:        deps.HTTP,
		Secrets:     deps.Secrets,
	})
	if err != nil {
		return nil, fmt.Errorf("build the Argo CD client: %w", err)
	}

	return &Rollback{
		ReadWriteGate: cfg.ReadWriteGate,
		RevisionLimit: cfg.RevisionLimit,
		name:          name,
		applications:  cfg.Limits.Applications,
		schema:        json.RawMessage(fmt.Sprintf(paramsSchemaFormat, cfg.Max())),
		client:        client,
		pollEvery:     defaultPollInterval,
		waitFor:       defaultWaitLimit,
	}, nil
}

// Spec implements action.Action. The description tells the model which
// applications it may roll back and how far.
func (a *Rollback) Spec() action.Spec {
	return action.Spec{
		Name: a.name,
		Description: "Rolls an Argo CD application back to an earlier deployed revision, through Argo CD's rollback API. " +
			"It deploys that revision's own source and does not prune. It refuses an application with auto-sync on " +
			"(propose a revert pull request for that), one with several sources, and one with an operation running. " +
			"Read the application's deployment history first. Allowed applications: " + a.applications.String() +
			". Up to " + strconv.Itoa(a.Max()) + " revisions back, counting revisions that differ.",
		ParamsSchema: a.schema,
	}
}

// Validate implements action.Action. It runs without calling Argo CD, so it
// checks what the parameters alone show against the limits. A limit
// violation wraps action.ErrLimit.
func (a *Rollback) Validate(_ context.Context, raw json.RawMessage) error {
	if _, _, err := a.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the application, checks
// that a rollback is possible and finds the two revisions, without changing
// anything.
func (a *Rollback) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	ref, back, err := a.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	app, err := a.client.GetApplication(ctx, ref)
	if se, ok := errors.AsType[*argocd.StatusError](err); ok && (se.Code == http.StatusNotFound || se.Code == http.StatusForbidden) {
		return action.Snapshot{}, refusal("Argo CD has no application %s, or the token may not get it.", ref)
	}

	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the application: %w", err)
	}

	if err := checkSupported(app, ref); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the application: %w", err)
	}

	if app.OperationRunning() {
		return action.Snapshot{}, refusal("An operation is running on application %s (phase %s). Wait for it to finish, then propose again.",
			ref, operationPhase(app))
	}

	found, err := plan(app, ref, back)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("find the revision to go back to: %w", err)
	}

	snap, err := action.RevisionSnapshot(targetName(ref), versionOf(found.running),
		revisionOf(found.running, imagesNote(app)), revisionOf(found.goesBackTo, ""))
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It checks the parameters again, reads the
// application, and asks Argo CD to roll back to the history entry of the
// snapshot, unless Argo CD deployed since the snapshot (action.ErrConflict).
// When the rollback was made already, which happens when a worker died after
// the call, it asks for nothing and succeeds. It waits for the operation to
// end, within one attempt's limit, and an operation that has not ended is a
// failure that a later attempt picks up again.
func (a *Rollback) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	ref, _, err := a.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	m, err := moveFromSnapshot(inv.Snapshot, ref)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	res, err := a.run(ctx, m)
	if err != nil {
		return action.Result{}, fmt.Errorf("roll back %s: %w", ref, err)
	}

	return res, nil
}

// Undo implements action.Undoer. It rolls the application back to the
// history entry that ran before the rollback, which is the Running revision
// of snap. When the application runs that revision again already, it asks for
// nothing and succeeds. When the newest deployment is not the one the
// rollback made, and force is false, it asks for nothing and returns an
// *action.ConflictError with the revision the application runs now.
func (a *Rollback) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	ref, err := a.decode(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	m, err := undoFromSnapshot(snap, ref, force)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	res, err := a.run(ctx, m)
	if err != nil {
		return action.Result{}, fmt.Errorf("undo the rollback of %s: %w", ref, err)
	}

	return res, nil
}

// parse decodes the parameters and checks them against the limits: the
// application is one limits.applications allows, and revisionsBack is within
// maxRevisionsBack.
func (a *Rollback) parse(raw json.RawMessage) (argocd.AppRef, int, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return argocd.AppRef{}, 0, fmt.Errorf("decode the parameters: %w", err)
	}

	ref, err := a.reach(p.Application)
	if err != nil {
		return argocd.AppRef{}, 0, fmt.Errorf("check the application: %w", err)
	}

	back := 1
	if p.RevisionsBack != nil {
		back = *p.RevisionsBack
	}

	if err := a.CheckRequest(back); err != nil {
		return argocd.AppRef{}, 0, fmt.Errorf("check revisionsBack: %w", err)
	}

	return ref, back, nil
}

// decode decodes the parameters and checks the application against the
// limits. Undo uses it alone, because an undo puts back a change that was
// allowed when it was made.
func (a *Rollback) decode(raw json.RawMessage) (argocd.AppRef, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return argocd.AppRef{}, fmt.Errorf("decode the parameters: %w", err)
	}

	ref, err := a.reach(p.Application)
	if err != nil {
		return argocd.AppRef{}, fmt.Errorf("check the application: %w", err)
	}

	return ref, nil
}

// reach checks that text is project/name and that limits.applications lists
// it.
func (a *Rollback) reach(text string) (argocd.AppRef, error) {
	ref, err := argocd.ParseAppRef(text)
	if err != nil {
		return argocd.AppRef{}, fmt.Errorf("%w: %w", action.ErrParams, err)
	}

	if !a.applications.Allows(ref) {
		return argocd.AppRef{}, refusal("application %q is not one the action may roll back; allowed: %s", ref, a.applications)
	}

	return ref, nil
}

// checkSupported refuses an application that the action does not roll back:
// auto-sync is on, it has several sources, or it uses the source hydrator.
// The refusal wraps action.ErrLimit.
func checkSupported(app *argocd.Application, ref argocd.AppRef) error {
	switch {
	case app.AutoSyncEnabled():
		return refusal("%s", refuseAutoSync)
	case app.MultiSource():
		return errSeveralSources(ref)
	case app.Spec.SourceHydrator != nil:
		return refusal("Application %s uses the source hydrator, and this action does not roll those back. "+
			"Propose a revert pull request, or roll it back in Argo CD.", ref)
	default:
		return nil
	}
}

// errSeveralSources is the refusal for an application with several sources.
func errSeveralSources(ref argocd.AppRef) error {
	return refusal("Application %s has several sources, and this action rolls back applications with one source only. "+
		"Propose a revert pull request, or roll it back in Argo CD.", ref)
}

// operationPhase is the phase of the operation that is requested or running:
// "Pending" for one the controller has not started.
func operationPhase(app *argocd.Application) string {
	if s := app.Status.OperationState; s != nil && !s.Completed() {
		return s.Phase
	}

	return "Pending"
}

// targetName names the application, as Snapshot.Target.
func targetName(ref argocd.AppRef) string {
	return "argocd:application/" + ref.String()
}

// refusal is an error that says, for the model to read, what the action will
// not do, and wraps action.ErrLimit so that Sidekick ends the proposal at
// once.
func refusal(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), action.ErrLimit)
}
