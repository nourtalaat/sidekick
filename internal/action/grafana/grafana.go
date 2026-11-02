// Package grafana is the action grafana.alert_rule: it changes a Grafana
// alert rule in place through the alerting provisioning HTTP API, or
// deletes it, after an approver approved the change, and it can restore the
// fields it changed. It follows datadog.monitor: the parameters say which
// fields to set, Snapshot reads the rule and computes the new definition,
// Execute writes what the approver read, and Undo puts back only the fields
// the action changed. docs/plugins/actions.md explains the steps.
//
// What the action relies on from Grafana, as its source shows (tag v13.2.5
// of github.com/grafana/grafana, with v12.4.14 and v11.6.9 where they
// differ; paths are under pkg/services/ngalert/). It supports Grafana 11.6
// and later.
//
//   - GET, PUT and DELETE /api/v1/provisioning/alert-rules/{UID}
//     (api/api_provisioning.go). From Grafana 13.0 these answer with a
//     deprecation warning (api/provisioning.go); they still work in 13.3.0.
//   - There is no call that checks a rule without storing it, so Snapshot
//     repeats the checks Grafana makes (check.go) and a rule Grafana still
//     refuses fails the proposal, with nothing stored.
//   - A write or delete without the X-Disable-Provenance header marks the
//     rule as provisioned by the API, which locks it in the Grafana UI
//     (api/api_provisioning.go, determineManagerProperties). Every write
//     and delete sends the header.
//   - With the header, a rule that has a provenance (made by files,
//     Terraform or the API) is refused with 409
//     (provisioning/alert_rules.go, UpdateAlertRule), so the action refuses
//     such a rule when it reads it, using the read-only provenance field.
//   - The rule has a read-only updated time that changes on every write
//     (api/tooling/definitions/provisioning_alert_rules.go). Only its
//     equality is used. There is no precondition on the write, so a change
//     made between the last read and the write is not detected.
//   - A PUT replaces the whole rule (api/compat/compat.go), so the action
//     always sends the whole rule, without id, orgID, uid, updated and
//     provenance, which Grafana sets itself. The folder and the group are
//     sent as they are: sending another moves the rule, so they are not
//     fields the action can change.
//   - Grafana does not keep a rule exactly as sent. normalise.go compares
//     values the way Grafana reads them back.
package grafana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the action type this package registers. A config selects it with
// "type: grafana.alert_rule".
const Type = "grafana.alert_rule"

// maxTitleShown is the longest rule title written into a result or a message.
const maxTitleShown = 80

