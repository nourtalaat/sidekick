// Package awsecs is the action aws.ecs_rollback: it puts an Amazon ECS
// service back on an earlier revision of its task definition through the ECS
// API, after an approver approved it, and it can put the service back on the
// task definition it ran before. It is a rollback action, so it is
// approval-only: it embeds action.ReadWriteGate, and a route in read-write
// mode may list it only when the config sets allowReadWrite: true. It limits
// how many revisions back the model may ask for with action.RevisionLimit,
// and its snapshot holds the two task definitions as revision documents.
// docs/plugins/actions.md explains the steps, and internal/action/datadog is
// its model. The account and Region config and the credentials are those of
// internal/action/awsaction, which every AWS action shares.
//
// What the action relies on from ECS, as the public source of the AWS SDK
// for Go v2 at the tag service/ecs/v1.102.0 shows (github.com/aws/aws-sdk-go-v2,
// directory service/ecs, and the API model codegen/sdk-codegen/aws-models/ecs.json):
//
//   - DescribeServices returns the service's status, its deployments (each
//     with an ID, a status and the ARN of its task definition) and its
//     deploymentController (types/types.go, Service and Deployment). The
//     deployment with status PRIMARY is the most recent one. The
//     CreateService output text says that a service on the ECS controller
//     has no deploymentController in the answer, so none means ECS
//     (api_op_CreateService.go, CreateServiceOutput). A service on the
//     CODE_DEPLOY controller has an empty deployments list.
//   - DescribeTaskDefinition takes a family:revision and returns its status
//     (ACTIVE, INACTIVE or DELETE_IN_PROGRESS), registeredAt, registeredBy
//     ("the principal that registered the task definition") and the image of
//     each container (types/types.go, TaskDefinition and ContainerDefinition).
//     An INACTIVE task definition can be described only while a task or
//     service uses it (api_op_DescribeTaskDefinition.go).
//   - ListTaskDefinitions takes familyPrefix, status (default ACTIVE only),
//     sort (ASC or DESC, by family name and then by revision number, newest
//     revision first for DESC) and a page size of 1 to 100, and returns task
//     definition ARNs only, in pages (api_op_ListTaskDefinitions.go). The
//     text calls familyPrefix "the full family name" while its name says
//     prefix, so the action keeps only the ARNs of exactly the family.
//   - A revision number always increases by one, also after a revision is
//     deregistered (types/types.go, TaskDefinition.Revision). Numbers follow
//     the order of registration, which is not the order of deployment.
//   - UpdateService takes a task definition as family:revision or an ARN and
//     starts a new deployment for it. On the ECS controller it can change
//     the task definition; on CODE_DEPLOY and EXTERNAL it cannot
//     (api_op_UpdateService.go). It has no version, ETag or client token, so
//     it has no precondition. Its answer is the whole service, with the new
//     deployments.
//   - UpdateService and DescribeServices act on the resource type service,
//     arn:<partition>:ecs:<region>:<account>:service/<cluster>/<service>;
//     DescribeTaskDefinition and ListTaskDefinitions have no resource type
//     (the aws.iam#iamAction traits of the model). Whether UpdateService also
//     needs iam:PassRole is not in the model: the AWS Service Reference
//     lists it for the call, with no word on when it is enforced, so the
//     docs give it as an optional statement.
//   - The errors of UpdateService that a retry cannot fix (a missing or
//     inactive service or cluster, a parameter ECS rejects, a refusal by
//     policy) are client errors of the model.
package awsecs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: aws.ecs_rollback".
const Type = "aws.ecs_rollback"

var (
	errNoSnapshot   = refuse("the proposal has no snapshot")
	errTargetDiffer = refuse("the snapshot is of another service")
	errNotRollback  = refuse("the proposal has no task definition revisions to restore")
)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Rollback is one configured aws.ecs_rollback action. It embeds the gate, so
// the built instance reports that it is approval-only, and the revision
// limit.
type Rollback struct {
	action.ReadWriteGate
	action.RevisionLimit

	name     string
	limits   Limits
	clients  *awsaction.Clients
	schema   json.RawMessage
	accounts []string
	regions  []string
	// endpoint replaces the ECS address of the Region. Tests set it.
	endpoint string
}

var (
	_ action.Action      = (*Rollback)(nil)
	_ action.Snapshotter = (*Rollback)(nil)
	_ action.Undoer      = (*Rollback)(nil)
)

