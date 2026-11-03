// Package unleashflag is the action unleash.flag: it turns a feature flag
// off in one environment of an Unleash project through the Unleash Admin
// API, after an approver approved the change. It is approval-only: it
// embeds action.ReadWriteGate, so a route in read-write mode may list it
// only when the config sets allowReadWrite: true. docs/plugins/actions.md
// explains the steps it follows, and internal/action/datadog is its model.
//
// What the action relies on from Unleash, as its public source at the
// release tag v8.2.1 (github.com/Unleash/unleash, directory src/lib) shows:
//
//   - POST /api/admin/projects/{project}/features/{flag}/environments/{environment}/off
//     takes no body and answers 200 with an empty body, although the
//     OpenAPI text says it answers the flag
//     (features/feature-toggle/feature-toggle-controller.ts). It needs the
//     permission UPDATE_FEATURE_ENVIRONMENT on the project and environment
//     (types/permissions.ts), answers 400 for an archived flag
//     (error/archivedfeature-error.ts) and 404 when the flag has no such
//     environment. Turning off an environment that is off changes nothing
//     and stores no event (feature-toggle-service.ts, updateEnabled and
//     unprotectedUpdateEnabled).
//   - GET .../features/{flag} answers the flag with its environments and
//     whether each is enabled, and answers 404 for a flag that is not in
//     the project or is archived. GET .../environments/{environment}/strategies
//     lists the strategies of the flag there, with their segments.
//   - GET /api/admin/search/events with the filters project, environment and
//     feature (each as IS:<value>) and limit lists the events newest first
//     (features/events/event-search-controller.ts and event-store.ts). Any
//     authenticated token may read it. Event IDs only grow.
//   - Unleash keeps no version or ETag for a flag, and no write has a
//     precondition. The action uses the ID of the newest event of the flag
//     in the environment as the version of the snapshot, and compares the
//     live strategies as well. A change made between the read and the write
//     is not detected.
//   - A token sends Authorization: <token> with no prefix
//     (middleware/api-token-middleware.ts, pat-middleware.ts).
//   - An environment that uses change requests refuses a direct change with
//     403 and a body whose permissions list names SKIP_CHANGE_REQUEST
//     (feature-toggle-service.ts, stopWhenChangeRequestsEnabled, and
//     error/permission-error.ts). Change requests are not in the public
//     source, so the action cannot see them when it proposes; it finds out
//     when the change runs, and fails with a message that says so.
package unleashflag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers. A config selects it with
// "type: unleash.flag".
const Type = "unleash.flag"

// maxKeyLength is the longest project, environment or flag key.
const maxKeyLength = 256

var (
	errNoSecrets    = errors.New("no secrets resolver to read the token with")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
	errTargetDiffer = errors.New("the snapshot is of another flag")
)