var (
	errNoSecrets    = errors.New("no secrets resolver to read the token with")
	errBothChanges  = errors.New("give either set or delete, not both")
	errNoChange     = errors.New("give set with the fields to change, or delete")
	errNotAllowed   = errors.New("the rule is not one of limits.ruleUids and its title matches none of limits.titlePatterns")
	errNotInFolder  = errors.New("the rule is not in one of limits.folderUids")
	errDeleteLimit  = errors.New("delete needs limits.allowDelete")
	errTooManyKeys  = fmt.Errorf("set names more than %d fields", action.MaxChanges)
	errTargetDiffer = errors.New("the snapshot is of another rule")
	errNotUndoable  = errors.New("a deleted rule cannot be restored by Sidekick")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
	errRecording    = errors.New("the rule is a recording rule, which this action does not change")
	errMoves        = errors.New("the change would move the rule to another folder or group")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["ruleUid"],
  "properties": {
    "ruleUid": {"type": "string", "pattern": "^[A-Za-z0-9_-]+$", "maxLength": 40, "description": "The UID of the alert rule."},
    "set": {
      "type": "object",
      "description": "The fields to change: each key is a field path of the rule, such as for, data[0].model.expr or labels.severity, and each value is the new value of that field.",
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
	RuleUID string                     `json:"ruleUid"`
	Set     map[string]json.RawMessage `json:"set"`
	Delete  bool                       `json:"delete"`
}

// Rule is one configured grafana.alert_rule action.
type Rule struct {
	name     string
	limits   Limits
	allowed  []action.Path
	patterns []*regexp.Regexp
	baseURL  string
	client   *client
}

var (
	_ action.Action      = (*Rule)(nil)
	_ action.Snapshotter = (*Rule)(nil)
	_ action.Undoer      = (*Rule)(nil)
)

// New builds the action from its validated config. It does not call
// Grafana: a service that is down must not stop Sidekick from starting.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	allowed, err := action.ParsePaths(cfg.Limits.Fields)
	if err != nil {
		return nil, fmt.Errorf("limits.fields: %w", err)
	}

	patterns, err := compilePatterns(cfg.Limits.TitlePatterns)
	if err != nil {
		return nil, fmt.Errorf("compile the title patterns: %w", err)
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	return &Rule{
		name:     name,
		limits:   cfg.Limits,
		allowed:  allowed,
		patterns: patterns,
		baseURL:  cfg.apiURL(),
		client:   &client{http: hc, baseURL: cfg.apiURL(), token: cfg.TokenSecret, secrets: deps.Secrets},
	}, nil
}

// Spec implements action.Action. The description tells the model which
// rules and fields it may touch.
func (r *Rule) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Changes fields of a Grafana alert rule through the Grafana API")

	if r.limits.AllowDelete {
		b.WriteString(", or deletes it. A delete cannot be undone in Sidekick")
	}

	b.WriteString(". Grafana cannot check a new rule without storing it, so Sidekick checks the common problems when you propose, " +
		"and Grafana can still refuse the rule when it is approved. A rule that Grafana marks as provisioned " +
		"(made by files, Terraform or an API client) is never changed. ")
	b.WriteString("Allowed folder UIDs: " + strings.Join(r.limits.FolderUIDs, ", ") + ". ")

	if len(r.limits.RuleUIDs) > 0 {
		b.WriteString("Allowed rule UIDs: " + strings.Join(r.limits.RuleUIDs, ", ") + ". ")
	}

	if len(r.limits.TitlePatterns) > 0 {
		b.WriteString("Rules with a title that matches one of the patterns are allowed too (* is any text): " +
			strings.Join(r.limits.TitlePatterns, ", ") + ". ")
	}

	if len(r.limits.Fields) > 0 {
		b.WriteString("Fields that may change, with every field below them: " + strings.Join(r.limits.Fields, ", ") + ". ")
	}

	b.WriteString("Give set as field path to new value; an array is replaced whole. " +
		"The queries and expressions of the rule are the list data, in the order of the rule, " +
		"for example data[0].model.expr or data[2].model.conditions[0].evaluator.params. " +
		"for and keep_firing_for are durations as text, such as 5m. " +
		"Set a label or annotation to null to remove it; to change one whose name has a dot, set labels or annotations whole. " +
		"The folder and the rule group cannot change.")

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It runs without calling Grafana, so it
// checks what the parameters alone show: the rule UID against
// limits.ruleUids when no title pattern can allow another rule, and each
// field of set against limits.fields. The folder and the title are checked
// when the rule is read. A limit violation wraps action.ErrLimit.
func (r *Rule) Validate(_ context.Context, raw json.RawMessage) error {
	if _, _, err := r.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the rule, checks it
// against the limits, computes the new definition from set, checks every
// field that differs against limits.fields, and checks that Grafana would
// store the new definition as written. For a delete the after definition is
// null.
func (r *Rule) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, edits, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	live, err := r.client.get(ctx, p.RuleUID)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the rule: %w", err)
	}

	before, err := r.inspect(p.RuleUID, live)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	snap := action.Snapshot{Target: target(p.RuleUID), Before: before, Version: version(live)}

	if p.Delete {
		return snap, nil
	}

	after, err := action.Apply(before, edits)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("apply set to the rule: %w: %w", action.ErrLimit, err)
	}

	snap.After, err = dropNullEntries(before, after)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("remove the entries set to null: %w", err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("compare the definitions: %w", err)
	}

	if err := action.CheckFields(r.allowed, deltas); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the fields: %w", err)
	}

	if err := r.checkNew(p.RuleUID, snap, deltas); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the new definition: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the rule, and writes the after definition of the snapshot, unless the rule