// buildOptions are what a test changes in how the action is built.
type buildOptions struct {
	aws      []awsaction.Option
	endpoint string
}

// New builds the action from its validated config. It does not call AWS:
// credentials are read, and the role assumed, by the first call.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	r, err := build(ctx, name, cfg, deps.HTTP, buildOptions{})
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", name, err)
	}

	return r, nil
}

// build is New with the HTTP client and the options to use.
func build(ctx context.Context, name string, cfg Config, httpClient *http.Client, opts buildOptions) (*Rollback, error) {
	clients, err := awsaction.New(ctx, cfg.Config, httpClient, opts.aws...)
	if err != nil {
		return nil, fmt.Errorf("set up the AWS clients: %w", err)
	}

	r := &Rollback{
		ReadWriteGate: cfg.ReadWriteGate,
		RevisionLimit: cfg.RevisionLimit,
		name:          name,
		limits:        cfg.Limits,
		clients:       clients,
		schema:        paramsSchema(cfg.Max()),
		regions:       cfg.Regions,
		endpoint:      opts.endpoint,
	}

	for _, a := range cfg.Accounts {
		r.accounts = append(r.accounts, a.ID)
	}

	return r, nil
}

// Spec implements action.Action. The description tells the model which
// services it may roll back and how.
func (r *Rollback) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Puts an Amazon ECS service back on an earlier revision of its task definition, through the ECS API. " +
		"It changes only the task definition: the service starts a new deployment of the older revision. " +
		"The older revision is the highest ACTIVE revision of the same task definition family that is below the one the service runs, " +
		"or the one revisionsBack such revisions below. That revision may never have been deployed: check its image in the proposal. " +
		"It works only for services on the ECS deployment controller. ")
	fmt.Fprintf(&b, "Accounts: %s. Regions: %s. Clusters: %s. Services: %s. ",
		strings.Join(r.accounts, ", "), strings.Join(r.regions, ", "), strings.Join(r.limits.Clusters, ", "), strings.Join(r.limits.Services, ", "))
	fmt.Fprintf(&b, "revisionsBack is at most %d.", r.Max())

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: r.schema}
}

// Validate implements action.Action. It runs without calling AWS, so it
// checks what the parameters alone show against the limits. A limit
// violation wraps action.ErrLimit.
func (r *Rollback) Validate(_ context.Context, raw json.RawMessage) error {
	if _, _, err := r.parse(raw); err != nil {
		return fmt.Errorf("validate the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the service and its task
// definitions, checks them against the limits and the rules of a rollback,
// and returns the snapshot with the running task definition as Before and
// the one it goes back to as After, without changing anything.
func (r *Rollback) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, pos, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	client, err := r.client(ctx, pos)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the ECS client: %w", err)
	}

	st, err := readService(ctx, client, pos)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the service: %w", err)
	}

	family, running, _ := parseTaskDefinition(st.taskDefinition)

	revision, err := earlier(ctx, client, family, running, p.back())
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("find the task definition to go back to: %w", err)
	}

	runningTD, err := describeTaskDefinition(ctx, client, st.taskDefinition)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the running task definition: %w", err)
	}

	targetTD, err := activeTaskDefinition(ctx, client, taskDefinitionID(family, revision), "the service cannot be put on it")
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the task definition to go back to: %w", err)
	}

	snap, err := action.RevisionSnapshot(pos.name(), versionOf(st), revisionOf(runningTD), revisionOf(targetTD))
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the service, and puts it on the task definition of the snapshot, unless the
// service changed since the snapshot (action.ErrConflict). When the service
// already runs that task definition, which happens when a worker died after
// the write, it writes nothing and succeeds. ECS has no precondition on
// UpdateService, so a deploy in the moment between the read and the write is
// not noticed. It does not wait for the rollout.
func (r *Rollback) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	_, pos, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	revs, err := rollbackOf(inv.Snapshot, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	client, err := r.client(ctx, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("make the ECS client: %w", err)
	}

	st, err := readService(ctx, client, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the service: %w", err)
	}

	if st.taskDefinition == revs.GoesBackTo.ID {
		return unchanged(pos, st), nil
	}

	if st.taskDefinition != revs.Running.ID || versionOf(st) != inv.Snapshot.Version {
		return action.Result{}, changedSince(pos, inv.Snapshot.Version, st)
	}

	deployment, err := updateService(ctx, client, pos, revs.GoesBackTo.ID)
	if err != nil {
		return action.Result{}, fmt.Errorf("write the rollback: %w", err)
	}

	return done(pos, revs.GoesBackTo.ID, revs.Running.ID, deployment), nil
}

