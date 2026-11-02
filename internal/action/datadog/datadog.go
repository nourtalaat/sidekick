// Package datadog is the action datadog.monitor: it changes a Datadog
// monitor in place through the Datadog API, or deletes it, after an
// approver approved the change, and it can restore the fields it changed.
// It is the model for the monitor actions that follow it: the parameters
// say which fields to set, Snapshot reads the monitor and computes the new
// definition, Execute writes what the approver read, and Undo puts back
// only the fields the action changed. docs/plugins/actions.md explains the
// steps.
//
// What is not confirmed against a real Datadog account, and is marked with
// ASSUMPTION below: the answer of the validate endpoint, that a PUT with
// options replaces options as a whole, and the form of the modified field.
package datadog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// "type: datadog.monitor".
const Type = "datadog.monitor"

// maxNameBytes is the longest monitor name written into a result.
const maxNameBytes = 80

// definitionKeys are the fields of a monitor that a snapshot keeps. Read-only
// fields such as the id, the creator and the timestamps are left out, so the
// definition holds only what the action can write.
var definitionKeys = []string{"name", "type", "query", "message", "tags", "priority", "options"}

var (
	errNoSecrets    = errors.New("no secrets resolver to read the keys with")
	errBothChanges  = errors.New("give either set or delete, not both")
	errNoChange     = errors.New("give set with the fields to change, or delete")
	errNotAllowed   = errors.New("monitorId is not one of limits.monitorIds, and the monitor has none of limits.monitorTags")
	errDeleteLimit  = errors.New("delete needs limits.allowDelete")
	errTooManyKeys  = fmt.Errorf("set names more than %d fields", action.MaxChanges)
	errTargetDiffer = errors.New("the snapshot is of another monitor")
	errNotUndoable  = errors.New("a deleted monitor cannot be restored by Sidekick")
	errNoSnapshot   = errors.New("the proposal has no snapshot")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["monitorId"],
  "properties": {
    "monitorId": {"type": "integer", "minimum": 1, "description": "The ID of the monitor."},
    "set": {
      "type": "object",
      "description": "The fields to change: each key is a field path of the monitor, such as options.thresholds.critical, message or tags, and each value is the new value of that field.",
      "minProperties": 1
    },
    "delete": {"type": "boolean", "description": "Delete the monitor. Give this or set, not both."}
  }
}`)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	MonitorID int64                      `json:"monitorId"`
	Set       map[string]json.RawMessage `json:"set"`
	Delete    bool                       `json:"delete"`
}

// Monitor is one configured datadog.monitor action.
type Monitor struct {
	name    string
	limits  Limits
	allowed []action.Path
	appHost string
	client  *client
}

var (
	_ action.Action      = (*Monitor)(nil)
	_ action.Snapshotter = (*Monitor)(nil)
	_ action.Undoer      = (*Monitor)(nil)
)

// New builds the action from its validated config. It does not call
// Datadog: a service that is down must not stop Sidekick from starting.
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

	m := &Monitor{
		name:    name,
		limits:  cfg.Limits,
		allowed: allowed,
		client:  &client{http: hc, baseURL: cfg.apiURL(), apiKey: cfg.APIKeySecret, appKey: cfg.AppKeySecret, secrets: deps.Secrets},
	}

	if cfg.URL == "" {
		m.appHost = appHost(cfg.site())
	}

	return m, nil
}

// Spec implements action.Action. The description tells the model which
// monitors and fields it may touch.
func (m *Monitor) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Changes fields of a Datadog monitor through the Datadog API")

	if m.limits.AllowDelete {
		b.WriteString(", or deletes it. A delete cannot be undone in Sidekick")
	}

	b.WriteString(". Before it is proposed, Datadog checks the new definition. ")

	if len(m.limits.MonitorIDs) > 0 {
		b.WriteString("Allowed monitor IDs: " + joinInts(m.limits.MonitorIDs) + ". ")
	}

	if len(m.limits.MonitorTags) > 0 {
		b.WriteString("Monitors with one of the tags: " + strings.Join(m.limits.MonitorTags, ", ") + " are allowed too. ")
	}

	if len(m.limits.Fields) > 0 {
		b.WriteString("Fields that may change, with every field below them: " + strings.Join(m.limits.Fields, ", ") + ". ")
	}

	b.WriteString("Give set as field path to new value; an array such as tags is replaced whole.")

	return action.Spec{Name: m.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It runs without calling Datadog, so it
// checks what the parameters alone show: the monitor ID against
// limits.monitorIds, and each field of set against limits.fields. A monitor
// that is allowed by its tags is checked when the monitor is read. A limit
// violation wraps action.ErrLimit.
func (m *Monitor) Validate(_ context.Context, raw json.RawMessage) error {
	if _, _, err := m.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// parse decodes the parameters and checks them against the limits. It
// returns them with the edits set describes, in order of field path.
func (m *Monitor) parse(raw json.RawMessage) (Params, []action.Edit, error) {
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return Params{}, nil, fmt.Errorf("decode the parameters: %w", err)
	}

	if len(m.limits.MonitorTags) == 0 && !slices.Contains(m.limits.MonitorIDs, p.MonitorID) {
		return Params{}, nil, fmt.Errorf("monitor %d: %w: %w", p.MonitorID, action.ErrLimit, errNotAllowed)
	}

	switch {
	case p.Delete && len(p.Set) > 0:
		return Params{}, nil, errBothChanges
	case p.Delete:
		if !m.limits.AllowDelete {
			return Params{}, nil, fmt.Errorf("%w: %w", action.ErrLimit, errDeleteLimit)
		}

		return p, nil, nil
	case len(p.Set) == 0:
		return Params{}, nil, errNoChange
	case len(p.Set) > action.MaxChanges:
		return Params{}, nil, errTooManyKeys
	}

	edits, err := m.edits(p.Set)
	if err != nil {
		return Params{}, nil, fmt.Errorf("check the fields to set: %w", err)
	}

	return p, edits, nil
}

// edits turns the keys of set into edits, in key order, after checking each
// against limits.fields.
func (m *Monitor) edits(set map[string]json.RawMessage) ([]action.Edit, error) {
	edits := make([]action.Edit, 0, len(set))

	for _, key := range slices.Sorted(maps.Keys(set)) {
		path, err := action.ParsePath(key)
		if err != nil {
			return nil, fmt.Errorf("set: %w", err)
		}

		if !m.covers(path) {
			return nil, fmt.Errorf("the field %q is not one of limits.fields: %w", key, action.ErrLimit)
		}

		edits = append(edits, action.Edit{Path: path, Value: set[key]})
	}

	return edits, nil
}

// covers reports whether limits.fields allows the field at path.
func (m *Monitor) covers(path action.Path) bool {
	return slices.ContainsFunc(m.allowed, path.Within)
}

// Snapshot implements action.Snapshotter. It reads the monitor, checks it
// against the limits, computes the new definition from set, checks every
// field that differs against limits.fields, and asks Datadog to validate
// the new definition. For a delete the after definition is null.
func (m *Monitor) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	p, edits, err := m.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	live, err := m.client.get(ctx, p.MonitorID)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the monitor: %w", err)
	}

	if err := m.checkMonitor(p.MonitorID, live); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the monitor against the limits: %w", err)
	}

	before, err := definition(live)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	snap := action.Snapshot{Target: target(p.MonitorID), Before: before, Version: version(live)}

	if p.Delete {
		return snap, nil
	}

	snap.After, err = action.Apply(before, edits)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("apply set to the monitor: %w: %w", action.ErrLimit, err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("compare the definitions: %w", err)
	}

	if err := action.CheckFields(m.allowed, deltas); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the fields: %w", err)
	}

	if err := m.checkAfter(p.MonitorID, snap.After); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the monitor after the change: %w", err)
	}

	if err := m.client.validate(ctx, p.MonitorID, snap.After); err != nil {
		return action.Snapshot{}, fmt.Errorf("ask Datadog to validate the new definition: %w", err)
	}

	return snap, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the monitor, and writes the after definition of the snapshot, unless the
// monitor changed since the snapshot (action.ErrConflict). When the monitor
// already has the fields the snapshot sets, which happens when a worker died
// after the write, it writes nothing and succeeds.
func (m *Monitor) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, _, err := m.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if inv.Snapshot == nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if inv.Snapshot.Target != target(p.MonitorID) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	if inv.Snapshot.Deletes() {
		return m.executeDelete(ctx, p.MonitorID, *inv.Snapshot)
	}

	return m.executeUpdate(ctx, p.MonitorID, *inv.Snapshot)
}

// executeUpdate writes the after definition of snap to the monitor.
func (m *Monitor) executeUpdate(ctx context.Context, id int64, snap action.Snapshot) (action.Result, error) {
	live, err := m.client.get(ctx, id)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the monitor: %w", err)
	}

	if err := m.checkMonitor(id, live); err != nil {
		return action.Result{}, fmt.Errorf("check the monitor against the limits: %w", err)
	}

	cur, err := definition(live)
	if err != nil {
		return action.Result{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	if err := action.CheckFields(m.allowed, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the fields: %w", err)
	}

	if err := m.checkAfter(id, snap.After); err != nil {
		return action.Result{}, fmt.Errorf("check the monitor after the change: %w", err)
	}

	done, err := action.AtAfter(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the monitor: %w", err)
	}

	if done {
		return m.changed(id, snap.After, len(deltas)), nil
	}

	unchanged, err := sameDefinition(cur, snap.Before)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the monitor with the snapshot: %w", err)
	}

	if !unchanged || version(live) != snap.Version {
		return action.Result{}, fmt.Errorf("monitor %d: %w", id, action.ErrConflict)
	}

	body, err := fieldsOf(snap.After, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("choose the fields to write: %w", err)
	}

	if err := m.client.update(ctx, id, body); err != nil {
		return action.Result{}, fmt.Errorf("write the change: %w", err)
	}

	return m.changed(id, snap.After, len(deltas)), nil
}

// executeDelete deletes the monitor of snap, unless it changed since.
func (m *Monitor) executeDelete(ctx context.Context, id int64, snap action.Snapshot) (action.Result, error) {
	live, err := m.client.get(ctx, id)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return action.Result{Summary: fmt.Sprintf("Monitor %d is deleted.", id)}, nil
	}

	if err != nil {
		return action.Result{}, fmt.Errorf("read the monitor: %w", err)
	}

	if err := m.checkMonitor(id, live); err != nil {
		return action.Result{}, fmt.Errorf("check the monitor against the limits: %w", err)
	}

	cur, err := definition(live)
	if err != nil {
		return action.Result{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	unchanged, err := sameDefinition(cur, snap.Before)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the monitor with the snapshot: %w", err)
	}

	if !unchanged || version(live) != snap.Version {
		return action.Result{}, fmt.Errorf("monitor %d: %w", id, action.ErrConflict)
	}

	if err := m.client.remove(ctx, id); err != nil {
		return action.Result{}, fmt.Errorf("delete the monitor: %w", err)
	}

	return action.Result{Summary: fmt.Sprintf("Deleted monitor %d, %s.", id, nameOf(snap.Before))}, nil
}

// Undo implements action.Undoer. It puts back the fields the action
// changed, from snap.Before, and leaves every other field of the monitor as
// it is now. When the monitor already has the old values it writes nothing
// and succeeds. When someone changed one of the fields since, and force is
// false, it writes nothing and returns an *action.ConflictError with the
// monitor as it is. A delete cannot be undone.
func (m *Monitor) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	p, _, err := m.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	if snap.Deletes() || len(snap.After) == 0 {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNotUndoable)
	}

	live, err := m.client.get(ctx, p.MonitorID)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the monitor: %w", err)
	}

	if err := m.checkMonitor(p.MonitorID, live); err != nil {
		return action.Result{}, fmt.Errorf("check the monitor against the limits: %w", err)
	}

	cur, err := definition(live)
	if err != nil {
		return action.Result{}, fmt.Errorf("keep the writable fields: %w", err)
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	if err := action.CheckFields(m.allowed, deltas); err != nil {
		return action.Result{}, fmt.Errorf("check the fields: %w", err)
	}

	restored, err := action.AtBefore(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the monitor: %w", err)
	}

	if restored {
		return m.restored(p.MonitorID, cur, len(deltas)), nil
	}

	asWritten, err := action.AtAfter(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the monitor: %w", err)
	}

	if !asWritten && !force {
		return action.Result{}, &action.ConflictError{Live: cur}
	}

	back, err := action.Revert(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("put the fields back: %w", err)
	}

	body, err := fieldsOf(back, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("choose the fields to write: %w", err)
	}

	if err := m.client.update(ctx, p.MonitorID, body); err != nil {
		return action.Result{}, fmt.Errorf("write the restore: %w", err)
	}

	return m.restored(p.MonitorID, back, len(deltas)), nil
}

// checkMonitor checks that the monitor read from Datadog is one the limits
// allow: its ID is in limits.monitorIDs, or it has a tag of limits.monitorTags.
func (m *Monitor) checkMonitor(id int64, live map[string]json.RawMessage) error {
	if slices.Contains(m.limits.MonitorIDs, id) {
		return nil
	}

	var tags []string
	if raw, ok := live["tags"]; ok {
		if err := json.Unmarshal(raw, &tags); err != nil {
			return fmt.Errorf("read the tags of monitor %d: %w", id, err)
		}
	}

	if m.hasAllowedTag(tags) {
		return nil
	}

	return fmt.Errorf("monitor %d: %w: %w", id, action.ErrLimit, errNotAllowed)
}

// checkAfter checks that the monitor is still one the limits allow once the
// change is made. A change must not take a monitor out of the action's
// reach, for example by removing the tag that allows it: the model would
// have moved it beyond the operator's limits, and Undo could never reach it
// again. A monitor allowed by its ID is always in reach.
func (m *Monitor) checkAfter(id int64, after json.RawMessage) error {
	if slices.Contains(m.limits.MonitorIDs, id) {
		return nil
	}

	var parsed struct {
		Tags []string `json:"tags"`
	}

	if err := json.Unmarshal(after, &parsed); err != nil {
		return fmt.Errorf("the tags after the change are not a list of text: %w: %w", action.ErrLimit, err)
	}

	if m.hasAllowedTag(parsed.Tags) {
		return nil
	}

	return fmt.Errorf("the change would take monitor %d out of limits.monitorTags: %w", id, action.ErrLimit)
}

// hasAllowedTag reports whether one of tags is in limits.monitorTags.
func (m *Monitor) hasAllowedTag(tags []string) bool {
	return slices.ContainsFunc(tags, func(t string) bool { return slices.Contains(m.limits.MonitorTags, t) })
}

// changed is the result of a change that is in place.
func (m *Monitor) changed(id int64, after json.RawMessage, fields int) action.Result {
	return m.result(id, fmt.Sprintf("Changed %s of monitor %d, %s.", fieldCount(fields), id, nameOf(after)))
}

// restored is the result of a restore that is in place.
func (m *Monitor) restored(id int64, doc json.RawMessage, fields int) action.Result {
	return m.result(id, fmt.Sprintf("Restored %s of monitor %d, %s.", fieldCount(fields), id, nameOf(doc)))
}

// result is a result that links to the monitor in Datadog, when the link
// is known.
func (m *Monitor) result(id int64, summary string) action.Result {
	res := action.Result{Summary: summary}

	if m.appHost != "" {
		res.Links = []action.Link{{Title: "Monitor " + strconv.FormatInt(id, 10), URL: "https://" + m.appHost + "/monitors/" + strconv.FormatInt(id, 10)}}
	}

	return res
}

// target names the monitor, as Snapshot.Target.
func target(id int64) string {
	return "datadog:monitor/" + strconv.FormatInt(id, 10)
}

// definition keeps the fields of a monitor that the action can write.
func definition(live map[string]json.RawMessage) (json.RawMessage, error) {
	kept := map[string]json.RawMessage{}

	for _, key := range definitionKeys {
		if v, ok := live[key]; ok {
			kept[key] = v
		}
	}

	raw, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("encode the definition: %w", err)
	}

	return raw, nil
}

// version is the time Datadog says the monitor was last modified, or "".
// ASSUMPTION: the monitor has a modified field, a string such as
// 2026-10-29T10:15:30.123456+00:00. Only equality is used, so its form
// does not matter.
func version(live map[string]json.RawMessage) string {
	var modified string
	if err := json.Unmarshal(live["modified"], &modified); err != nil {
		return ""
	}

	return modified
}

// sameDefinition reports whether two definitions are the same.
func sameDefinition(a, b json.RawMessage) (bool, error) {
	deltas, err := action.Diff(a, b)
	if err != nil {
		return false, fmt.Errorf("compare the definitions: %w", err)
	}

	return len(deltas) == 0, nil
}

// fieldsOf returns what to send to Datadog to write the fields of deltas:
// for each top-level field they touch, its whole value in doc, or null when
// doc has none. ASSUMPTION: a PUT sets each field it names as a whole, so
// options is sent whole, and a field it does not name stays as it is.
func fieldsOf(doc json.RawMessage, deltas []action.Delta) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return nil, fmt.Errorf("decode the definition: %w", err)
	}

	body := map[string]json.RawMessage{}

	for _, d := range deltas {
		if len(d.Path) == 0 {
			continue
		}

		root := d.Path[0]
		if v, ok := top[root]; ok {
			body[root] = v
		} else {
			body[root] = json.RawMessage("null")
		}
	}

	return body, nil
}

// nameOf is the name of the monitor in a definition, cut, for a result. It
// is text from Datadog.
func nameOf(doc json.RawMessage) string {
	var d struct {
		Name string `json:"name"`
	}

	if json.Unmarshal(doc, &d) != nil || d.Name == "" {
		return "no name"
	}

	return strconv.Quote(source.CutText(d.Name, maxNameBytes))
}

// fieldCount writes a number of fields, such as "1 field" or "3 fields".
func fieldCount(n int) string {
	if n == 1 {
		return "1 field"
	}

	return strconv.Itoa(n) + " fields"
}

// joinInts writes IDs separated by commas.
func joinInts(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}

	return strings.Join(parts, ", ")
}