// changed since the snapshot (action.ErrConflict). When the rule already has
// the fields the snapshot sets, which happens when a worker died after the
// write, it writes nothing and succeeds.
func (r *Rule) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, _, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if inv.Snapshot == nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if inv.Snapshot.Target != target(p.RuleUID) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	if inv.Snapshot.Deletes() {
		return r.executeDelete(ctx, p.RuleUID, *inv.Snapshot)
	}

	return r.executeUpdate(ctx, p.RuleUID, *inv.Snapshot)
}

// Undo implements action.Undoer. It puts back the fields the action
// changed, from snap.Before, and leaves every other field of the rule as it
// is now. When the rule already has the old values it writes nothing and
// succeeds. When someone changed one of the fields since, and force is
// false, it writes nothing and returns an *action.ConflictError with the
// rule as it is. A delete cannot be undone.
func (r *Rule) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	p, _, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	if snap.Deletes() || len(snap.After) == 0 {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNotUndoable)
	}

	live, err := r.client.get(ctx, p.RuleUID)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the rule: %w", err)
	}

	cur, err := r.inspect(p.RuleUID, live)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	if err := action.CheckFields(r.allowed, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the fields: %w", err)
	}

	restored, err := holdsBefore(cur, snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule: %w", err)
	}

	if restored {
		return r.restored(p.RuleUID, cur, len(deltas)), nil
	}

	asWritten, err := holdsAfter(cur, snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule: %w", err)
	}

	if !asWritten && !force {
		return action.Result{}, &action.ConflictError{Live: cur}
	}

	back, err := action.Revert(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("put the fields back: %w", err)
	}

	if err := r.checkAfter(p.RuleUID, back); err != nil {
		return action.Result{}, fmt.Errorf("check the rule after the restore: %w", err)
	}

	if err := r.client.update(ctx, p.RuleUID, back); err != nil {
		return action.Result{}, fmt.Errorf("write the restore: %w", err)
	}

	return r.restored(p.RuleUID, back, len(deltas)), nil
}

// parse decodes the parameters and checks them against the limits. It
// returns them with the edits set describes, in order of field path.
func (r *Rule) parse(raw json.RawMessage) (Params, []action.Edit, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return Params{}, nil, fmt.Errorf("decode the parameters: %w", err)
	}

	if !validUID(p.RuleUID) {
		return Params{}, nil, fmt.Errorf("ruleUid %q is not the UID of a Grafana rule: %w", source.CutText(p.RuleUID, maxTitleShown), action.ErrParams)
	}

	if len(r.limits.RuleUIDs) > 0 && len(r.patterns) == 0 && !slices.Contains(r.limits.RuleUIDs, p.RuleUID) {
		return Params{}, nil, action.LimitError(errNotAllowed, "rule %s", p.RuleUID)
	}

	switch {
	case p.Delete && len(p.Set) > 0:
		return Params{}, nil, errBothChanges
	case p.Delete:
		if !r.limits.AllowDelete {
			return Params{}, nil, fmt.Errorf("%w: %w", action.ErrLimit, errDeleteLimit)
		}

		return p, nil, nil
	case len(p.Set) == 0:
		return Params{}, nil, errNoChange
	case len(p.Set) > action.MaxChanges:
		return Params{}, nil, errTooManyKeys
	}

	edits, err := r.edits(p.Set)
	if err != nil {
		return Params{}, nil, fmt.Errorf("check the fields to set: %w", err)
	}

	return p, edits, nil
}

// edits turns the keys of set into edits, in key order, after checking each
// against limits.fields.
func (r *Rule) edits(set map[string]json.RawMessage) ([]action.Edit, error) {
	edits := make([]action.Edit, 0, len(set))

	for _, key := range slices.Sorted(maps.Keys(set)) {
		path, err := action.ParsePath(key)
		if err != nil {
			return nil, fmt.Errorf("set: %w", err)
		}

		if !r.covers(path) {
			return nil, fmt.Errorf("the field %q is not one of limits.fields: %w", key, action.ErrLimit)
		}

		edits = append(edits, action.Edit{Path: path, Value: set[key]})
	}

	return edits, nil
}

