// Package prometheusrule is the action prometheus.rule: it changes alerting
// and recording rules of a PrometheusRule resource (Prometheus Operator)
// through the Kubernetes API, after an approver approved the change, and it
// can restore the fields it changed. It follows the model of datadog.monitor:
// the parameters say which fields of which rules to set, Snapshot reads the
// resource and computes the new definition, Execute writes what the approver
// read, and Undo puts back only the fields the action changed.
//
// The rules are named by group and by alert or record name, never by their
// position in the list, and a snapshot holds only the rules a change names,
// as an object from group to rule to the editable fields.
//
// What the action relies on, as the public source of Kubernetes (tag
// v1.37.1), the Prometheus Operator (tag v0.94.1) and Prometheus (tag
// v3.15.0) show it:
//
//   - A JSON merge patch (RFC 7386) is accepted for custom resources
//     (staging/src/k8s.io/apiextensions-apiserver/pkg/apiserver/customresource_handler.go),
//     and a list in it replaces the list whole (staging/src/k8s.io/apiserver/pkg/endpoints/handlers/patch.go,
//     jsonpatch.MergePatch). So the patch holds all of spec.groups, as read
//     with the change made, and the action never relies on how a partial
//     list is merged.
//   - A patch that holds metadata.resourceVersion is refused with 409 when
//     the stored one differs (staging/src/k8s.io/apiserver/pkg/registry/generic/registry/store.go,
//     in Update). The action reads the resource right before it writes and
//     sends the version it read, so the API server guards the moment between
//     the read and the write. The version of the snapshot is only recorded:
//     it moves with every write to the resource, including status writes of
//     the operator and edits of other rules, so it is not compared.
//   - A dry run (dryRun=All) answers with the object as it would be stored.
//     The action compares the rules in the answer with the change, so a
//     change the server would drop or alter is refused before it is made.
//     Unknown fields are dropped by the CRD schema (example/prometheus-operator-crd/monitoring.coreos.com_prometheusrules.yaml
//     of the Operator), and expr may be a number or a text, which
//     normalise.go makes text.
//   - spec.groups is a list keyed by name in the CRD, so group names are
//     unique. Rule names are not: a group can hold two alerts with the same
//     name, and the action refuses to address them.
//   - promtool check rules (model/rulefmt/rulefmt.go of Prometheus) reads the
//     group fields name, interval, query_offset, limit, labels and rules, and
//     refuses others, such as the Thanos field partial_response_strategy, so
//     the file sent to the validator holds only those.
//   - A token for a service account comes from the TokenRequest API, which
//     gives tokens that live at least 10 minutes
//     (pkg/apis/authentication/validation/validation.go), and RBAC can limit
//     the request to named accounts because the account is in the URL
//     (staging/src/k8s.io/apiserver/pkg/endpoints/request/requestinfo.go).
package prometheusrule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Type is the action type this package registers. A config selects it with
// "type: prometheus.rule".
const Type = "prometheus.rule"

const (
	// maxRules is the most rules one proposal may change.
	maxRules = 20
	// maxListedRules is the most rule names an error lists for the model.
	maxListedRules = 30
	// maxProblems is the most validator problems an error lists.
	maxProblems = 10
	// maxDifferences is the most fields an error lists when the API server
	// would store the rules in another way.
	maxDifferences = 5
)

