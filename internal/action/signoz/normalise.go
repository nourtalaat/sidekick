package signoz

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// readOnlyKeys are the keys of a rule that SigNoz sets and a write cannot
// change. They are left out of the definition a snapshot keeps.
var readOnlyKeys = []string{"id", "state", "createdAt", "createdBy", "updatedAt", "updatedBy"}

// lowercased are the texts SigNoz reads back in lower case and without space
// around them (pkg/valuer/string.go).
var lowercased = [][]string{
	p("condition", "op"),
	p("condition", "matchType"),
	p("condition", "thresholds", "spec", anyPos, "op"),
	p("condition", "thresholds", "spec", anyPos, "matchType"),
	p("notificationSettings", "renotify", "alertStates", anyPos),
}

// dropRule says that SigNoz reads the optional value at path back as not
// there when empty reports the value empty.
type dropRule struct {
	path  []string
	empty func(v any) bool
}

// droppedWhenEmpty are the optional values SigNoz reads back as not there,
// or shows as null or empty when they were not set. What counts as empty
// depends on how SigNoz's type keeps the value, at v0.148.1 in
// pkg/types/ruletypes: a value with omitempty is left out when it is the Go
// zero value, so null, empty text, false, 0 and an empty list or object are
// all "not there"; a pointer without omitempty shows null when unset, so only
// null is "not there" and 0 is a value; a list with omitzero is left out when
// nil, so only null is "not there" and [] is a value.
var droppedWhenEmpty = []dropRule{
	// api_params.go:66, 72, 73, 79: string, maps and list with omitempty.
	{p("description"), omitsEmpty},
	{p("labels"), omitsEmpty},
	{p("annotations"), omitsEmpty},
	{p("preferredChannels"), omitsEmpty},
	// alerting.go:114, 115, 117, 121, 122: bool, uint64, string, bool, int with omitempty.
	{p("condition", "alertOnAbsent"), omitsEmpty},
	{p("condition", "absentFor"), omitsEmpty},
	{p("condition", "requireMinPoints"), omitsEmpty},
	{p("condition", "requiredNumPoints"), omitsEmpty},
	{p("condition", "targetUnit"), omitsEmpty},
	// threshold.go:139: *float64 without omitempty, null when unset, 0 is a value.
	{p("condition", "thresholds", "spec", anyPos, "recoveryTarget"), isNull},
	// threshold.go:138: string without omitempty, "" when unset.
	{p("condition", "thresholds", "spec", anyPos, "targetUnit"), isNullOrBlank},
	// threshold.go:142: list without omitempty, null when nil, [] when empty.
	{p("condition", "thresholds", "spec", anyPos, "channels"), isNull},
	// api_params.go:90, 93, 99: lists and a duration with omitzero.
	{p("notificationSettings", "groupBy"), isNull},
	{p("notificationSettings", "newGroupEvalDelay"), isNull},
	{p("notificationSettings", "renotify", "alertStates"), isNull},
}

// normalise returns the rule as it compares between a write and a read.
// SigNoz parses what it stores into typed values and writes them out again
// on every read, so the enum text is in lower case, and optional values
// that are empty are not there, or are null. Numbers that are written
// another way are already equal for action.Diff. Anything else is left as
// it is.
func normalise(doc json.RawMessage) (json.RawMessage, error) {
	tree, err := decodeTree(doc)
	if err != nil {
		return nil, fmt.Errorf("decode the rule: %w", err)
	}

	out, err := json.Marshal(normalised(tree, nil))
	if err != nil {
		return nil, fmt.Errorf("encode the rule: %w", err)
	}

	return out, nil
}

// normalised returns node, at path in the rule, normalised.
func normalised(node any, path []string) any {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			childPath := append(slices.Clone(path), key)

			child = normalised(child, childPath)
			if dropped(childPath, child) {
				delete(v, key)

				continue
			}

			v[key] = child
		}

		return v
	case []any:
		for i := range v {
			v[i] = normalised(v[i], append(slices.Clone(path), "["+strconv.Itoa(i)+"]"))
		}

		return v
	case string:
		if matchesAny(lowercased, path) {
			return strings.ToLower(strings.TrimSpace(v))
		}

		return v
	default:
		return node
	}
}

