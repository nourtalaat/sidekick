// Package awslambda is the action aws.lambda_rollback: it points an AWS Lambda
// alias back at the published version it served before, through the Lambda
// API, after an approver approved it, and it can point the alias at the
// version and traffic split it had before. It is a rollback action, so it is
// approval-only: it embeds action.ReadWriteGate, and a route in read-write
// mode may list it only when the config sets allowReadWrite: true. It limits
// how many versions back the model may ask for with action.RevisionLimit, and
// its snapshot holds the two versions as revision documents.
// docs/plugins/actions.md explains the steps, and internal/action/datadog is
// its model. The account and Region config and the credentials are those of
// internal/action/awsaction, which every AWS action shares.
//
// What the action relies on from Lambda, as the public source of the AWS SDK
// for Go v2 at the tag service/lambda/v1.113.3 shows (github.com/aws/aws-sdk-go-v2,
// directory service/lambda, and the API model
// codegen/sdk-codegen/aws-models/lambda.json at that tag):
//
//   - GetAlias takes a function name and an alias name and returns the alias's
//     FunctionVersion, its RoutingConfig (AdditionalVersionWeights, a map from
//     a version number to its share of the traffic) and its RevisionId, "a
//     unique identifier that changes when you update the alias"
//     (api_op_GetAlias.go, types AliasConfiguration).
//   - UpdateAlias takes FunctionVersion, RoutingConfig and RevisionId, and
//     "only updates the alias if the revision ID matches the ID that's
//     specified". When it does not match, Lambda answers
//     PreconditionFailedException, a client error with HTTP status 412
//     (api_op_UpdateAlias.go, types/errors.go, the model). The model does not
//     say that an empty AdditionalVersionWeights clears a split. The action
//     sends an empty map, which the SDK writes as {}, and checks the answer,
//     which names the version and split the alias now has, and fails for good
//     when a split is left.
//   - ListVersionsByFunction takes Marker and MaxItems (at most 50 in a page)
//     and returns each version's configuration and NextMarker. It lists
//     $LATEST and the published versions, whose names are numbers, and the
//     model says nothing of their order, so the action reads every page and
//     sorts the numbers itself (api_op_ListVersionsByFunction.go).
//   - GetFunctionConfiguration with Qualifier set to a version number returns
//     that version's CodeSha256 (base64), Runtime, PackageType (Zip or
//     Image), Description and LastModified, written like
//     2020-04-10T19:06:32.563+0000 (api_op_GetFunctionConfiguration.go,
//     types FunctionConfiguration). For an image it does not give the image
//     address. Lambda does not record who published a version.
//   - ResourceNotFoundException and InvalidParameterValueException are client
//     errors that a retry cannot fix. ResourceConflictException (status 409,
//     "another operation is in progress") is also a client error, but a retry
//     can pass, so the action does not call it permanent.
//   - GetAlias, UpdateAlias, ListVersionsByFunction and GetFunctionConfiguration
//     each list only the resource type function, with the ARN
//     arn:<partition>:lambda:<region>:<account>:function:<name>, in the AWS
//     Service Authorization Reference for Lambda ("Actions, resources, and
//     condition keys for AWS Lambda", also served as
//     servicereference.us-east-1.amazonaws.com/v1/lambda/lambda.json).
package awslambda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: aws.lambda_rollback".
const Type = "aws.lambda_rollback"

var (
	errNoSnapshot   = awsaction.Refuse("the proposal has no snapshot")
	errTargetDiffer = awsaction.Refuse("the snapshot is of another alias")
	errNotRollback  = awsaction.Refuse("the proposal has no function versions to restore")
)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Rollback is one configured aws.lambda_rollback action. It embeds the gate,
// so the built instance reports that it is approval-only, and the revision
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
	// endpoint replaces the Lambda address of the Region. Tests set it.
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
// aliases it may roll back and how.
func (r *Rollback) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Points an AWS Lambda alias back at an earlier published version of its function, through the Lambda API. " +
		"It changes only the alias, never $LATEST. " +
		"The earlier version is the highest published version below the one the alias serves, or the one revisionsBack such versions below. " +
		"That version may never have served traffic: check its code in the proposal. " +
		"An alias that splits traffic between two versions ends up serving the earlier version alone: the split is cleared, and Undo restores it. ")
	fmt.Fprintf(&b, "Accounts: %s. Regions: %s. Functions: %s. Aliases: %s. ",
		strings.Join(r.accounts, ", "), strings.Join(r.regions, ", "), strings.Join(r.limits.Functions, ", "), strings.Join(r.limits.Aliases, ", "))
	fmt.Fprintf(&b, "revisionsBack is at most %d.", r.Max())

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: r.schema}
}