// paramsSchemaFormat is the JSON Schema of Params. Its arguments are the
// longest key and the list of changes the config allows.
const paramsSchemaFormat = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project", "environment", "flag", "change"],
  "properties": {
    "project": {"type": "string", "minLength": 1, "maxLength": %[1]d, "description": "The key of the Unleash project."},
    "environment": {"type": "string", "minLength": 1, "maxLength": %[1]d, "description": "The name of the environment, such as production."},
    "flag": {"type": "string", "minLength": 1, "maxLength": %[1]d, "description": "The name of the feature flag."},
    "change": {"type": "string", "enum": [%[2]s], "description": "What to do to the flag in that environment."}
  }
}`

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

// Flag is one configured unleash.flag action. It embeds the gate, so the
// built instance reports that it is approval-only.
type Flag struct {
	action.ReadWriteGate

	name    string
	limits  Limits
	allow   []string
	baseURL string
	schema  json.RawMessage
	client  *client
}

var (
	_ action.Action      = (*Flag)(nil)
	_ action.Snapshotter = (*Flag)(nil)
)

// reading is what the action reads of a flag in one environment.
type reading struct {
	state  state
	newest event
}

// New builds the action from its validated config. It does not call
// Unleash: a service that is down must not stop Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	allow := cfg.Limits.Allow
	if len(allow) == 0 {
		allow = []string{changeTurnOff}
	}

	baseURL := strings.TrimSuffix(cfg.BaseURL, "/")

	return &Flag{
		ReadWriteGate: cfg.ReadWriteGate,
		name:          name,
		limits:        cfg.Limits,
		allow:         allow,
		baseURL:       baseURL,
		schema:        paramsSchema(allow),
		client: &client{
			http:      hc,
			baseURL:   baseURL,
			userAgent: "sidekick/" + buildinfo.Version,
			token:     cfg.TokenSecret,
			secrets:   deps.Secrets,
		},
	}, nil
}

// Spec implements action.Action. The description tells the model which
// flags it may change and how.
func (a *Flag) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Turns a feature flag off in one environment of an Unleash project, through the Unleash API. " +
		"The strategies of the flag are not changed, and no other environment is touched. " +
		"Unleash environments that use change requests refuse it. Allowed flags: ")

	for i, p := range a.limits.Projects {
		if i > 0 {
			b.WriteString("; ")
		}

		fmt.Fprintf(&b, "project %s, environments %s, flags %s", p.Key, strings.Join(p.Environments, ", "), strings.Join(p.Flags, ", "))
	}

	b.WriteString(". Changes: " + strings.Join(a.allow, ", ") + ".")

	return action.Spec{Name: a.name, Description: b.String(), ParamsSchema: a.schema}
}

// Validate implements action.Action. It runs without calling Unleash, so
// it checks what the parameters alone show against the limits. A limit
// violation wraps action.ErrLimit.
func (a *Flag) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := a.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// parse decodes the parameters and checks them against the limits.
func (a *Flag) parse(raw json.RawMessage) (Params, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return Params{}, fmt.Errorf("decode the parameters: %w", err)
	}

	if err := a.checkReach(p); err != nil {
		return Params{}, fmt.Errorf("check the flag: %w", err)
	}

	if !slices.Contains(a.allow, p.Change) {
		return Params{}, refusal("change %q is not one of: %s", p.Change, strings.Join(a.allow, ", "))
	}

	return p, nil
}

// checkReach checks that the project, environment and flag are keys, and
// that limits.projects lists them.
func (a *Flag) checkReach(p Params) error {
	for _, key := range []string{p.Project, p.Environment, p.Flag} {
		if !keyPattern.MatchString(key) {
			return refusal("%q is not a project, environment or flag key", key)
		}
	}

	project, ok := a.project(p.Project)
	if !ok {
		return refusal("project %q is not one the action may change; allowed: %s", p.Project, strings.Join(a.projectKeys(), ", "))
	}

	if !slices.Contains(project.Environments, p.Environment) {
		return refusal("environment %q of project %s is not one the action may change; allowed: %s",
			p.Environment, p.Project, strings.Join(project.Environments, ", "))
	}

	if !slices.ContainsFunc(project.Flags, func(pattern string) bool { return matches(pattern, p.Flag) }) {
		return refusal("flag %q is not one the action may change in project %s; allowed: %s",
			p.Flag, p.Project, strings.Join(project.Flags, ", "))
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the flag, checks it
// against the limits and computes the state the change writes, without
// changing anything.
func (a *Flag) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, err := a.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	live, err := a.read(ctx, p)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the flag: %w", err)
	}

	if !live.state.Enabled {
		return action.Snapshot{}, refusal("flag %s is already off in %s of project %s", p.Flag, p.Environment, p.Project)
	}

	after := live.state
	after.Enabled = false

	before, err := json.Marshal(live.state)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("encode the state before: %w", err)
	}

	written, err := json.Marshal(after)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("encode the state after: %w", err)
	}

	return action.Snapshot{Target: target(p), Before: before, After: written, Version: version(live.newest)}, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the flag, and writes the after state of the snapshot, unless the flag
// changed since the snapshot (action.ErrConflict). When the flag already
// has what the snapshot writes, which happens when a worker died after the
// write, it writes nothing and succeeds.
func (a *Flag) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := a.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	before, after, err := statesOf(inv.Snapshot, p)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	st, err := diffStep(before, after)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, err)
	}

	live, err := a.read(ctx, p)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the flag: %w", err)
	}

	if st.atAfter(live.state) {
		return a.result(p, st), nil
	}

	if version(live.newest) != inv.Snapshot.Version || !live.state.same(before) {
		return action.Result{}, fmt.Errorf("flag %s in %s of project %s: %w", p.Flag, p.Environment, p.Project, action.ErrConflict)
	}

	if err := a.apply(ctx, p, st); err != nil {
		return action.Result{}, fmt.Errorf("write the change: %w", err)
	}

	return a.result(p, st), nil
}

// read reads the flag in the environment of p: its newest event there,
// whether it is on and its strategies. The event comes first, so a change
// made while the rest is read leaves a version older than the state, and
// shows up as a conflict. A flag that does not exist, is archived or has no
// such environment is a refusal that wraps action.ErrLimit.
func (a *Flag) read(ctx context.Context, p Params) (reading, error) {
	newest, err := a.client.newestEvent(ctx, p.Project, p.Flag, p.Environment)
	if err != nil {
		return reading{}, fmt.Errorf("read the newest event: %w", err)
	}

	view, err := a.client.feature(ctx, p.Project, p.Flag)
	if se, ok := errors.AsType[*statusError](err); ok && se.code == http.StatusNotFound {
		return reading{}, refusal("flag %s does not exist in project %s, or it is archived", p.Flag, p.Project)
	}

	if err != nil {
		return reading{}, fmt.Errorf("read the flag: %w", err)
	}

	index := slices.IndexFunc(view.Environments, func(e featureEnvironment) bool { return e.Name == p.Environment })
	if index < 0 {
		return reading{}, refusal("flag %s has no environment %s in project %s", p.Flag, p.Environment, p.Project)
	}

	list, err := a.client.strategies(ctx, p.Project, p.Flag, p.Environment)
	if err != nil {
		return reading{}, fmt.Errorf("read the strategies: %w", err)
	}

	st, err := newState(view.Environments[index].Enabled, list)
	if err != nil {
		return reading{}, fmt.Errorf("read the state: %w", err)
	}

	return reading{state: st, newest: newest}, nil
}

// apply makes the call of the step.
func (a *Flag) apply(ctx context.Context, p Params, st step) error {
	if err := a.client.toggle(ctx, p.Project, p.Flag, p.Environment, st.kind == stepOn); err != nil {
		return fmt.Errorf("turn the flag %s: %w", st.verb(), err)
	}

	return nil
}

// result is the result of a change that is in place.
func (a *Flag) result(p Params, st step) action.Result {
	summary := fmt.Sprintf("Turned flag %s %s in %s of project %s.", p.Flag, st.verb(), p.Environment, p.Project)

	return action.Result{
		Summary: summary,
		Links:   []action.Link{{Title: "Flag " + p.Flag, URL: a.baseURL + "/projects/" + url.PathEscape(p.Project) + "/features/" + url.PathEscape(p.Flag)}},
	}
}

// project returns the entry of limits.projects for key.
func (a *Flag) project(key string) (Project, bool) {
	i := slices.IndexFunc(a.limits.Projects, func(p Project) bool { return p.Key == key })
	if i < 0 {
		return Project{}, false
	}

	return a.limits.Projects[i], true
}

// projectKeys lists the projects of limits.projects.
func (a *Flag) projectKeys() []string {
	keys := make([]string, 0, len(a.limits.Projects))
	for _, p := range a.limits.Projects {
		keys = append(keys, p.Key)
	}

	return keys
}

// statesOf decodes the before and after state of the snapshot an
// invocation carries, after checking that it is of the flag of p. A
// snapshot that is missing or is not Sidekick's fails permanently.
func statesOf(snap *action.Snapshot, p Params) (before, after state, err error) {
	if snap == nil {
		return state{}, state{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if snap.Target != target(p) {
		return state{}, state{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	if err := json.Unmarshal(snap.Before, &before); err != nil {
		return state{}, state{}, fmt.Errorf("%w: decode the state before: %w", action.ErrPermanent, err)
	}

	if err := json.Unmarshal(snap.After, &after); err != nil {
		return state{}, state{}, fmt.Errorf("%w: decode the state after: %w", action.ErrPermanent, err)
	}

	return before, after, nil
}

// paramsSchema is the JSON Schema of Params, with the changes the config
// allows. Sidekick checks the parameters against it before it calls
// Validate. The changes are names this package defines, so they need no
// escaping.
func paramsSchema(allow []string) json.RawMessage {
	enum := `"` + strings.Join(allow, `", "`) + `"`

	return json.RawMessage(fmt.Sprintf(paramsSchemaFormat, maxKeyLength, enum))
}

// target names the flag in its environment, as Snapshot.Target.
func target(p Params) string {
	return "unleash:" + p.Project + "/" + p.Environment + "/" + p.Flag
}

// version is the version of the flag in the environment: the ID of its
// newest event there, or 0 when it has none.
func version(newest event) string {
	return strconv.FormatInt(newest.ID, 10)
}

// matches reports whether the flag key matches a pattern of limits, in
// which "*" stands for any run of characters.
func matches(pattern, flag string) bool {
	ok, err := path.Match(pattern, flag)

	return err == nil && ok
}

// refusal is an error that says, for the model to read, what the action
// will not do, and wraps action.ErrLimit so that Sidekick ends the proposal
// at once.
func refusal(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), action.ErrLimit)
}