// dropped reports whether SigNoz reads the value at path back as not there.
func dropped(path []string, value any) bool {
	for _, rule := range droppedWhenEmpty {
		if pathMatches(rule.path, path) {
			return rule.empty(value)
		}
	}

	return false
}

// omitsEmpty reports whether a value is one that a field with omitempty is
// left out for: null, empty text, false, zero, or an empty list or object.
func omitsEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case json.Number:
		f, err := x.Float64()

		return err == nil && f == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

// isNull reports whether a value is null.
func isNull(v any) bool {
	return v == nil
}

// isNullOrBlank reports whether a value is null or empty text.
func isNullOrBlank(v any) bool {
	text, isText := v.(string)

	return v == nil || (isText && text == "")
}

// sameDefinition reports whether two rules are the same, as SigNoz reads
// them back.
func sameDefinition(a, b json.RawMessage) (bool, error) {
	a, err := normalise(a)
	if err != nil {
		return false, fmt.Errorf("normalise the first rule: %w", err)
	}

	b, err = normalise(b)
	if err != nil {
		return false, fmt.Errorf("normalise the second rule: %w", err)
	}

	deltas, err := action.Diff(a, b)
	if err != nil {
		return false, fmt.Errorf("compare the rules: %w", err)
	}

	return len(deltas) == 0, nil
}

// holdsAfter reports whether the live rule has the value the change writes
// at every field it changes, as SigNoz reads values back. Fields the change
// does not touch are not looked at.
func holdsAfter(live json.RawMessage, snap action.Snapshot) (bool, error) {
	wrong, err := keptWrong(live, snap, true)
	if err != nil {
		return false, fmt.Errorf("compare with the after values: %w", err)
	}

	return len(wrong) == 0, nil
}

// holdsBefore is holdsAfter for the values before the change.
func holdsBefore(live json.RawMessage, snap action.Snapshot) (bool, error) {
	wrong, err := keptWrong(live, snap, false)
	if err != nil {
		return false, fmt.Errorf("compare with the before values: %w", err)
	}

	return len(wrong) == 0, nil
}

// keptWrong returns the paths of the fields of the change where the live
// rule does not have the after values (when after is true) or the before
// values (when it is false), as SigNoz reads values back.
func keptWrong(live json.RawMessage, snap action.Snapshot, after bool) ([]string, error) {
	cur, deltas, err := normalisedSnapshot(live, snap)
	if err != nil {
		return nil, fmt.Errorf("normalise for the comparison: %w", err)
	}

	var wrong []string

	for _, d := range deltas {
		held, err := atValue(cur, d, after)
		if err != nil {
			return nil, fmt.Errorf("compare the field %q: %w", d.Path.String(), err)
		}

		if !held {
			wrong = append(wrong, d.Path.String())
		}
	}

	return wrong, nil
}

// atValue reports whether doc has the after or the before value of d.
func atValue(doc json.RawMessage, d action.Delta, after bool) (bool, error) {
	if after {
		ok, err := action.AtAfter(doc, []action.Delta{d})
		if err != nil {
			return false, fmt.Errorf("compare with the after value: %w", err)
		}

		return ok, nil
	}

	ok, err := action.AtBefore(doc, []action.Delta{d})
	if err != nil {
		return false, fmt.Errorf("compare with the before value: %w", err)
	}

	return ok, nil
}

// normalisedSnapshot returns the live rule and the difference of the
// snapshot, both normalised.
func normalisedSnapshot(live json.RawMessage, snap action.Snapshot) (json.RawMessage, []action.Delta, error) {
	cur, err := normalise(live)
	if err != nil {
		return nil, nil, fmt.Errorf("normalise the live rule: %w", err)
	}

	before, err := normalise(snap.Before)
	if err != nil {
		return nil, nil, fmt.Errorf("normalise the before rule: %w", err)
	}

	after, err := normalise(snap.After)
	if err != nil {
		return nil, nil, fmt.Errorf("normalise the after rule: %w", err)
	}

	deltas, err := action.Diff(before, after)
	if err != nil {
		return nil, nil, fmt.Errorf("compare the normalised snapshot: %w", err)
	}

	return cur, deltas, nil
}
