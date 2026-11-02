package signoz

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The two segments of a supported field that stand for more than one name:
// any key, and any position in an array.
const (
	anyKey = "*"
	anyPos = "[*]"
)

// The schema versions of a rule. A rule without schemaVersion is the older
// shape, read as "v1": the threshold, the window and the channels are at the
// top of the rule and of its condition. A v2alpha1 rule has them in
// condition.thresholds, evaluation and notificationSettings.
type schema int

const (
	schemaAny schema = iota
	schemaV1
	schemaV2
)

const schemaV2Name = "v2alpha1"

// kind is what a value of a field must be.
type kind int

const (
	kindText kind = iota
	kindName
	kindFlag
	kindNumber
	kindCount
	kindDuration
	kindTextList
	kindOperator
	kindMatchType
	kindAlertStates
	kindBuilderText
	kindQueryText
)

// leaf is one field of a rule that Sidekick can change, with what SigNoz
// does with it.
type leaf struct {
	path []string
	// schema says which kind of rule keeps the field. SigNoz keeps a field of
	// the other kind but ignores it, so a change to it does nothing.
	schema schema
	kind   kind
	// removable fields read back as not there when they are empty, so a
	// change may remove them.
	removable bool
	// rolling fields exist only in a rolling evaluation.
	rolling bool
}

var (
	errNotSupported = errors.New("not a field Sidekick can change in a SigNoz rule")
	errUnsafeShape  = errors.New("the change would not read back as written")
)

// supported are the fields of a rule Sidekick can change, in the order they
// are listed to people and to the model.
var supported = []leaf{
	{path: p("alert"), kind: kindName},
	{path: p("description"), kind: kindText, removable: true},
	{path: p("disabled"), kind: kindFlag},
	{path: p("labels", anyKey), kind: kindText, removable: true},
	{path: p("annotations", anyKey), kind: kindText, removable: true},
	{path: p("condition", "alertOnAbsent"), kind: kindFlag, removable: true},
	{path: p("condition", "absentFor"), kind: kindCount, removable: true},
	{path: p("condition", "requireMinPoints"), kind: kindFlag, removable: true},
	{path: p("condition", "requiredNumPoints"), kind: kindCount, removable: true},
	{path: p("condition", "compositeQuery", "queries", anyPos, "spec", "filter", "expression"), kind: kindBuilderText},
	{path: p("condition", "compositeQuery", "queries", anyPos, "spec", "having", "expression"), kind: kindBuilderText},
	{path: p("condition", "compositeQuery", "queries", anyPos, "spec", "query"), kind: kindQueryText},

	{path: p("evalWindow"), schema: schemaV1, kind: kindDuration},
	{path: p("frequency"), schema: schemaV1, kind: kindDuration},
	{path: p("preferredChannels"), schema: schemaV1, kind: kindTextList, removable: true},
	{path: p("condition", "target"), schema: schemaV1, kind: kindNumber},
	{path: p("condition", "op"), schema: schemaV1, kind: kindOperator},
	{path: p("condition", "matchType"), schema: schemaV1, kind: kindMatchType},
	{path: p("condition", "targetUnit"), schema: schemaV1, kind: kindText, removable: true},

	{path: p("condition", "thresholds", "spec", anyPos, "name"), schema: schemaV2, kind: kindName},
	{path: p("condition", "thresholds", "spec", anyPos, "target"), schema: schemaV2, kind: kindNumber},
	{path: p("condition", "thresholds", "spec", anyPos, "targetUnit"), schema: schemaV2, kind: kindText, removable: true},
	{path: p("condition", "thresholds", "spec", anyPos, "recoveryTarget"), schema: schemaV2, kind: kindNumber, removable: true},
	{path: p("condition", "thresholds", "spec", anyPos, "matchType"), schema: schemaV2, kind: kindMatchType},
	{path: p("condition", "thresholds", "spec", anyPos, "op"), schema: schemaV2, kind: kindOperator},
	{path: p("condition", "thresholds", "spec", anyPos, "channels"), schema: schemaV2, kind: kindTextList, removable: true},
	{path: p("evaluation", "spec", "evalWindow"), schema: schemaV2, kind: kindDuration, rolling: true},
	{path: p("evaluation", "spec", "frequency"), schema: schemaV2, kind: kindDuration},
	{path: p("notificationSettings", "groupBy"), schema: schemaV2, kind: kindTextList, removable: true},
	{path: p("notificationSettings", "usePolicy"), schema: schemaV2, kind: kindFlag},
	{path: p("notificationSettings", "newGroupEvalDelay"), schema: schemaV2, kind: kindDuration, removable: true},
	{path: p("notificationSettings", "renotify", "enabled"), schema: schemaV2, kind: kindFlag},
	{path: p("notificationSettings", "renotify", "interval"), schema: schemaV2, kind: kindDuration, removable: true},
	{path: p("notificationSettings", "renotify", "alertStates"), schema: schemaV2, kind: kindAlertStates, removable: true},
}