// Validate implements action.Action. It runs without calling AWS, so it checks
// what the parameters alone show against the limits. A limit violation wraps
// action.ErrLimit.
func (r *Rollback) Validate(_ context.Context, raw json.RawMessage) error {
	if _, _, err := r.parse(raw); err != nil {
		return fmt.Errorf("validate the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the alias and the versions
// of the function, checks them against the limits and the rules of a
// rollback, and returns the snapshot with the version the alias serves as
// Before and the one it goes back to as After, without changing anything. The
// snapshot's Version holds what Undo and Execute need: the alias's RevisionId
// and its traffic split.
func (r *Rollback) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, pos, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	client, err := r.client(ctx, pos)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the Lambda client: %w", err)
	}

	st, err := readAlias(ctx, client, pos)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the alias: %w", err)
	}

	running, err := publishedVersion(st.version, pos)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("check the version the alias serves: %w", err)
	}

	target, err := earlier(ctx, client, pos, running, p.back())
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("find the version to go back to: %w", err)
	}

	runningCfg, err := describeVersion(ctx, client, pos, st.version)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the version the alias serves: %w", err)
	}

	targetID := fmt.Sprint(target)

	targetCfg, err := describeVersion(ctx, client, pos, targetID)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the version to go back to: %w", err)
	}

	version, err := encodeRestore(st)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the restore data: %w", err)
	}

	snap, err := action.RevisionSnapshot(pos.name(), version, revisionOf(st.version, runningCfg, splitNote(st)), revisionOf(targetID, targetCfg, ""))
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It checks the parameters again, reads the
// alias, and points it at the version of the snapshot, unless the alias
// changed since the snapshot (action.ErrConflict). When the alias already
// serves that version with no split, which happens when a worker died after
// the write, it writes nothing and succeeds. The write carries the alias's
// RevisionId, so Lambda refuses it if the alias changed after the read. It
// does not wait for anything: the alias change is immediate.
func (r *Rollback) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	_, pos, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	revs, restore, err := rollbackOf(inv.Snapshot, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	client, err := r.client(ctx, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("make the Lambda client: %w", err)
	}

	st, err := readAlias(ctx, client, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the alias: %w", err)
	}

	if st.sameRouting(revs.GoesBackTo.ID, nil) {
		return unchanged(pos, st), nil
	}

	if st.revisionID != restore.RevisionID || !st.sameRouting(revs.Running.ID, restore.Weights) {
		return action.Result{}, changedSince(pos, revs, st)
	}

	if err := updateAlias(ctx, client, pos, revs.GoesBackTo.ID, nil, st.revisionID); err != nil {
		return action.Result{}, fmt.Errorf("write the rollback: %w", err)
	}

	return done(pos, revs.GoesBackTo.ID, st), nil
}

// Undo implements action.Undoer. It points the alias back at the version and
// traffic split it had before the rollback, which are Before and the restore
// data of the snapshot. When the alias has them already, it writes nothing
// and succeeds. When the alias is not as the rollback left it, and force is
// false, it writes nothing and returns an *action.ConflictError with the
// version the alias serves now. With force it writes anyway, with the
// RevisionId it has just read.
func (r *Rollback) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	_, pos, err := r.decode(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	revs, restore, err := rollbackOf(&snap, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	client, err := r.client(ctx, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("make the Lambda client: %w", err)
	}

	st, err := readAlias(ctx, client, pos)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the alias: %w", err)
	}

	if st.sameRouting(revs.Running.ID, restore.Weights) {
		return unchanged(pos, st), nil
	}

	if !st.sameRouting(revs.GoesBackTo.ID, nil) && !force {
		return action.Result{}, r.liveConflict(ctx, client, pos, st)
	}

	if _, err := describeVersion(ctx, client, pos, revs.Running.ID); err != nil {
		return action.Result{}, fmt.Errorf("read the version to restore: %w", err)
	}

	if err := updateAlias(ctx, client, pos, revs.Running.ID, restore.Weights, st.revisionID); err != nil {
		return action.Result{}, r.undoWriteError(ctx, client, pos, err)
	}

	return done(pos, revs.Running.ID, st), nil
}