// Undo implements action.Undoer. It puts the service back on the task
// definition that ran before the rollback, which is Before of the snapshot.
// When the service runs it already, it writes nothing and succeeds. When the
// service is not on the task definition the rollback set, and force is
// false, it writes nothing and returns an *action.ConflictError with the
// task definition the service runs now.
func (r *Rollback) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	_, pos, err := r.decode(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	revs, err := rollbackOf(&snap, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	client, err := r.client(ctx, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("make the ECS client: %w", err)
	}

	st, err := readService(ctx, client, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the service: %w", err)
	}

	if st.taskDefinition == revs.Running.ID {
		return unchanged(pos, st), nil
	}

	if st.taskDefinition != revs.GoesBackTo.ID && !force {
		return action.Result{}, r.liveConflict(ctx, client, pos, st)
	}

	if _, err := activeTaskDefinition(ctx, client, revs.Running.ID, "Undo cannot put the service back on it"); err != nil {
		return action.Result{}, fmt.Errorf("read the task definition to restore: %w", err)
	}

	deployment, err := updateService(ctx, client, pos, revs.Running.ID)
	if err != nil {
		return action.Result{}, fmt.Errorf("write the restore: %w", err)
	}

	return done(pos, revs.Running.ID, st.taskDefinition, deployment), nil
}

// rollbackOf returns the two revisions of the snapshot of a rollback of
// the service at pos. It fails, for good, when there is no snapshot, when it
// is of another service or when it is not a rollback.
func rollbackOf(snap *action.Snapshot, pos position) (*action.Revisions, error) {
	switch {
	case snap == nil:
		return nil, errNoSnapshot
	case snap.Target != pos.name():
		return nil, errTargetDiffer
	}

	revs, err := snap.Revisions()
	if err != nil {
		return nil, refuse("the snapshot cannot be read as two task definitions: %v", err)
	}

	if revs == nil {
		return nil, errNotRollback
	}

	return revs, nil
}

// liveConflict is the error of an Undo that finds the service on another
// task definition than the rollback set. It carries the task definition the
// service runs now, as a revision document, for the approver to compare
// with.
func (r *Rollback) liveConflict(ctx context.Context, client *ecs.Client, pos position, st state) error {
	live := action.Revision{Kind: revisionKind, ID: st.taskDefinition}

	if td, err := describeTaskDefinition(ctx, client, st.taskDefinition); err == nil {
		live = revisionOf(td)
	}

	snap, err := action.RevisionSnapshot(pos.name(), "", live, live)
	if err != nil {
		return fmt.Errorf("make the live revision: %w", err)
	}

	return &action.ConflictError{Live: snap.Before}
}

// changedSince is the conflict error of an Execute that finds the service
// not as it was when the rollback was proposed.
func changedSince(pos position, version string, now state) error {
	was := state{}
	was.deploymentID, was.taskDefinition = splitVersion(version)

	return fmt.Errorf("service %s in cluster %s changed after the proposal (it ran %s in deployment %s, now it runs %s in deployment %s): %w",
		pos.service, pos.cluster, was.taskDefinition, was.deploymentID, now.taskDefinition, now.deploymentID, action.ErrConflict)
}

// done is the result of a write: the service is on task definition to, which
// it did not run before.
func done(pos position, to, from, deployment string) action.Result {
	text := fmt.Sprintf("Put service %s of cluster %s back on task definition %s (it ran %s). ", pos.service, pos.cluster, to, from)

	if deployment != "" {
		text += "The new deployment is " + deployment + ". "
	} else {
		text += "ECS did not report the new deployment. "
	}

	text += "Sidekick does not wait for the rollout; the watcher reports whether the alert recovers."

	return action.Result{Summary: text, Links: []action.Link{serviceLink(pos)}}
}

// unchanged is the result when the service runs the task definition the
// action was to put it on, so nothing was written.
func unchanged(pos position, st state) action.Result {
	return action.Result{
		Summary: fmt.Sprintf("Service %s of cluster %s already runs task definition %s in deployment %s, so nothing was changed.",
			pos.service, pos.cluster, st.taskDefinition, st.deploymentID),
		Links: []action.Link{serviceLink(pos)},
	}
}