// removableObjects are the objects a change may remove whole: SigNoz reads
// them back as not there when they are empty or absent.
var removableObjects = [][]string{
	p("labels"),
	p("annotations"),
	p("notificationSettings", "renotify"),
	p("condition", "compositeQuery", "queries", anyPos, "spec", "filter"),
	p("condition", "compositeQuery", "queries", anyPos, "spec", "having"),
}

// The values SigNoz accepts for the compare operator and the match type, as
// it reads them: lower case, without space around them
// (pkg/types/ruletypes/compare.go and match.go).
var (
	operators = []string{
		"1", "2", "3", "4", "5", "6", "7",
		"above", "below", "equal", "eq", "not_equal", "not_eq", "above_or_equal", "above_or_eq",
		"below_or_equal", "below_or_eq", "outside_bounds",
		">", "<", "=", "!=", ">=", "<=",
	}
	matchTypes = []string{
		"1", "2", "3", "4", "5",
		"at_least_once", "all_the_times", "on_average", "avg", "in_total", "sum", "last",
	}
	alertStates = []string{"firing", "nodata"}
)

// The query types of a rule that have the text fields of kindBuilderText
// and of kindQueryText.
var (
	builderQueryTypes = []string{"builder_query", "builder_ai_query", "builder_sub_query"}
	textQueryTypes    = []string{"promql", "clickhouse_sql"}
)

// checkChanges refuses a change that SigNoz would not keep as written: a
// field that is not in supported, a field of the other schema version, a
// value of the wrong form, or a removal of a field that cannot be removed.
// after is the rule as it will be written. The error wraps action.ErrLimit.
func checkChanges(after json.RawMessage, deltas []action.Delta) error {
	root, err := decodeTree(after)
	if err != nil {
		return fmt.Errorf("read the new rule: %w: %w", action.ErrLimit, err)
	}

	obj, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("the new rule is not an object: %w", action.ErrLimit)
	}

	version, err := schemaOf(obj)
	if err != nil {
		return fmt.Errorf("read the schema version: %w", err)
	}

	c := &checker{root: obj, schema: version}

	for _, d := range deltas {
		if err := c.checkDelta(d.Path, d.After); err != nil {
			return fmt.Errorf("check the change: %w", err)
		}
	}

	return checkRelations(obj, deltas)
}

// checker holds the new rule while the changes to it are checked.
type checker struct {
	root   map[string]any
	schema schema
}

// checkDelta checks the change of one field, or of an object that holds
// supported fields. A nil raw is a removal.
func (c *checker) checkDelta(path []string, raw json.RawMessage) error {
	if l, ok := leafFor(path); ok {
		return c.checkLeaf(l, path, raw)
	}

	if isParent(path) {
		return c.checkParent(path, raw)
	}

	return notSupported(path)
}