// covers reports whether limits.fields allows the field at path.
func (r *Rule) covers(path action.Path) bool {
	return slices.ContainsFunc(r.allowed, path.Within)
}

// inspect checks that the rule read from Grafana is one the action may
// change, and returns its definition.
func (r *Rule) inspect(uid string, live map[string]json.RawMessage) (json.RawMessage, error) {
	if err := r.checkRule(uid, live); err != nil {
		return nil, fmt.Errorf("check rule %s: %w", uid, err)
	}

	def, err := definition(live)
	if err != nil {
		return nil, fmt.Errorf("keep the writable fields: %w", err)
	}

	return def, nil
}

// checkRule checks that the rule read from Grafana is one the limits allow:
// it is in one of limits.folderUids, it is allowed by its UID or its title,
// and no one else manages it. A rule that Grafana reports a provenance for
// is made by files, Terraform or an API client; Grafana refuses a write that
// keeps the rule editable in the UI, and a write that does not would take
// the rule away from its owner, so the action does neither.
func (r *Rule) checkRule(uid string, live map[string]json.RawMessage) error {
	folder, _ := textOf(live["folderUID"])
	if !slices.Contains(r.limits.FolderUIDs, folder) {
		return fmt.Errorf("%w: %w", action.ErrLimit, errNotInFolder)
	}

	title, _ := textOf(live["title"])
	if !r.limits.ruleAllowed(uid, title, r.patterns) {
		return fmt.Errorf("%w: %w", action.ErrLimit, errNotAllowed)
	}

	if managed := provenance(live); managed != "" {
		return refuse("the rule is provisioned (provenance %q), so Grafana does not let Sidekick change it without taking it away from its owner. "+
			"Only the owner of the rule can allow changes, by removing the provisioning in Grafana, for example by managing the rule in the Grafana UI "+
			"instead of in files or Terraform. Do not propose another way to change this rule", source.CutText(managed, maxTitleShown))
	}

	if record, has := live["record"]; has && !isNull(record) {
		return fmt.Errorf("%w: %w", action.ErrLimit, errRecording)
	}

	return nil
}

// checkNew checks the new definition of a snapshot: Grafana must store it as
// written, the rule must stay one the limits allow, and it must differ from
// the old one. The last check comes after the others, because a value that
// Grafana replaces would otherwise look like no change.
func (r *Rule) checkNew(uid string, snap action.Snapshot, deltas []action.Delta) error {
	if err := checkChange(snap.Before, snap.After, deltas); err != nil {
		return fmt.Errorf("check that Grafana stores the change: %w", err)
	}

	if err := r.checkAfter(uid, snap.After); err != nil {
		return fmt.Errorf("check the rule after the change: %w", err)
	}

	same, err := sameDefinition(snap.Before, snap.After)
	if err != nil {
		return fmt.Errorf("compare the definitions: %w", err)
	}

	if same {
		return refuse("the rule has these values already")
	}

	return nil
}

// checkAfter checks that the rule is still one the limits allow once the
// change is made. A change must not take a rule out of the action's reach,
// for example by giving it a title no pattern matches: the model would have
// moved it beyond the operator's limits, and Undo could never reach it
// again. A rule allowed by its UID is always in reach. The folder cannot
// change, so only the title matters.
func (r *Rule) checkAfter(uid string, after json.RawMessage) error {
	title, ok := textOf(titleOf(after))
	if !ok {
		return refuse("the title after the change is not text")
	}

	if !r.limits.ruleAllowed(uid, title, r.patterns) {
		return refuse("the change would take rule %s out of limits.titlePatterns", uid)
	}

	return nil
}

