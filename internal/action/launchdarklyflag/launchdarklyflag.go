// Package launchdarklyflag is the action launchdarkly.flag: it turns a
// LaunchDarkly feature flag off in one environment through the LaunchDarkly
// REST API, after an approver approved the change, and it can turn the flag
// back on. The action is approval-only: it embeds action.ReadWriteGate, so
// a read-write route may list it only when the operator set
// allowReadWrite. It follows internal/action/datadog: Snapshot reads the
// flag and computes the change, Execute writes what the approver read, and
// Undo puts back the field the action changed.
//
// Only turning a flag off is supported. Putting the earlier targeting back
// would need the full previous configuration of the environment, and the
// public documentation of the audit log entry does not show that it holds
// one (see below), so that part is not built.
//
// What the action relies on from LaunchDarkly. LaunchDarkly publishes its
// OpenAPI file only as a live document, https://app.launchdarkly.com/api/v2/openapi.json
// (OpenAPI 3.0.3, info.version 2.0; sha256 036b4c4965b836fc244b0c141948f26af90b451beee46582cbb21bd380027c68
// on 2026-11-03); the tags of github.com/launchdarkly/ld-openapi hold only
// the scripts that generate clients from it and no spec. The same text is
// at tag v25.1.1 (commit 063ee128b64d) of the generated Go client,
// github.com/launchdarkly/api-client-go, in api_feature_flags.go and
// api_audit_log.go. The public documentation is
// https://launchdarkly.com/docs/api and
// https://launchdarkly.com/docs/home/account/role-actions.
//
//   - PATCH /api/v2/flags/{projectKey}/{featureFlagKey} (patchFeatureFlag)
//     takes a semantic patch when the Content-Type is
//     "application/json; domain-model=launchdarkly.semanticpatch". The body
//     has comment (optional), environmentKey and instructions, a list of
//     objects with a kind. turnFlagOff and turnFlagOn take no other field.
//     A semantic patch is applied whole or not at all, and an instruction
//     that finds the state already asked for leaves it unchanged.
//   - A change in an environment where approvals are required for the flag
//     fails with 405. A change that would make a pending scheduled change
//     or approval request fail returns 400, unless the ignoreConflicts query
//     parameter is true, which the action never sends.
//   - Semantic patch has no precondition on the flag's version. The only
//     documented precondition is a JSON patch "test" operation, shown on a
//     "/version" path that the flag no longer has; that it works on
//     /environments/{env}/version, and what it answers when it fails, is
//     not documented. The action therefore reads the flag and compares its
//     environment version just before it writes, and a change made in the
//     short time between that read and the write is not detected, as for
//     datadog.monitor.
//   - GET /api/v2/flags/{projectKey}/{featureFlagKey}?env={env} gives
//     environments.{env} with on, archived, version and _site, and the
//     flag's archived and tags.
//   - GET /api/v2/projects/{projectKey}/environments/{environmentKey} gives
//     approvalSettings (required, requiredApprovalTags), but only when the
//     approvals feature is enabled. requiredApprovalTags limits approvals to
//     flags with one of the tags; an empty list means every flag.
//   - GET .../flags/{flag}/environments/{env}/scheduled-changes lists the
//     scheduled changes, and GET .../workflows?status=active gives
//     totalCount. The documentation does not say which custom role action
//     these two reads need.
//   - A custom role allows the change with the action updateOn, scoped to
//     the resource proj/<project>:env/<environment>:flag/<flag>, and viewProject
//     on proj/<project>. basePermissions no_access grants only what the
//     policy allows.
//   - GET /api/v2/auditlog/{id} documents previousVersion only as "a JSON
//     representation of the previous version of the entity", with no
//     example and no schema, and delta is null for a semantic patch.
package launchdarklyflag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the action type this package registers. A config selects it with
// "type: launchdarkly.flag".
const Type = "launchdarkly.flag"

// changeTurnOff is the one change the model may propose.
const changeTurnOff = "turn_off"

// maxShown is the longest model-written text put in an error message.
const maxShown = 80