// checkLeaf checks the new value of one supported field.
func (c *checker) checkLeaf(l leaf, path []string, raw json.RawMessage) error {
	if l.schema != schemaAny && l.schema != c.schema {
		return refuse(path, "applies to a rule %s, and this rule %s; SigNoz would keep the value and ignore it", l.schema.describe(), c.schema.describe())
	}

	if err := checkLabelName(path); err != nil {
		return fmt.Errorf("check the name: %w", err)
	}

	if raw == nil {
		if !l.removable {
			return refuse(path, "cannot be removed; set a new value")
		}

		return nil
	}

	value, err := decodeTree(raw)
	if err != nil {
		return refuse(path, "has a value that is not JSON")
	}

	if err := checkKind(l.kind, path, value); err != nil {
		return fmt.Errorf("check the value: %w", err)
	}

	return c.checkWhere(l, path)
}

// checkParent checks an object that was added or removed whole.
func (c *checker) checkParent(path []string, raw json.RawMessage) error {
	if raw == nil {
		if matchesAny(removableObjects, path) {
			return nil
		}

		return refuse(path, "cannot be removed whole; change the fields below it one at a time")
	}

	value, err := decodeTree(raw)
	if err != nil {
		return refuse(path, "has a value that is not JSON")
	}

	obj, ok := value.(map[string]any)
	if !ok {
		return refuse(path, "must be an object; to add or remove an item of a list, or to replace a list, is not supported, change the fields of its items one at a time")
	}

	for _, key := range slices.Sorted(maps.Keys(obj)) {
		child, err := json.Marshal(obj[key])
		if err != nil {
			return refuse(path, "has a value that cannot be written")
		}

		if err := c.checkDelta(append(slices.Clone(path), key), child); err != nil {
			return fmt.Errorf("check the fields below it: %w", err)
		}
	}

	return nil
}

// checkWhere checks what a field needs of the rule around it: the type of the
// query it belongs to, and the kind of the evaluation.
func (c *checker) checkWhere(l leaf, path []string) error {
	switch {
	case l.kind == kindBuilderText:
		return c.checkQueryType(path, builderQueryTypes, "a builder query")
	case l.kind == kindQueryText:
		return c.checkQueryType(path, textQueryTypes, "a PromQL or ClickHouse SQL query")
	case l.rolling:
		if text(c.root, "evaluation", "kind") != "rolling" {
			return refuse(path, "exists only when evaluation.kind is rolling")
		}
	}

	return nil
}

// checkQueryType checks that the query a path goes into has one of types.
func (c *checker) checkQueryType(path, types []string, want string) error {
	const positionAt = 3

	n, err := strconv.Atoi(strings.Trim(path[positionAt], "[]"))
	if err != nil {
		return refuse(path, "is not in a query")
	}

	queries, _ := lookup(c.root, "condition", "compositeQuery", "queries").([]any)
	if n >= len(queries) {
		return refuse(path, "is not in a query of the rule")
	}

	query, _ := queries[n].(map[string]any)
	if !slices.Contains(types, text(query, "type")) {
		return refuse(path, "applies to %s, and this query is %q", want, text(query, "type"))
	}

	return nil
}

// checkRelations checks the fields of the new rule that must agree, when a
// change touches one of them.
func checkRelations(root map[string]any, deltas []action.Delta) error {
	if touches(deltas, p("condition", "requireMinPoints")) || touches(deltas, p("condition", "requiredNumPoints")) {
		if lookup(root, "condition", "requireMinPoints") == true && !positive(lookup(root, "condition", "requiredNumPoints")) {
			return fmt.Errorf("condition.requireMinPoints needs condition.requiredNumPoints above 0 in the same change: %w", action.ErrLimit)
		}
	}

	if touches(deltas, p("notificationSettings", "renotify")) && lookup(root, "notificationSettings", "renotify", "enabled") == true {
		interval, _ := lookup(root, "notificationSettings", "renotify", "interval").(string)
		if d, err := time.ParseDuration(interval); err != nil || d <= 0 {
			return fmt.Errorf("notificationSettings.renotify.interval must be a duration above zero while renotify is enabled: %w", action.ErrLimit)
		}
	}

	return nil
}