// rollbackOf returns the two revisions and the restore data of the snapshot
// of a rollback of the alias at pos. It fails, for good, when there is no
// snapshot, when it is of another alias, when it is not a rollback, or when a
// version in it is not a published version number.
func rollbackOf(snap *action.Snapshot, pos position) (*action.Revisions, restoreData, error) {
	switch {
	case snap == nil:
		return nil, restoreData{}, errNoSnapshot
	case snap.Target != pos.name():
		return nil, restoreData{}, errTargetDiffer
	}

	revs, err := snap.Revisions()
	if err != nil {
		return nil, restoreData{}, awsaction.Refuse("the snapshot cannot be read as two function versions: %v", err)
	}

	if revs == nil {
		return nil, restoreData{}, errNotRollback
	}

	for _, id := range []string{revs.Running.ID, revs.GoesBackTo.ID} {
		if _, err := publishedVersion(id, pos); err != nil {
			return nil, restoreData{}, fmt.Errorf("check the versions of the snapshot: %w", err)
		}
	}

	restore, err := decodeRestore(snap.Version)
	if err != nil {
		return nil, restoreData{}, fmt.Errorf("read the alias data of the snapshot: %w", err)
	}

	return revs, restore, nil
}

// undoWriteError is the error of an Undo whose write failed. A refused
// RevisionId, which means the alias changed in the moment since Undo read it,
// is a conflict like any other, with the alias as it is now.
func (r *Rollback) undoWriteError(ctx context.Context, client *lambda.Client, pos position, err error) error {
	if !isConflict(err) {
		return fmt.Errorf("write the restore: %w", err)
	}

	st, readErr := readAlias(ctx, client, pos)
	if readErr != nil {
		return fmt.Errorf("write the restore: %w", err)
	}

	return r.liveConflict(ctx, client, pos, st)
}

// liveConflict is the error of an Undo that finds the alias not as the
// rollback left it. It carries the version the alias serves now, as a
// revision document, for the approver to compare with.
func (r *Rollback) liveConflict(ctx context.Context, client *lambda.Client, pos position, st aliasState) error {
	live := action.Revision{Kind: revisionKind, ID: st.version, Notes: splitNote(st)}

	if cfg, err := describeVersion(ctx, client, pos, st.version); err == nil {
		live = revisionOf(st.version, cfg, splitNote(st))
	}

	snap, err := action.RevisionSnapshot(pos.name(), "", live, live)
	if err != nil {
		return fmt.Errorf("make the live revision: %w", err)
	}

	return &action.ConflictError{Live: snap.Before}
}

// changedSince is the conflict error of an Execute that finds the alias not
// as it was when the rollback was proposed.
func changedSince(pos position, revs *action.Revisions, now aliasState) error {
	return fmt.Errorf("alias %s of function %s changed after the proposal (it served version %s, now it serves version %s%s): %w",
		pos.alias, pos.function, revs.Running.ID, now.version, splitText(now), action.ErrConflict)
}

// splitText says in a few words that an alias splits traffic, for an error
// message.
func splitText(st aliasState) string {
	if !st.split() {
		return ""
	}

	return " with " + shares(st)
}

// done is the result of a write: the alias serves version to, which it did
// not serve alone before.
func done(pos position, to string, was aliasState) action.Result {
	return action.Result{
		Summary: fmt.Sprintf("Pointed alias %s of function %s at version %s (it served version %s%s). The change is live now.",
			pos.alias, pos.function, to, was.version, splitText(was)),
		Links: []action.Link{aliasLink(pos)},
	}
}

// unchanged is the result when the alias is as the action was to leave it, so
// nothing was written.
func unchanged(pos position, st aliasState) action.Result {
	return action.Result{
		Summary: fmt.Sprintf("Alias %s of function %s already serves version %s%s, so nothing was changed.", pos.alias, pos.function, st.version, splitText(st)),
		Links:   []action.Link{aliasLink(pos)},
	}
}

// isConflict reports whether err matches action.ErrConflict.
func isConflict(err error) bool {
	return errors.Is(err, action.ErrConflict)
}