var (
	errRejected  = errors.New("the change was refused")
	errNoRules   = errors.New("rules: give at least one rule")
	errTooMany   = fmt.Errorf("rules: give at most %d rules", maxRules)
	errBothKinds = errors.New("rules: give either alert or record for a rule, not both")
	errNoKind    = errors.New("rules: give alert or record for a rule")
	errNoSet     = errors.New("rules: give set with the fields to change")
	errRepeated  = errors.New("rules: a rule is listed twice")
	errBracket   = errors.New("rules: a group or rule name must not start with a bracket")
	errNoSnap    = errors.New("the proposal has no snapshot")
	errTarget    = errors.New("the snapshot is of another resource")
	errDelete    = errors.New("deleting a rule is not supported")
	errGone      = errors.New("a rule is not there any more")
)

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate.
var paramsSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["namespace", "name", "rules"],
  "properties": {
    "namespace": {"type": "string", "minLength": 1, "maxLength": 63, "description": "The namespace of the PrometheusRule."},
    "name": {"type": "string", "minLength": 1, "maxLength": 253, "description": "The name of the PrometheusRule resource."},
    "rules": {
      "type": "array",
      "minItems": 1,
      "maxItems": 20,
      "description": "The rules to change.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["group", "set"],
        "properties": {
          "group": {"type": "string", "minLength": 1, "maxLength": 253, "description": "The name of the rule group."},
          "alert": {"type": "string", "minLength": 1, "maxLength": 253, "description": "The name of the alerting rule. Give this or record."},
          "record": {"type": "string", "minLength": 1, "maxLength": 253, "description": "The name of the recording rule. Give this or alert."},
          "set": {
            "type": "object",
            "minProperties": 1,
            "description": "The fields to change: each key is a field of the rule, such as expr, for, keep_firing_for, labels, annotations, labels.severity or annotations.summary, and each value is the new value. A null value removes the field."
          }
        }
      }
    }
  }
}`)

// init registers the action when the package is linked in.
func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Params are what the model fills in when it proposes the action.
type Params struct {
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Rules     []RuleChange `json:"rules"`
}

// RuleChange is the change to one rule, named by its group and by its alert
// or record name.
type RuleChange struct {
	Group  string                     `json:"group"`
	Alert  string                     `json:"alert"`
	Record string                     `json:"record"`
	Set    map[string]json.RawMessage `json:"set"`
}

// Rule is one configured prometheus.rule action.
type Rule struct {
	name      string
	limits    Limits
	allowed   []action.Path
	cluster   *cluster
	validator validator
}

// validator checks rule files. validateclient.Validator is one; the interface
// is small so that another way to reach the validator can replace it in one
// line, in New.
type validator interface {
	Validate(ctx context.Context, req validateclient.Request) (validateclient.Result, error)
}

var (
	_ action.Action          = (*Rule)(nil)
	_ action.ProposalChecker = (*Rule)(nil)
	_ action.Snapshotter     = (*Rule)(nil)
	_ action.Undoer          = (*Rule)(nil)
)

// request is a parsed proposal.
type request struct {
	params Params
	refs   []ruleRef
	edits  []action.Edit
}

// plan is a change worked out against the resource as it is.
type plan struct {
	before, after json.RawMessage
	deltas        []action.Delta
	// groups is spec.groups with the change made.
	groups groupList
}

// New builds the action from its validated config. It makes no call to the
// API server, but it fails outside a cluster, because the action uses the
// pod's service account to ask for tokens.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	cl, err := inCluster(cfg.serviceAccount())
	if err != nil {
		return nil, fmt.Errorf("prepare the Kubernetes client: %w", err)
	}

	checker := deps.Validator
	if checker == nil {
		checker = validateclient.New(deps.ValidatorURL, deps.HTTP)
	}

	r, err := newRule(name, cfg, cl, checker)
	if err != nil {
		return nil, fmt.Errorf("build the action: %w", err)
	}

	return r, nil
}

// newRule builds the action around a way to reach the cluster and the
// validator.
func newRule(name string, cfg Config, cl *cluster, checker validator) (*Rule, error) {
	allowed, err := action.ParsePaths(cfg.Limits.Fields)
	if err != nil {
		return nil, fmt.Errorf("limits.fields: %w", err)
	}

	return &Rule{name: name, limits: cfg.Limits, allowed: allowed, cluster: cl, validator: checker}, nil
}

// Spec implements action.Action. The description tells the model which
// resources, groups and fields it may touch.
func (r *Rule) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Changes fields of alerting and recording rules in a PrometheusRule (Prometheus Operator) through the Kubernetes API. ")
	b.WriteString("Name each rule by its group and its alert or record name. ")
	b.WriteString("Before it is proposed, the new rules are checked with promtool and with a dry run on the API server. ")
	b.WriteString("Allowed namespaces: " + strings.Join(r.limits.Namespaces, ", ") + ". ")
	b.WriteString("Allowed PrometheusRule names: " + strings.Join(r.limits.Names, ", ") + ". ")
	b.WriteString("Allowed rule groups: " + strings.Join(r.limits.Groups, ", ") + ". ")
	b.WriteString("Fields that may change, with every field below them: " + strings.Join(r.limits.Fields, ", ") + ". ")
	b.WriteString("Give set as field to new value, for example {\"for\": \"10m\", \"labels.severity\": \"critical\"}. " +
		"A label name with a dot cannot be set on its own: set labels whole. A null value removes a field. " +
		"Rules cannot be added, removed or renamed.")

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It runs without calling the cluster,
// so it checks what the parameters alone show: the namespace, the resource
// name, each group and each field of set against the limits. A limit
// violation wraps action.ErrLimit.
func (r *Rule) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := r.parse(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// CheckProposal implements action.ProposalChecker. It reads the resource,
// works out the change, asks the validator to check the rule groups with
// promtool, and asks the API server for a dry run, which must store the
// rules as the change says. It runs apart from Snapshot because the
// validator can take a minute.
func (r *Rule) CheckProposal(ctx context.Context, raw json.RawMessage) error {
	req, err := r.parse(raw)
	if err != nil {
		return fmt.Errorf("validate the parameters: %w", err)
	}

	sess, res, err := r.read(ctx, req)
	if err != nil {
		return fmt.Errorf("read the PrometheusRule: %w", err)
	}

	p, err := r.plan(req, res)
	if err != nil {
		return fmt.Errorf("work out the change: %w", err)
	}

	if err := r.verify(ctx, sess, req, res, p.groups, p.after); err != nil {
		return fmt.Errorf("check the new rules: %w", err)
	}

	return nil
}

// Snapshot implements action.Snapshotter. It reads the resource, keeps the
// rules the change names as the before definition, computes the after
// definition from set, and refuses a difference outside limits.fields, a
// field of the wrong type, or a difference too large to review. The version
// is the resourceVersion that was read; it is recorded and not compared.
func (r *Rule) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	req, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	_, res, err := r.read(ctx, req)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the PrometheusRule: %w", err)
	}

	p, err := r.plan(req, res)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("work out the change: %w", err)
	}

	return action.Snapshot{Target: target(req.params), Before: p.before, After: p.after, Version: res.version}, nil
}

// Execute implements action.Action. It checks the parameters again, reads
// the resource, and writes the after definition of the snapshot, unless the
// rules changed since the snapshot (action.ErrConflict). When the rules are
// at the after definition already, which happens when a worker died after
// the write, it writes nothing and succeeds. Before the write it asks the
// validator and the API server for a dry run again. The write holds the
// resourceVersion that was just read, so the API server answers 409 if the
// resource changes in between.
func (r *Rule) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	req, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	snap, err := r.checkSnapshot(req, inv.Snapshot)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the snapshot: %w", err)
	}

	sess, res, err := r.read(ctx, req)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("read the PrometheusRule: %w", err))
	}

	deltas, err := r.snapshotDeltas(snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	cur, missing, err := res.groups.view(req.refs)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrConflict, err)
	}

	if len(missing) > 0 {
		return action.Result{}, fmt.Errorf("%s: %w", describe(missing), action.ErrConflict)
	}

	done, err := action.AtAfter(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rules: %w", err)
	}

	if done {
		return r.result(req.params, "Changed", len(deltas)), nil
	}

	unchanged, err := sameDefinition(cur, snap.Before)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rules with the snapshot: %w", err)
	}

	if !unchanged {
		return action.Result{}, fmt.Errorf("PrometheusRule %s/%s: %w", req.params.Namespace, req.params.Name, action.ErrConflict)
	}

	if err := r.write(ctx, sess, req, res, deltas, afterValue, snap.After); err != nil {
		return action.Result{}, fmt.Errorf("write the change: %w", err)
	}

	return r.result(req.params, "Changed", len(deltas)), nil
}

// Undo implements action.Undoer. It puts back the fields the action changed,
// from snap.Before, and leaves every other field as it is now. When the rules
// already have the old values it writes nothing and succeeds. When someone
// changed one of the fields since, and force is false, it writes nothing and
// returns an *action.ConflictError with the rules as they are.
func (r *Rule) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	req, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	if snap.Deletes() || len(snap.After) == 0 {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errDelete)
	}

	if snap.Target != target(req.params) {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTarget)
	}

	sess, res, err := r.read(ctx, req)
	if err != nil {
		return action.Result{}, classify(fmt.Errorf("read the PrometheusRule: %w", err))
	}

	deltas, err := r.snapshotDeltas(snap)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the snapshot: %w", err)
	}

	cur, missing, err := res.groups.view(req.refs)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrConflict, err)
	}

	if len(missing) > 0 {
		if !force {
			return action.Result{}, &action.ConflictError{Live: cur}
		}

		return action.Result{}, fmt.Errorf("%s: %w: %w", describe(missing), action.ErrPermanent, errGone)
	}

	restored, err := action.AtBefore(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rules: %w", err)
	}

	if restored {
		return r.result(req.params, "Restored", len(deltas)), nil
	}

	asWritten, err := action.AtAfter(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare the rules: %w", err)
	}

	if !asWritten && !force {
		return action.Result{}, &action.ConflictError{Live: cur}
	}

	back, err := action.Revert(cur, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("put the fields back: %w", err)
	}

	if err := r.write(ctx, sess, req, res, deltas, beforeValue, back); err != nil {
		return action.Result{}, fmt.Errorf("write the restore: %w", err)
	}

	return r.result(req.params, "Restored", len(deltas)), nil
}

// parse decodes the parameters and checks them against the limits. It
// returns them with the rules they name and the edits set describes.
func (r *Rule) parse(raw json.RawMessage) (request, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return request{}, fmt.Errorf("decode the parameters: %w", err)
	}

	if !slices.Contains(r.limits.Namespaces, p.Namespace) {
		return request{}, fmt.Errorf("namespace %q is not one of limits.namespaces: %w", p.Namespace, action.ErrLimit)
	}

	if !slices.Contains(r.limits.Names, p.Name) {
		return request{}, fmt.Errorf("PrometheusRule %q is not one of limits.names: %w", p.Name, action.ErrLimit)
	}

	switch {
	case len(p.Rules) == 0:
		return request{}, errNoRules
	case len(p.Rules) > maxRules:
		return request{}, errTooMany
	}

	req := request{params: p}

	for _, change := range p.Rules {
		ref, err := r.ruleRef(change)
		if err != nil {
			return request{}, fmt.Errorf("check a rule: %w", err)
		}

		if slices.ContainsFunc(req.refs, func(o ruleRef) bool { return o.group == ref.group && o.name == ref.name }) {
			return request{}, fmt.Errorf("%s: %w", ref, errRepeated)
		}

		edits, err := r.edits(ref, change.Set)
		if err != nil {
			return request{}, fmt.Errorf("check the fields to set of the %s: %w", ref, err)
		}

		req.refs = append(req.refs, ref)
		req.edits = append(req.edits, edits...)
	}

	if len(req.edits) > action.MaxChanges {
		return request{}, fmt.Errorf("set names more than %d fields in all: %w", action.MaxChanges, action.ErrLimit)
	}

	return req, nil
}

// ruleRef checks the name of one rule against the limits.
func (r *Rule) ruleRef(change RuleChange) (ruleRef, error) {
	ref := ruleRef{group: change.Group, name: change.Alert, record: change.Record != ""}

	switch {
	case change.Alert != "" && change.Record != "":
		return ruleRef{}, errBothKinds
	case change.Alert == "" && change.Record == "":
		return ruleRef{}, errNoKind
	case ref.record:
		ref.name = change.Record
	}

	if !slices.Contains(r.limits.Groups, ref.group) {
		return ruleRef{}, fmt.Errorf("group %q is not one of limits.groups: %w", ref.group, action.ErrLimit)
	}

	if strings.HasPrefix(ref.group, "[") || strings.HasPrefix(ref.name, "[") {
		return ruleRef{}, errBracket
	}

	if len(change.Set) == 0 {
		return ruleRef{}, errNoSet
	}

	return ref, nil
}

// edits turns the keys of set into edits of one rule, in key order, after
// checking each against limits.fields. A null value removes the field.
func (r *Rule) edits(ref ruleRef, set map[string]json.RawMessage) ([]action.Edit, error) {
	edits := make([]action.Edit, 0, len(set))

	for _, key := range slices.Sorted(maps.Keys(set)) {
		path, err := action.ParsePath(key)
		if err != nil {
			return nil, fmt.Errorf("set: %w", err)
		}

		if !slices.ContainsFunc(r.allowed, path.Within) {
			return nil, fmt.Errorf("the field %q is not one of limits.fields: %w", key, action.ErrLimit)
		}

		value := set[key]
		if isNull(value) {
			value = nil
		}

		edits = append(edits, action.Edit{Path: append(action.Path{ref.group, ref.name}, path...), Value: value})
	}

	return edits, nil
}

// read asks for a token and reads the resource.
func (r *Rule) read(ctx context.Context, req request) (*session, resource, error) {
	sess, err := r.cluster.open(ctx)
	if err != nil {
		return nil, resource{}, fmt.Errorf("get access: %w", err)
	}

	res, err := sess.get(ctx, req.params.Namespace, req.params.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, resource{}, fmt.Errorf("%w: %w", action.ErrLimit, err)
		}

		return nil, resource{}, fmt.Errorf("get the resource: %w", err)
	}

	return sess, res, nil
}

// plan works out the change on the resource as it is: the before and after
// definitions of the rules the request names, the fields that differ, and
// spec.groups with the change made. It refuses what the limits or the
// shapes of the fields do not allow. Every refusal wraps action.ErrLimit.
func (r *Rule) plan(req request, res resource) (plan, error) {
	before, missing, err := res.groups.view(req.refs)
	if err != nil {
		return plan{}, fmt.Errorf("%w: %w", action.ErrLimit, err)
	}

	if len(missing) > 0 {
		return plan{}, fmt.Errorf("PrometheusRule %s/%s has no %s; the rules in the allowed groups are: %s: %w",
			req.params.Namespace, req.params.Name, describe(missing),
			strings.Join(res.groups.names(r.limits.Groups, maxListedRules), ", "), action.ErrLimit)
	}

	after, err := action.Apply(before, req.edits)
	if err != nil {
		return plan{}, fmt.Errorf("apply set to the rules: %w: %w", action.ErrLimit, err)
	}

	if err := checkShapes(after); err != nil {
		return plan{}, fmt.Errorf("check the new rules: %w", err)
	}

	deltas, err := diff(before, after)
	if err != nil {
		return plan{}, fmt.Errorf("compare the definitions: %w", err)
	}

	if err := r.checkFields(deltas); err != nil {
		return plan{}, fmt.Errorf("check the fields: %w", err)
	}

	groups, err := res.groups.edit(req.refs, valueEdits(deltas, afterValue))
	if err != nil {
		return plan{}, fmt.Errorf("make the new rules: %w: %w", action.ErrLimit, err)
	}

	return plan{before: before, after: after, deltas: deltas, groups: groups}, nil
}

// checkFields checks that every field that differs is within limits.fields.
// The fields of limits.fields are relative to a rule, so the group and the
// rule name are cut off the front of each path.
func (r *Rule) checkFields(deltas []action.Delta) error {
	relative := make([]action.Delta, 0, len(deltas))

	for _, d := range deltas {
		if len(d.Path) <= ruleDepth {
			return fmt.Errorf("the change adds or removes a whole rule: %w", action.ErrLimit)
		}

		relative = append(relative, action.Delta{Path: d.Path[ruleDepth:]})
	}

	if err := action.CheckFields(r.allowed, relative); err != nil {
		return fmt.Errorf("check the paths: %w", err)
	}

	return nil
}

// checkSnapshot checks that the snapshot of an invocation is one this
// request can run: it exists, it is of this resource, and it does not delete.
func (*Rule) checkSnapshot(req request, snap *action.Snapshot) (action.Snapshot, error) {
	switch {
	case snap == nil:
		return action.Snapshot{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnap)
	case snap.Target != target(req.params):
		return action.Snapshot{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTarget)
	case snap.Deletes():
		return action.Snapshot{}, fmt.Errorf("%w: %w", action.ErrPermanent, errDelete)
	default:
		return *snap, nil
	}
}

// snapshotDeltas returns the fields that differ in a snapshot, after
// checking them against limits.fields as they are now and their shapes.
func (r *Rule) snapshotDeltas(snap action.Snapshot) ([]action.Delta, error) {
	deltas, err := diff(snap.Before, snap.After)
	if err != nil {
		return nil, fmt.Errorf("compare the definitions: %w", err)
	}

	if err := r.checkFields(deltas); err != nil {
		return nil, fmt.Errorf("check the fields: %w", err)
	}

	if err := checkShapes(snap.After); err != nil {
		return nil, fmt.Errorf("check the new rules: %w", err)
	}

	return deltas, nil
}

// write makes a change that Execute or Undo has worked out: it puts the
// values of deltas, picked by pick, into the live rules, checks the result
// with the validator and a dry run, and writes it. want is the definition
// the rules must have afterwards.
func (r *Rule) write(ctx context.Context, sess *session, req request, res resource, deltas []action.Delta, pick func(action.Delta) json.RawMessage, want json.RawMessage) error {
	groups, err := res.groups.edit(req.refs, valueEdits(deltas, pick))
	if err != nil {
		return fmt.Errorf("make the new rules: %w", err)
	}

	if err := r.verify(ctx, sess, req, res, groups, want); err != nil {
		return classify(fmt.Errorf("check the new rules: %w", err))
	}

	if _, err := sess.patch(ctx, req.params.Namespace, req.params.Name, groups, res.version, false); err != nil {
		return classify(fmt.Errorf("patch the resource: %w", err))
	}

	return nil
}

// verify checks the rule groups with the validator, and then with a dry run
// of the patch. The rules in the answer of the dry run must be want: if the
// API server would store them in another way, the change would not read back
// as it was made.
func (r *Rule) verify(ctx context.Context, sess *session, req request, res resource, groups groupList, want json.RawMessage) error {
	if err := r.validate(ctx, groups, req.refs); err != nil {
		return fmt.Errorf("validate with promtool: %w", err)
	}

	got, err := sess.patch(ctx, req.params.Namespace, req.params.Name, groups, res.version, true)
	if err != nil {
		return fmt.Errorf("dry run the patch: %w", err)
	}

	stored, missing, err := got.groups.view(req.refs)
	if err != nil {
		return fmt.Errorf("read the rules of the dry run: %w", err)
	}

	if len(missing) > 0 {
		return fmt.Errorf("the dry run lost %s: %w", describe(missing), errRejected)
	}

	differences, err := diff(want, stored)
	if err != nil {
		return fmt.Errorf("compare the dry run with the change: %w", err)
	}

	if len(differences) > 0 {
		return fmt.Errorf("the API server would store %s other than the change says (for example an old CRD without the field drops it): %w",
			listPaths(differences), errRejected)
	}

	return nil
}

// validate sends the groups of the rules to the validator, which checks them
// with promtool check rules.
func (r *Rule) validate(ctx context.Context, groups groupList, refs []ruleRef) error {
	file, err := groups.validationFile(refs)
	if err != nil {
		return fmt.Errorf("make the rules file: %w", err)
	}

	res, err := r.validator.Validate(ctx, validateclient.Request{
		Format: validateclient.FormatPrometheusRules,
		Files:  []validateclient.File{{Path: validationFilePath, Content: string(file)}},
	})
	if err != nil {
		return fmt.Errorf("call the validator: %w", err)
	}

	if !res.OK {
		return &validationError{problems: res.Errors}
	}

	return nil
}

// result is the result of a change that is in place.
func (*Rule) result(p Params, verb string, fields int) action.Result {
	return action.Result{Summary: fmt.Sprintf("%s %s in %d rule(s) of PrometheusRule %s/%s.", verb, fieldCount(fields), len(p.Rules), p.Namespace, p.Name)}
}

// validationError is what the validator found wrong with the rules.
type validationError struct {
	problems []validateclient.Problem
}

// Error lists the problems, one per line. The lines are in the rules file
// that Sidekick made from the groups, which holds the whole groups of the
// rules.
func (e *validationError) Error() string {
	var b strings.Builder

	b.WriteString("the validator found errors in the rule groups:")

	for i, p := range e.problems {
		if i == maxProblems {
			fmt.Fprintf(&b, "\n- and %d more", len(e.problems)-maxProblems)

			break
		}

		b.WriteString("\n- ")

		if p.Line > 0 {
			fmt.Fprintf(&b, "line %d: ", p.Line)
		}

		b.WriteString(strings.Join(strings.Fields(p.Message), " "))
	}

	return b.String()
}

// Unwrap lets callers see that the change was refused.
func (*validationError) Unwrap() error {
	return errRejected
}

// classify tells Sidekick what an error from the cluster or the validator
// means for the proposal: a conflict, a refusal that repeating cannot
// change, or a failure to try again.
func classify(err error) error {
	switch {
	case isConflict(err):
		return fmt.Errorf("%w: %w", action.ErrConflict, err)
	case isRejected(err), errors.Is(err, errRejected),
		errors.Is(err, validateclient.ErrBadRequest), errors.Is(err, validateclient.ErrTooLarge):
		return fmt.Errorf("%w: %w", action.ErrPermanent, err)
	default:
		return fmt.Errorf("will be tried again: %w", err)
	}
}

// diff is action.Diff with a message for the model when the change is too
// large to review.
func diff(before, after json.RawMessage) ([]action.Delta, error) {
	deltas, err := action.Diff(before, after)
	if errors.Is(err, action.ErrTooManyChanges) {
		return nil, fmt.Errorf("the change would differ in more than %d fields, which an approver cannot review; "+
			"change fewer rules or fields in one proposal, and split the rest into other proposals: %w", action.MaxChanges, err)
	}

	if err != nil {
		return nil, fmt.Errorf("compare: %w", err)
	}

	return deltas, nil
}

// sameDefinition reports whether two definitions of rules are the same.
func sameDefinition(a, b json.RawMessage) (bool, error) {
	deltas, err := action.Diff(a, b)
	if err != nil {
		return false, fmt.Errorf("compare the definitions: %w", err)
	}

	return len(deltas) == 0, nil
}

// afterValue and beforeValue pick the value a delta writes.
func afterValue(d action.Delta) json.RawMessage  { return d.After }
func beforeValue(d action.Delta) json.RawMessage { return d.Before }

// valueEdits turns deltas into edits that write the values pick takes. A
// nil value removes the field.
func valueEdits(deltas []action.Delta, pick func(action.Delta) json.RawMessage) []action.Edit {
	edits := make([]action.Edit, 0, len(deltas))

	for _, d := range deltas {
		edits = append(edits, action.Edit{Path: d.Path, Value: pick(d)})
	}

	return edits
}

// target names the resource, as Snapshot.Target.
func target(p Params) string {
	return "kubernetes:prometheusrule/" + p.Namespace + "/" + p.Name
}

// describe lists rules for a person.
func describe(refs []ruleRef) string {
	parts := make([]string, len(refs))
	for i, ref := range refs {
		parts[i] = ref.String()
	}

	return strings.Join(parts, ", ")
}

// listPaths writes the paths of some deltas, at most maxDifferences.
func listPaths(deltas []action.Delta) string {
	var paths []string

	for i, d := range deltas {
		if i == maxDifferences {
			paths = append(paths, "and more")

			break
		}

		paths = append(paths, strconv.Quote(d.Path.String()))
	}

	return strings.Join(paths, ", ")
}

// fieldCount writes a number of fields, such as "1 field" or "3 fields".
func fieldCount(n int) string {
	if n == 1 {
		return "1 field"
	}

	return strconv.Itoa(n) + " fields"
}