// checkKind checks that value has the form kind asks for.
func checkKind(k kind, path []string, value any) error {
	if _, isObject := value.(map[string]any); isObject && len(path) == 2 && (path[0] == "labels" || path[0] == "annotations") {
		return refuse(path, "is an object, which happens when a %s name has a dot; names with a dot cannot be changed, because a dot in a path separates keys", path[0][:len(path[0])-1])
	}

	var ok bool

	switch k {
	case kindText, kindBuilderText, kindQueryText:
		_, ok = value.(string)
	case kindName:
		s, isText := value.(string)
		ok = isText && strings.TrimSpace(s) != ""
	case kindFlag:
		_, ok = value.(bool)
	case kindNumber:
		ok = isFiniteNumber(value)
	case kindCount:
		n, isNumber := value.(json.Number)
		_, err := strconv.ParseUint(string(n), 10, 64)
		ok = isNumber && err == nil
	case kindDuration:
		ok = isPositiveDuration(value)
	case kindTextList:
		ok = isTextList(value)
	case kindOperator:
		ok = isOneOf(value, operators)
	case kindMatchType:
		ok = isOneOf(value, matchTypes)
	case kindAlertStates:
		ok = isListOf(value, alertStates)
	}

	if !ok {
		return refuse(path, "must be %s", k.describe())
	}

	return nil
}

// checkLabelName checks the name of a label or an annotation, as SigNoz does.
func checkLabelName(path []string) error {
	if len(path) != 2 || (path[0] != "labels" && path[0] != "annotations") {
		return nil
	}

	if !validLabelName(path[1]) {
		return refuse(path, "is not a valid name: it takes letters, digits, \"_\" and \".\", and no digit first")
	}

	return nil
}

// validLabelName is SigNoz's own check of a label or annotation name
// (pkg/types/ruletypes/api_params.go, isValidLabelName).
func validLabelName(name string) bool {
	if name == "" {
		return false
	}

	for i, r := range name {
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := r >= '0' && r <= '9'

		if !letter && r != '_' && r != '.' && (!digit || i == 0) {
			return false
		}
	}

	return true
}

// leafFor returns the supported field at path.
func leafFor(path []string) (leaf, bool) {
	for _, l := range supported {
		if pathMatches(l.path, path) {
			return l, true
		}
	}

	return leaf{}, false
}

// isParent reports whether path is above a supported field.
func isParent(path []string) bool {
	return slices.ContainsFunc(supported, func(l leaf) bool {
		return len(path) < len(l.path) && pathMatches(l.path[:len(path)], path)
	})
}

// fieldKnown reports whether an entry of limits.fields can allow a supported
// field: it is one, or it is above some.
func fieldKnown(field action.Path) bool {
	return slices.ContainsFunc(supported, func(l leaf) bool {
		return len(field) <= len(l.path) && pathMatches(l.path[:len(field)], field)
	})
}

// supportedWithin returns the supported fields that an entry of
// limits.fields allows, written for people.
func supportedWithin(fields []action.Path) []string {
	var out []string

	for _, l := range supported {
		if slices.ContainsFunc(fields, func(f action.Path) bool {
			return len(f) <= len(l.path) && pathMatches(l.path[:len(f)], f)
		}) {
			out = append(out, l.String())
		}
	}

	return out
}

// pathMatches reports whether path is the pattern, where the pattern's any
// key and any position segments match every key and every position.
func pathMatches(pattern, path []string) bool {
	if len(pattern) != len(path) {
		return false
	}

	for i, seg := range pattern {
		switch {
		case seg == anyKey && !strings.HasPrefix(path[i], "["):
		case seg == anyPos && strings.HasPrefix(path[i], "["):
		case seg == path[i]:
		default:
			return false
		}
	}

	return true
}

// matchesAny reports whether path matches one of the patterns.
func matchesAny(patterns [][]string, path []string) bool {
	return slices.ContainsFunc(patterns, func(pattern []string) bool { return pathMatches(pattern, path) })
}

// touches reports whether a change is at or below prefix.
func touches(deltas []action.Delta, prefix []string) bool {
	return slices.ContainsFunc(deltas, func(d action.Delta) bool {
		return len(d.Path) >= len(prefix) && slices.Equal(d.Path[:len(prefix)], prefix)
	})
}