var (
	errNoSecrets    = errors.New("no secrets resolver to read the token with")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
	errTargetDiffer = errors.New("the snapshot is of another flag")
	errNotTurnOff   = errors.New("the snapshot is not a change from on to off")
	errNoConfig     = errors.New("the flag has no configuration in the environment")
	errArchived     = errors.New("the flag is archived")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["project", "environment", "flag", "change"],
  "properties": {
    "project": {"type": "string", "maxLength": 256, "description": "The key of the LaunchDarkly project."},
    "environment": {"type": "string", "maxLength": 256, "description": "The key of the environment. Only this environment of the flag changes."},
    "flag": {"type": "string", "maxLength": 256, "description": "The key of the feature flag."},
    "change": {"type": "string", "enum": ["turn_off"], "description": "turn_off turns the flag off in this environment."}
  }
}`)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Flag        string `json:"flag"`
	Change      string `json:"change"`
}

// state is the part of a flag's environment configuration that a snapshot
// holds: the only field the action changes.
type state struct {
	On bool `json:"on"`
}

// Flag is one configured launchdarkly.flag action.
type Flag struct {
	action.ReadWriteGate

	name   string
	limits Limits
	client *client
	logger *slog.Logger
}

var (
	_ action.Action      = (*Flag)(nil)
	_ action.Snapshotter = (*Flag)(nil)
	_ action.Undoer      = (*Flag)(nil)
)

// New builds the action from its validated config. It does not call
// LaunchDarkly: a service that is down must not stop Sidekick from
// starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	base := defaultBaseURL
	if cfg.BaseURL != "" {
		base = strings.TrimSuffix(cfg.BaseURL, "/")
	}

	return &Flag{
		ReadWriteGate: cfg.ReadWriteGate,
		name:          name,
		limits:        cfg.Limits,
		client:        &client{http: hc, baseURL: base, token: cfg.TokenSecret, secrets: deps.Secrets},
		logger:        logger,
	}, nil
}

// Spec implements action.Action. The description tells the model which
// flags it may touch.
func (f *Flag) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Turns a LaunchDarkly feature flag off in one environment through the LaunchDarkly API. " +
		"Only turning a flag off is supported, and no other environment of the flag changes. " +
		"A flag that is already off or archived is refused, and so is an environment that requires approvals " +
		"or a flag with a scheduled change or an active workflow in that environment. Allowed: ")

	for i, p := range f.limits.Projects {
		if i > 0 {
			b.WriteString("; ")
		}

		fmt.Fprintf(&b, "project %s, environments %s, flags %s",
			p.Key, strings.Join(p.Environments, ", "), strings.Join(p.Flags, ", "))
	}

	b.WriteString(".")

	return action.Spec{Name: f.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It runs without calling LaunchDarkly,
// so it checks only what the parameters show: the project, the environment
// and the flag against limits.projects. A limit violation wraps
// action.ErrLimit.
func (f *Flag) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := f.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the flag and refuses a
// change that cannot be made: a flag that is archived or already off, or an
// environment where LaunchDarkly would refuse a direct change. The snapshot
// holds the environment's on setting before and after, and its version.
func (f *Flag) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, err := f.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	live, err := f.client.flag(ctx, p.Project, p.Flag, p.Environment)
	if err != nil {
		return action.Snapshot{}, readFailure(p, err)
	}

	env, err := liveState(live, p.Environment)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("check the flag: %w: %w", action.ErrLimit, err)
	}

	if !env.On {
		return action.Snapshot{}, fmt.Errorf("flag %s is already off in environment %s: %w", p.Flag, p.Environment, action.ErrLimit)
	}

	if err := f.checkPending(ctx, p, live); err != nil {
		return action.Snapshot{}, fmt.Errorf("check for changes pending in LaunchDarkly: %w", err)
	}

	return action.Snapshot{
		Target:  target(p),
		Before:  json.RawMessage(`{"on":true}`),
		After:   json.RawMessage(`{"on":false}`),
		Version: strconv.Itoa(env.Version),
	}, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the flag, and turns it off as the snapshot says, unless it changed since
// the snapshot (action.ErrConflict). A flag that is off already, which
// happens when a worker died after the write, is left alone and the call
// succeeds.
func (f *Flag) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := f.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	before, after, err := readSnapshot(p, inv.Snapshot)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	if !before.On || after.On {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNotTurnOff)
	}

	live, err := f.client.flag(ctx, p.Project, p.Flag, p.Environment)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the flag: %w", err)
	}

	env, err := liveState(live, p.Environment)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the flag: %w: %w", action.ErrConflict, err)
	}

	switch {
	case env.On == after.On:
		return f.result(p, env, "Turned off"), nil
	case env.On != before.On || strconv.Itoa(env.Version) != inv.Snapshot.Version:
		return action.Result{}, fmt.Errorf("flag %s in environment %s: %w", p.Flag, p.Environment, action.ErrConflict)
	}

	if err := f.client.turn(ctx, p.Project, p.Flag, p.Environment, after.On, "Sidekick proposal "+inv.ProposalID); err != nil {
		return action.Result{}, fmt.Errorf("write the change: %w", err)
	}

	return f.result(p, env, "Turned off"), nil
}

// Undo implements action.Undoer. It turns the flag back on in the
// environment, unless it is on already, in which case it writes nothing and
// succeeds. The action changes one field, which is either on or off, so the
// flag cannot differ from both the snapshot's before and after, and force
// has nothing to confirm.
func (f *Flag) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, _ bool) (action.Result, error) {
	p, err := f.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	before, _, err := readSnapshot(p, &snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	live, err := f.client.flag(ctx, p.Project, p.Flag, p.Environment)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the flag: %w", err)
	}

	env, err := liveState(live, p.Environment)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the flag: %w: %w", action.ErrPermanent, err)
	}

	if env.On == before.On {
		return f.result(p, env, "Turned on"), nil
	}

	of := inv.UndoOf
	if of == "" {
		of = inv.ProposalID
	}

	if err := f.client.turn(ctx, p.Project, p.Flag, p.Environment, before.On, "Sidekick undo of proposal "+of); err != nil {
		return action.Result{}, fmt.Errorf("write the restore: %w", err)
	}

	return f.result(p, env, "Turned on"), nil
}

// parse decodes the parameters and checks them against the limits.
func (f *Flag) parse(raw json.RawMessage) (Params, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return Params{}, fmt.Errorf("decode the parameters: %w", err)
	}

	if p.Change != changeTurnOff {
		return Params{}, fmt.Errorf("change %q is not supported; the only change is %s", source.CutText(p.Change, maxShown), changeTurnOff)
	}

	if err := f.limits.allow(p); err != nil {
		return Params{}, fmt.Errorf("check the limits: %w", err)
	}

	return p, nil
}

// allow checks that the project, the environment and the flag of p are
// listed. The error says what is allowed, for the model.
func (l Limits) allow(p Params) error {
	i := slices.IndexFunc(l.Projects, func(pr Project) bool { return pr.Key == p.Project })
	if i < 0 {
		keys := make([]string, 0, len(l.Projects))
		for _, pr := range l.Projects {
			keys = append(keys, pr.Key)
		}

		return fmt.Errorf("%w: project %q is not one of limits.projects; allowed: %s",
			action.ErrLimit, source.CutText(p.Project, maxShown), strings.Join(keys, ", "))
	}

	pr := l.Projects[i]

	if !slices.Contains(pr.Environments, p.Environment) {
		return fmt.Errorf("%w: environment %q of project %s is not allowed; allowed: %s",
			action.ErrLimit, source.CutText(p.Environment, maxShown), pr.Key, strings.Join(pr.Environments, ", "))
	}

	if !keyPattern.MatchString(p.Flag) || !slices.ContainsFunc(pr.Flags, func(pattern string) bool {
		ok, err := path.Match(pattern, p.Flag)

		return err == nil && ok
	}) {
		return fmt.Errorf("%w: flag %q is not allowed in project %s; allowed flags: %s",
			action.ErrLimit, source.CutText(p.Flag, maxShown), pr.Key, strings.Join(pr.Flags, ", "))
	}

	return nil
}

// checkPending refuses a flag that LaunchDarkly would refuse to change
// directly: its environment requires approvals, or the flag has a scheduled
// change or an active workflow there. A read that fails, such as one the
// token is not allowed, does not refuse the proposal: it is logged, and the
// 405 or 400 of the write, which Execute reports, is what stops the change.
func (f *Flag) checkPending(ctx context.Context, p Params, live flagDoc) error {
	env, err := f.client.environment(ctx, p.Project, p.Environment)

	switch {
	case err != nil:
		f.unchecked("approvals", p, err)
	case env.requiresApproval(live.Tags):
		return fmt.Errorf("environment %s of project %s requires approval for changes to flag %s, so LaunchDarkly refuses a direct change; "+
			"a person must make an approval request in LaunchDarkly: %w", p.Environment, p.Project, p.Flag, action.ErrLimit)
	}

	n, err := f.client.scheduledChanges(ctx, p.Project, p.Flag, p.Environment)

	switch {
	case err != nil:
		f.unchecked("scheduled changes", p, err)
	case n > 0:
		return fmt.Errorf("flag %s has %d scheduled change(s) in environment %s; a person should look at them in LaunchDarkly first: %w",
			p.Flag, n, p.Environment, action.ErrLimit)
	}

	n, err = f.client.activeWorkflows(ctx, p.Project, p.Flag, p.Environment)

	switch {
	case err != nil:
		f.unchecked("workflows", p, err)
	case n > 0:
		return fmt.Errorf("flag %s has an active workflow in environment %s; a person should look at it in LaunchDarkly first: %w",
			p.Flag, p.Environment, action.ErrLimit)
	}

	return nil
}

// unchecked logs a check that could not be made.
func (f *Flag) unchecked(what string, p Params, err error) {
	f.logger.Warn("could not check "+what+" before proposing a flag change",
		slog.String("action", f.name), slog.String("project", p.Project),
		slog.String("environment", p.Environment), slog.String("flag", p.Flag), slog.Any("error", err))
}

// result is the result of a change that is in place, with a link to the
// flag when LaunchDarkly gave one.
func (f *Flag) result(p Params, env flagState, verb string) action.Result {
	res := action.Result{Summary: fmt.Sprintf("%s flag %s in %s/%s.", verb, p.Flag, p.Project, p.Environment)}

	if strings.HasPrefix(env.Site.Href, "/") {
		res.Links = []action.Link{{Title: "Flag " + p.Flag, URL: f.client.baseURL + env.Site.Href}}
	}

	return res
}

// requiresApproval reports whether LaunchDarkly requires approval for a
// flag with tags in the environment: approvals are on, for every flag or
// for the flags with one of requiredApprovalTags.
func (e environmentDoc) requiresApproval(tags []string) bool {
	if !e.ApprovalSettings.Required {
		return false
	}

	if len(e.ApprovalSettings.RequiredTags) == 0 {
		return true
	}

	return slices.ContainsFunc(tags, func(tag string) bool { return slices.Contains(e.ApprovalSettings.RequiredTags, tag) })
}

// readFailure turns the error of reading the flag into the error Snapshot
// returns. A flag LaunchDarkly does not know is a refusal the model can act
// on; anything else is a failure to read, which the model is told to try
// again.
func readFailure(p Params, err error) error {
	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return fmt.Errorf("flag %s was not found in project %s: %w", p.Flag, p.Project, action.ErrLimit)
	}

	return fmt.Errorf("read the flag: %w", err)
}

// readSnapshot checks that snap is the snapshot of this flag and returns
// its before and after states.
func readSnapshot(p Params, snap *action.Snapshot) (before, after state, err error) {
	if snap == nil {
		return state{}, state{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if snap.Target != target(p) {
		return state{}, state{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	if err := json.Unmarshal(snap.Before, &before); err != nil {
		return state{}, state{}, fmt.Errorf("%w: decode before: %w", action.ErrPermanent, err)
	}

	if err := json.Unmarshal(snap.After, &after); err != nil {
		return state{}, state{}, fmt.Errorf("%w: decode after: %w", action.ErrPermanent, err)
	}

	return before, after, nil
}

// liveState returns the configuration of the flag in env, or an error when
// the flag is archived or has none there.
func liveState(live flagDoc, env string) (flagState, error) {
	state, ok := live.Environments[env]

	switch {
	case !ok:
		return flagState{}, errNoConfig
	case live.Archived || state.Archived:
		return flagState{}, errArchived
	default:
		return state, nil
	}
}

// target names the flag and environment, as Snapshot.Target.
func target(p Params) string {
	return "launchdarkly:proj/" + p.Project + "/env/" + p.Environment + "/flag/" + p.Flag
}