// executeUpdate writes the after definition of snap to the rule.
func (r *Rule) executeUpdate(ctx context.Context, uid string, snap action.Snapshot) (action.Result, error) {
	live, err := r.client.get(ctx, uid)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the rule: %w", err)
	}

	cur, err := r.inspect(uid, live)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	if err := action.CheckFields(r.allowed, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the fields: %w", err)
	}

	if err := r.checkAfter(uid, snap.After); err != nil {
		return action.Result{}, fmt.Errorf("check the rule after the change: %w", err)
	}

	if err := checkPlacement(cur, snap.After); err != nil {
		return action.Result{}, fmt.Errorf("check the folder and group: %w", err)
	}

	done, err := holdsAfter(cur, snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule: %w", err)
	}

	if done {
		return r.changed(uid, snap.After, len(deltas)), nil
	}

	unchanged, err := sameDefinition(cur, snap.Before)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule with the snapshot: %w", err)
	}

	if !unchanged || version(live) != snap.Version {
		return action.Result{}, fmt.Errorf("rule %s: %w", uid, action.ErrConflict)
	}

	if err := r.client.update(ctx, uid, snap.After); err != nil {
		return action.Result{}, fmt.Errorf("write the change: %w", err)
	}

	return r.changed(uid, snap.After, len(deltas)), nil
}

// executeDelete deletes the rule of snap, unless it changed since.
func (r *Rule) executeDelete(ctx context.Context, uid string, snap action.Snapshot) (action.Result, error) {
	live, err := r.client.get(ctx, uid)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return action.Result{Summary: fmt.Sprintf("Rule %s is deleted.", uid)}, nil
	}

	if err != nil {
		return action.Result{}, fmt.Errorf("read the rule: %w", err)
	}

	cur, err := r.inspect(uid, live)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the rule against the limits: %w", err)
	}

	unchanged, err := sameDefinition(cur, snap.Before)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rule with the snapshot: %w", err)
	}

	if !unchanged || version(live) != snap.Version {
		return action.Result{}, fmt.Errorf("rule %s: %w", uid, action.ErrConflict)
	}

	if err := r.client.remove(ctx, uid); err != nil {
		return action.Result{}, fmt.Errorf("delete the rule: %w", err)
	}

	return action.Result{Summary: fmt.Sprintf("Deleted rule %s, %s.", uid, nameOf(snap.Before))}, nil
}

// changed is the result of a change that is in place.
func (r *Rule) changed(uid string, after json.RawMessage, fields int) action.Result {
	return r.result(uid, fmt.Sprintf("Changed %s of rule %s, %s.", fieldCount(fields), uid, nameOf(after)))
}

// restored is the result of a restore that is in place.
func (r *Rule) restored(uid string, doc json.RawMessage, fields int) action.Result {
	return r.result(uid, fmt.Sprintf("Restored %s of rule %s, %s.", fieldCount(fields), uid, nameOf(doc)))
}

// result is a result that links to the rule in Grafana.
func (r *Rule) result(uid, summary string) action.Result {
	return action.Result{
		Summary: summary,
		Links:   []action.Link{{Title: "Rule " + uid, URL: r.baseURL + "/alerting/grafana/" + url.PathEscape(uid) + "/view"}},
	}
}

// targetPrefix starts the Snapshot.Target of every rule.
const targetPrefix = "grafana:alert_rule/"

// target names the rule, as Snapshot.Target.
func target(uid string) string {
	return targetPrefix + uid
}

// checkPlacement checks that the new definition keeps the rule in the
// folder and the group it is in: sending another would move it.
func checkPlacement(cur, after json.RawMessage) error {
	oldRule, _ := objectOf(cur)
	newRule, _ := objectOf(after)

	for _, key := range []string{"folderUID", "ruleGroup"} {
		oldValue, _ := textOf(oldRule[key])
		newValue, _ := textOf(newRule[key])

		if oldValue != newValue {
			return fmt.Errorf("%w: %w", action.ErrLimit, errMoves)
		}
	}

	return nil
}

// titleOf returns the title field of a definition.
func titleOf(doc json.RawMessage) json.RawMessage {
	rule, _ := objectOf(doc)

	return rule["title"]
}

// nameOf is the title of the rule in a definition, cut, for a result. It is
// text from Grafana.
func nameOf(doc json.RawMessage) string {
	title, ok := textOf(titleOf(doc))
	if !ok || title == "" {
		return "no title"
	}

	return strconv.Quote(source.CutText(title, maxTitleShown))
}

// fieldCount writes a number of fields, such as "1 field" or "3 fields".
func fieldCount(n int) string {
	if n == 1 {
		return "1 field"
	}

	return strconv.Itoa(n) + " fields"
}