// schemaOf says which shape a rule has, from its schemaVersion.
func schemaOf(rule map[string]any) (schema, error) {
	version, _ := rule["schemaVersion"].(string)

	switch version {
	case "", "v1":
		return schemaV1, nil
	case schemaV2Name:
		return schemaV2, nil
	default:
		return schemaAny, fmt.Errorf("this rule has schemaVersion %q, which Sidekick does not know how to change: %w", version, action.ErrLimit)
	}
}

// String writes the path of a supported field for people: <name> for any
// key and [n] for any position.
func (l leaf) String() string {
	var b strings.Builder

	for i, seg := range l.path {
		switch {
		case seg == anyPos:
			b.WriteString("[n]")
		case i > 0:
			b.WriteByte('.')

			if seg == anyKey {
				b.WriteString("<name>")

				continue
			}

			b.WriteString(seg)
		default:
			b.WriteString(seg)
		}
	}

	return b.String()
}

// describe says which rules have the schema.
func (s schema) describe() string {
	switch s {
	case schemaV1:
		return "without schemaVersion"
	case schemaV2:
		return "with schemaVersion " + schemaV2Name
	default:
		return "of any shape"
	}
}

// describe says what a value of the kind is, for an error.
func (k kind) describe() string {
	switch k {
	case kindName:
		return "text that is not empty"
	case kindFlag:
		return "true or false"
	case kindNumber:
		return "a number"
	case kindCount:
		return "a whole number, 0 or more"
	case kindDuration:
		return "a duration in text, above zero, such as \"5m\" or \"1h30m\" (a number would mean nanoseconds)"
	case kindTextList:
		return "a list of text that is not empty"
	case kindOperator:
		return "one of " + strings.Join(operators, ", ")
	case kindMatchType:
		return "one of " + strings.Join(matchTypes, ", ")
	case kindAlertStates:
		return "a list of " + strings.Join(alertStates, " and ")
	default:
		return "text"
	}
}

// notSupported is the error for a field that Sidekick does not change.
func notSupported(path []string) error {
	return fmt.Errorf("the field %q is %w; the fields it can change are %s: %w",
		action.Path(path).String(), errNotSupported, strings.Join(supportedWithin([]action.Path{{}}), ", "), action.ErrLimit)
}

// p makes a path pattern.
func p(segments ...string) []string {
	return segments
}

// refuse is the error for a change that SigNoz would not keep as written.
func refuse(path []string, format string, args ...any) error {
	return fmt.Errorf("the field %q %s: %w: %w", action.Path(path).String(), fmt.Sprintf(format, args...), errUnsafeShape, action.ErrLimit)
}

// decodeTree reads JSON into maps, lists and values, keeping numbers as
// written.
func decodeTree(raw json.RawMessage) (any, error) {
	var v any

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}

	return v, nil
}

// lookup returns the value at the keys below node, or nil.
func lookup(node any, keys ...string) any {
	for _, key := range keys {
		obj, ok := node.(map[string]any)
		if !ok {
			return nil
		}

		node = obj[key]
	}

	return node
}

// text returns the lower-case text at the keys below node, or "".
func text(node any, keys ...string) string {
	s, _ := lookup(node, keys...).(string)

	return strings.ToLower(strings.TrimSpace(s))
}

func isFiniteNumber(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}

	f, err := n.Float64()

	return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}

func positive(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}

	f, err := n.Float64()

	return err == nil && f > 0
}

func isPositiveDuration(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}

	d, err := time.ParseDuration(s)

	return err == nil && d > 0
}

func isTextList(v any) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}

	return !slices.ContainsFunc(list, func(item any) bool {
		s, isText := item.(string)

		return !isText || strings.TrimSpace(s) == ""
	})
}

func isOneOf(v any, allowed []string) bool {
	s, ok := v.(string)

	return ok && slices.Contains(allowed, strings.ToLower(strings.TrimSpace(s)))
}

func isListOf(v any, allowed []string) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}

	return !slices.ContainsFunc(list, func(item any) bool { return !isOneOf(item, allowed) })
}
