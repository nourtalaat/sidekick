package signoz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the action type this package registers. A config selects it with
// "type: signoz.alert_rule".
const Type = "signoz.alert_rule"

// maxNameBytes is the longest rule name written into a result.
const maxNameBytes = 80

var (
	errNoSecrets     = errors.New("no secrets resolver to read the key with")
	errBothChanges   = errors.New("give either set or delete, not both")
	errNoChange      = errors.New("give set with the fields to change, or delete")
	errNotAllowed    = errors.New("ruleId is not one of limits.ruleIds, and the rule name matches none of limits.ruleNames")
	errDeleteLimit   = errors.New("delete needs limits.allowDelete")
	errTooManyKeys   = fmt.Errorf("set names more than %d fields", action.MaxChanges)
	errTargetDiffer  = errors.New("the snapshot is of another rule")
	errNotUndoable   = errors.New("a deleted rule cannot be restored by Sidekick")
	errNoSnapshot    = errors.New("the proposal has no snapshot")
	errBadRuleID     = errors.New("ruleId must be the UUID of the rule")
	errNothing       = errors.New("the rule has these values already, so the change would change nothing")
	errNotKept       = errors.New("SigNoz kept another value than the one written")
	errReadsDisabled = errors.New("SigNoz shows this rule as disabled, and it shows a rule that has not finished loading the same way, " +
		"so Sidekick cannot tell the two apart; writing the rule back could switch it off for good. " +
		"Only a change that also sets disabled to false is allowed, so that the approver sees it")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["ruleId"],
  "properties": {
    "ruleId": {"type": "string", "description": "The ID of the alert rule, a UUID."},
    "set": {
      "type": "object",
      "description": "The fields to change: each key is a field path of the rule, such as condition.target, labels.team or condition.thresholds.spec[0].target, and each value is the new value of that field. null removes a label, an annotation, a description or a recovery target.",
      "minProperties": 1
    },
    "delete": {"type": "boolean", "description": "Delete the rule. Give this or set, not both."}
  }
}`)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	RuleID string                     `json:"ruleId"`
	Set    map[string]json.RawMessage `json:"set"`
	Delete bool                       `json:"delete"`
}

// AlertRule is one configured signoz.alert_rule action.
type AlertRule struct {
	name    string
	limits  Limits
	allowed []action.Path
	baseURL string
	client  *client
}

var (
	_ action.Action      = (*AlertRule)(nil)
	_ action.Snapshotter = (*AlertRule)(nil)
	_ action.Undoer      = (*AlertRule)(nil)
)

// New builds the action from its validated config. It does not call SigNoz:
// a service that is down must not stop Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	allowed, err := action.ParsePaths(cfg.Limits.Fields)
	if err != nil {
		return nil, fmt.Errorf("limits.fields: %w", err)
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &AlertRule{
		name:    name,
		limits:  cfg.Limits,
		allowed: allowed,
		baseURL: cfg.apiURL(),
		client: &client{
			http: hc, baseURL: cfg.apiURL(), header: cfg.keyHeader(), apiKey: cfg.APIKeySecret,
			secrets: deps.Secrets, logger: logger,
		},
	}, nil
}

// Spec implements action.Action. The description tells the model which
// rules and fields it may touch.
func (a *AlertRule) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Changes fields of a SigNoz alert rule through the SigNoz API")

	if a.limits.AllowDelete {
		b.WriteString(", or deletes it. A delete cannot be undone in Sidekick")
	}

	b.WriteString(". SigNoz cannot check a new rule without side effects, so Sidekick checks the common mistakes itself " +
		"and SigNoz checks the rule again when it is written; a rule SigNoz refuses is not changed. ")

	if len(a.limits.RuleIDs) > 0 {
		b.WriteString("Allowed rule IDs: " + strings.Join(a.limits.RuleIDs, ", ") + ". ")
	}

	if len(a.limits.RuleNames) > 0 {
		b.WriteString("Rules whose name matches one of these patterns are allowed too, where * stands for any text: " + strings.Join(a.limits.RuleNames, ", ") + ". ")
	}

	b.WriteString("Give set as field path to new value. ")

	if fields := supportedWithin(a.allowed); len(fields) > 0 {
		b.WriteString("Fields that may change, by their path in the rule as SigNoz reads it: " + strings.Join(fields, ", ") + ". ")
	}

	b.WriteString("Rules without schemaVersion keep the threshold in condition.target, condition.op and condition.matchType, " +
		"and the window in evalWindow and frequency; rules with schemaVersion v2alpha1 keep them in condition.thresholds and evaluation, " +
		"and a field of the other kind is refused. [n] is the position in a list, as in condition.thresholds.spec[0].target. " +
		"A duration is text such as 5m. null removes a label, an annotation, a description or a recovery target. " +
		"A label or annotation whose name has a dot cannot be changed. " +
		"A rule that SigNoz shows as disabled can only be changed by a change that also sets disabled to false.")

	return action.Spec{Name: a.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It runs without calling SigNoz, so it
// checks what the parameters alone show: the rule ID against limits.ruleIds
// when the limits have no name pattern, and each field of set against
// limits.fields. A rule that is allowed by its name is checked when the rule
// is read. A limit violation wraps action.ErrLimit.
func (a *AlertRule) Validate(_ context.Context, raw json.RawMessage) error {
	if _, _, err := a.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the rule, checks it
// against the limits, computes the new rule from set, checks every field that
// differs against limits.fields and against what SigNoz keeps as written. For
// a delete the after rule is null.
func (a *AlertRule) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, edits, err := a.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	if err := a.client.ensureSupported(ctx); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the SigNoz release: %w", err)
	}

	live, err := a.client.get(ctx, p.RuleID)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the rule: %w", err)
	}

	if err := a.checkRule(p.RuleID, live); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	before, err := definition(live)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	snap := action.Snapshot{Target: target(p.RuleID), Before: before, Version: version(live)}

	if p.Delete {
		return snap, nil
	}

	snap.After, err = action.Apply(before, edits)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("apply set to the rule: %w: %w", action.ErrLimit, err)
	}

	if err := a.checkChange(p.RuleID, snap); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the change: %w", err)
	}

	return snap, nil
}

// checkChange checks a computed change: only fields of limits.fields differ,
// SigNoz keeps them as written, a rule that reads as disabled is not written
// back by accident, the rule stays in reach of the limits, and something
// really changes.
func (a *AlertRule) checkChange(id string, snap action.Snapshot) error {
	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return fmt.Errorf("compare the rules: %w", err)
	}

	if err := action.CheckFields(a.allowed, deltas); err != nil {
		return fmt.Errorf("check the fields: %w", err)
	}

	if err := checkChanges(snap.After, deltas); err != nil {
		return fmt.Errorf("check what SigNoz keeps: %w", err)
	}

	if err := guardDisabled(snap.Before, deltas); err != nil {
		return fmt.Errorf("check the disabled rule: %w", err)
	}

	if err := a.checkAfter(id, snap.After); err != nil {
		return fmt.Errorf("check the rule after the change: %w", err)
	}

	_, changes, err := normalisedSnapshot(snap.Before, snap)
	if err != nil {
		return fmt.Errorf("compare the rules as SigNoz reads them: %w", err)
	}

	if len(changes) == 0 {
		return fmt.Errorf("%w: %w", errNothing, action.ErrLimit)
	}

	return nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the rule, and writes the after rule of the snapshot, unless the rule
// changed since the snapshot (action.ErrConflict). When the rule already has
// the fields the snapshot sets, which happens when a worker died after the
// write, it writes nothing and succeeds.
func (a *AlertRule) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, _, err := a.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if inv.Snapshot == nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if inv.Snapshot.Target != target(p.RuleID) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	if err := a.client.ensureSupported(ctx); err != nil {
		return action.Result{}, fmt.Errorf("check the SigNoz release: %w", err)
	}

	if inv.Snapshot.Deletes() {
		return a.executeDelete(ctx, p.RuleID, *inv.Snapshot)
	}

	return a.executeUpdate(ctx, p.RuleID, *inv.Snapshot)
}

// executeUpdate writes the after rule of snap.
func (a *AlertRule) executeUpdate(ctx context.Context, id string, snap action.Snapshot) (action.Result, error) {
	live, err := a.client.get(ctx, id)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the rule: %w", err)
	}

	if err := a.checkRule(id, live); err != nil {
		return action.Result{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	cur, err := definition(live)
	if err != nil {
		return action.Result{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	if err := a.checkSnapshot(id, snap, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot against the limits: %w", err)
	}

	done, err := holdsAfter(cur, snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule: %w", err)
	}

	if done {
		return a.changed(id, snap.After, len(deltas)), nil
	}

	unchanged, err := sameDefinition(cur, snap.Before)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule with the snapshot: %w", err)
	}

	if !unchanged || version(live) != snap.Version {
		return action.Result{}, fmt.Errorf("rule %s: %w", id, action.ErrConflict)
	}

	if err := guardDisabled(cur, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the disabled rule: %w", err)
	}

	if err := a.client.update(ctx, id, snap.After); err != nil {
		return action.Result{}, fmt.Errorf("write the change: %w", err)
	}

	if err := a.verify(ctx, id, snap, true); err != nil {
		return action.Result{}, fmt.Errorf("check the write: %w", err)
	}

	return a.changed(id, snap.After, len(deltas)), nil
}

// checkSnapshot checks the fields of a stored snapshot against the limits
// as they are now.
func (a *AlertRule) checkSnapshot(id string, snap action.Snapshot, deltas []action.Delta) error {
	if err := action.CheckFields(a.allowed, deltas); err != nil {
		return fmt.Errorf("check the fields: %w", err)
	}

	if err := checkChanges(snap.After, deltas); err != nil {
		return fmt.Errorf("check what SigNoz keeps: %w", err)
	}

	if err := a.checkAfter(id, snap.After); err != nil {
		return fmt.Errorf("check the rule after the change: %w", err)
	}

	return nil
}

// executeDelete deletes the rule of snap, unless it changed since.
func (a *AlertRule) executeDelete(ctx context.Context, id string, snap action.Snapshot) (action.Result, error) {
	live, err := a.client.get(ctx, id)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return action.Result{Summary: fmt.Sprintf("Rule %s is deleted.", id)}, nil
	}

	if err != nil {
		return action.Result{}, fmt.Errorf("read the rule: %w", err)
	}

	if err := a.checkRule(id, live); err != nil {
		return action.Result{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	cur, err := definition(live)
	if err != nil {
		return action.Result{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	unchanged, err := sameDefinition(cur, snap.Before)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule with the snapshot: %w", err)
	}

	if !unchanged || version(live) != snap.Version {
		return action.Result{}, fmt.Errorf("rule %s: %w", id, action.ErrConflict)
	}

	if err := a.client.remove(ctx, id); err != nil {
		return action.Result{}, fmt.Errorf("delete the rule: %w", err)
	}

	return action.Result{Summary: fmt.Sprintf("Deleted rule %s, %s.", id, nameOf(snap.Before))}, nil
}

// Undo implements action.Undoer. It puts back the fields the action changed,
// from snap.Before, and leaves every other field of the rule as it is now.
// When the rule already has the old values it writes nothing and succeeds.
// When someone changed one of the fields since, and force is false, it
// writes nothing and returns an *action.ConflictError with the rule as it
// is. A delete cannot be undone.
func (a *AlertRule) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	p, _, err := a.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	if snap.Deletes() || len(snap.After) == 0 {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNotUndoable)
	}

	if err := a.client.ensureSupported(ctx); err != nil {
		return action.Result{}, fmt.Errorf("check the SigNoz release: %w", err)
	}

	live, err := a.client.get(ctx, p.RuleID)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the rule: %w", err)
	}

	if err := a.checkRule(p.RuleID, live); err != nil {
		return action.Result{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	cur, err := definition(live)
	if err != nil {
		return action.Result{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	if err := action.CheckFields(a.allowed, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the fields: %w", err)
	}

	restored, err := holdsBefore(cur, snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule: %w", err)
	}

	if restored {
		return a.restored(p.RuleID, cur, len(deltas)), nil
	}

	asWritten, err := holdsAfter(cur, snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule: %w", err)
	}

	if !asWritten && !force {
		return action.Result{}, &action.ConflictError{Live: cur}
	}

	if err := guardDisabled(cur, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the disabled rule: %w", err)
	}

	back, err := action.Revert(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("put the fields back: %w", err)
	}

	if err := a.client.update(ctx, p.RuleID, back); err != nil {
		return action.Result{}, fmt.Errorf("write the restore: %w", err)
	}

	if err := a.verify(ctx, p.RuleID, snap, false); err != nil {
		return action.Result{}, fmt.Errorf("check the restore: %w", err)
	}

	return a.restored(p.RuleID, back, len(deltas)), nil
}

// verify reads the rule again after a write and checks that SigNoz kept the
// after values (or the before values, after an undo). The write has happened
// by then, so the error says so, names the fields, and links to the rule for
// a person to look at. It is permanent: writing again would not help.
func (a *AlertRule) verify(ctx context.Context, id string, snap action.Snapshot, after bool) error {
	live, err := a.client.get(ctx, id)
	if err != nil {
		return fmt.Errorf("read the rule after the write: %w", err)
	}

	cur, err := definition(live)
	if err != nil {
		return fmt.Errorf("keep the writable fields: %w", err)
	}

	wrong, err := keptWrong(cur, snap, after)
	if err != nil {
		return fmt.Errorf("compare the rule after the write: %w", err)
	}

	if len(wrong) == 0 {
		return nil
	}

	return fmt.Errorf("the rule was written, but SigNoz kept a different value for %s; check the rule at %s: %w: %w",
		strings.Join(wrong, ", "), a.ruleLink(id), action.ErrPermanent, errNotKept)
}

// parse decodes the parameters and checks them against the limits. It
// returns them with the edits set describes, in order of field path.
func (a *AlertRule) parse(raw json.RawMessage) (Params, []action.Edit, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return Params{}, nil, fmt.Errorf("decode the parameters: %w", err)
	}

	if !ruleID.MatchString(p.RuleID) {
		return Params{}, nil, fmt.Errorf("%w: %w", action.ErrParams, errBadRuleID)
	}

	p.RuleID = strings.ToLower(p.RuleID)

	if len(a.limits.RuleNames) == 0 && !a.listed(p.RuleID) {
		return Params{}, nil, fmt.Errorf("rule %s: %w: %w", p.RuleID, action.ErrLimit, errNotAllowed)
	}

	switch {
	case p.Delete && len(p.Set) > 0:
		return Params{}, nil, errBothChanges
	case p.Delete:
		if !a.limits.AllowDelete {
			return Params{}, nil, fmt.Errorf("%w: %w", action.ErrLimit, errDeleteLimit)
		}

		return p, nil, nil
	case len(p.Set) == 0:
		return Params{}, nil, errNoChange
	case len(p.Set) > action.MaxChanges:
		return Params{}, nil, errTooManyKeys
	}

	edits, err := a.edits(p.Set)
	if err != nil {
		return Params{}, nil, fmt.Errorf("check the fields to set: %w", err)
	}

	return p, edits, nil
}

// edits turns the keys of set into edits, in key order, after checking each
// against limits.fields. A null value removes the field: SigNoz would read
// a null in a map of text as empty text, and a null in the rule as not set.
func (a *AlertRule) edits(set map[string]json.RawMessage) ([]action.Edit, error) {
	edits := make([]action.Edit, 0, len(set))

	for _, key := range slices.Sorted(maps.Keys(set)) {
		path, err := action.ParsePath(key)
		if err != nil {
			return nil, fmt.Errorf("set: %w", err)
		}

		if !slices.ContainsFunc(a.allowed, path.Within) {
			return nil, fmt.Errorf("the field %q is not one of limits.fields: %w", key, action.ErrLimit)
		}

		edit := action.Edit{Path: path, Value: set[key]}
		if strings.TrimSpace(string(set[key])) == "null" {
			edit.Value = nil
		}

		edits = append(edits, edit)
	}

	return edits, nil
}

// checkRule checks that the rule read from SigNoz is one the limits allow:
// its ID is in limits.ruleIds, or its name matches a pattern of
// limits.ruleNames.
func (a *AlertRule) checkRule(id string, live map[string]json.RawMessage) error {
	if a.listed(id) {
		return nil
	}

	var name string
	if err := json.Unmarshal(live["alert"], &name); err != nil {
		return fmt.Errorf("read the name of rule %s: %w", id, err)
	}

	if matchesAnyName(a.limits.RuleNames, name) {
		return nil
	}

	return fmt.Errorf("rule %s: %w: %w", id, action.ErrLimit, errNotAllowed)
}

// checkAfter checks that the rule is still one the limits allow once the
// change is made. A change must not take a rule out of the action's reach,
// for example by renaming it out of limits.ruleNames: the model would have
// moved it beyond the operator's limits, and Undo could never reach it
// again. A rule allowed by its ID is always in reach.
func (a *AlertRule) checkAfter(id string, after json.RawMessage) error {
	if a.listed(id) {
		return nil
	}

	var parsed struct {
		Alert string `json:"alert"`
	}

	if err := json.Unmarshal(after, &parsed); err != nil {
		return fmt.Errorf("the name after the change is not text: %w: %w", action.ErrLimit, err)
	}

	if matchesAnyName(a.limits.RuleNames, parsed.Alert) {
		return nil
	}

	return fmt.Errorf("the change would rename rule %s out of limits.ruleNames: %w", id, action.ErrLimit)
}

// listed reports whether the rule ID is in limits.ruleIds.
func (a *AlertRule) listed(id string) bool {
	return slices.ContainsFunc(a.limits.RuleIDs, func(listed string) bool { return strings.EqualFold(listed, id) })
}

// changed is the result of a change that is in place.
func (a *AlertRule) changed(id string, after json.RawMessage, fields int) action.Result {
	return a.result(id, fmt.Sprintf("Changed %s of rule %s, %s.", fieldCount(fields), id, nameOf(after)))
}

// restored is the result of a restore that is in place.
func (a *AlertRule) restored(id string, doc json.RawMessage, fields int) action.Result {
	return a.result(id, fmt.Sprintf("Restored %s of rule %s, %s.", fieldCount(fields), id, nameOf(doc)))
}

// result is a result that links to the rule in SigNoz.
func (a *AlertRule) result(id, summary string) action.Result {
	return action.Result{Summary: summary, Links: []action.Link{{Title: "Alert rule", URL: a.ruleLink(id)}}}
}

// ruleLink is the page of the rule in the SigNoz web app.
func (a *AlertRule) ruleLink(id string) string {
	return a.baseURL + "/alerts/overview?ruleId=" + id
}

// guardDisabled refuses to write a rule back that reads as disabled, unless
// the change itself sets disabled. SigNoz shows a rule as disabled when a
// person disabled it and also when the rule is not loaded in its rule
// manager, for example right after a restart, whatever its stored value is,
// and no read tells the two apart. A whole-rule write would store disabled as
// read, so for a rule that only failed to load it would switch the rule off
// for good. A change that sets disabled shows the approver the switch.
func guardDisabled(def json.RawMessage, deltas []action.Delta) error {
	if !readsDisabled(def) {
		return nil
	}

	if slices.ContainsFunc(deltas, func(d action.Delta) bool { return slices.Equal(d.Path, disabledPath) }) {
		return nil
	}

	return fmt.Errorf("%w: %w", errReadsDisabled, action.ErrLimit)
}

// disabledPath is the field that says a rule is paused.
var disabledPath = action.Path{"disabled"}

// readsDisabled reports whether a rule definition has disabled set.
func readsDisabled(def json.RawMessage) bool {
	var parsed struct {
		Disabled bool `json:"disabled"`
	}

	return json.Unmarshal(def, &parsed) == nil && parsed.Disabled
}

// target names the rule, as Snapshot.Target.
func target(id string) string {
	return "signoz:rule/" + id
}

// definition keeps the fields of a rule that the action can write: every key
// of the read except the ones SigNoz sets.
func definition(live map[string]json.RawMessage) (json.RawMessage, error) {
	kept := map[string]json.RawMessage{}

	for key, value := range live {
		if !slices.Contains(readOnlyKeys, key) {
			kept[key] = value
		}
	}

	raw, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("encode the definition: %w", err)
	}

	return raw, nil
}

// version is the time SigNoz says the rule was last updated, or "". It is a
// date-time that changes on every write. Only its equality is used.
func version(live map[string]json.RawMessage) string {
	var updated string
	if err := json.Unmarshal(live["updatedAt"], &updated); err != nil {
		return ""
	}

	return updated
}

// nameOf is the name of the rule in a definition, cut, for a result. It is
// text from SigNoz.
func nameOf(doc json.RawMessage) string {
	var d struct {
		Alert string `json:"alert"`
	}

	if json.Unmarshal(doc, &d) != nil || d.Alert == "" {
		return "no name"
	}

	return strconv.Quote(source.CutText(d.Alert, maxNameBytes))
}

// fieldCount writes a number of fields, such as "1 field" or "3 fields".
func fieldCount(n int) string {
	if n == 1 {
		return "1 field"
	}

	return strconv.Itoa(n) + " fields"
}
