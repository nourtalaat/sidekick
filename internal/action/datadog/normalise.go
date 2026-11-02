package datadog

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The two alert types Datadog reads back as each other.
const (
	typeQuery  = "query alert"
	typeMetric = "metric alert"
)

// normalise returns the definition as it compares between a write and a
// read. Datadog does not keep every value as it was sent: it does not
// promise the order of the tags, it trims the space around a query and a
// message, and it can read a "query alert" back as a "metric alert" and the
// other way round. Numbers that come back as 3.0 for 3 are already equal
// for action.Diff, which compares them by value. Anything else is left as
// it is.
func normalise(doc json.RawMessage) (json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return nil, fmt.Errorf("decode the definition: %w", err)
	}

	if tags, ok := top["tags"]; ok {
		var list []string
		if json.Unmarshal(tags, &list) == nil {
			slices.Sort(list)

			sorted, err := json.Marshal(list)
			if err != nil {
				return nil, fmt.Errorf("encode the tags: %w", err)
			}

			top["tags"] = sorted
		}
	}

	for _, key := range []string{"query", "message"} {
		if err := mapText(top, key, strings.TrimSpace); err != nil {
			return nil, fmt.Errorf("normalise the %s: %w", key, err)
		}
	}

	if err := mapText(top, "type", func(t string) string {
		if t == typeQuery {
			return typeMetric
		}

		return t
	}); err != nil {
		return nil, fmt.Errorf("normalise the type: %w", err)
	}

	if err := dropNullOptions(top); err != nil {
		return nil, fmt.Errorf("normalise the options: %w", err)
	}

	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("encode the definition: %w", err)
	}

	return out, nil
}

// dropNullOptions removes the options whose value is null. Datadog reads a
// null option back as one that is not there, which is how a change removes
// an option such as no_data_timeframe. Only options are treated this way: a
// null at the top of the definition is left as it is.
func dropNullOptions(top map[string]json.RawMessage) error {
	options, ok := objectOf(top["options"])
	if !ok {
		return nil
	}

	for key, value := range options {
		if strings.TrimSpace(string(value)) == "null" {
			delete(options, key)
		}
	}

	out, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("encode the options: %w", err)
	}

	top["options"] = out

	return nil
}

// objectOf returns the fields of a JSON object, and false for anything else,
// including nothing.
func objectOf(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}

	return fields, true
}

// mapText replaces the text at key in top with change of it. A field that is
// missing or is not text is left as it is.
func mapText(top map[string]json.RawMessage, key string, change func(string) string) error {
	text, ok := textOf(top[key])
	if !ok {
		return nil
	}

	changed, err := json.Marshal(change(text))
	if err != nil {
		return fmt.Errorf("encode the %s: %w", key, err)
	}

	top[key] = changed

	return nil
}

// textOf returns the text of a JSON string, and false for anything else,
// including nothing.
func textOf(raw json.RawMessage) (string, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}

	return text, true
}

// holdsAfter reports whether the live definition has the value the change
// writes at every field it changes, as Datadog reads values back. Fields the
// change does not touch are not looked at, so what Datadog adds to a monitor
// when it is written, such as options.notify_audit, does not matter.
func holdsAfter(live json.RawMessage, snap action.Snapshot) (bool, error) {
	cur, deltas, err := normalised(live, snap)
	if err != nil {
		return false, fmt.Errorf("normalise for the after check: %w", err)
	}

	ok, err := action.AtAfter(cur, deltas)
	if err != nil {
		return false, fmt.Errorf("compare the monitor with the after values: %w", err)
	}

	return ok, nil
}

// holdsBefore is holdsAfter for the values before the change.
func holdsBefore(live json.RawMessage, snap action.Snapshot) (bool, error) {
	cur, deltas, err := normalised(live, snap)
	if err != nil {
		return false, fmt.Errorf("normalise for the before check: %w", err)
	}

	ok, err := action.AtBefore(cur, deltas)
	if err != nil {
		return false, fmt.Errorf("compare the monitor with the before values: %w", err)
	}

	return ok, nil
}

// normalised returns the live definition and the difference of the snapshot,
// both normalised.
func normalised(live json.RawMessage, snap action.Snapshot) (json.RawMessage, []action.Delta, error) {
	cur, err := normalise(live)
	if err != nil {
		return nil, nil, fmt.Errorf("normalise the live definition: %w", err)
	}

	before, err := normalise(snap.Before)
	if err != nil {
		return nil, nil, fmt.Errorf("normalise the before definition: %w", err)
	}

	after, err := normalise(snap.After)
	if err != nil {
		return nil, nil, fmt.Errorf("normalise the after definition: %w", err)
	}

	deltas, err := action.Diff(before, after)
	if err != nil {
		return nil, nil, fmt.Errorf("compare the normalised snapshot: %w", err)
	}

	return cur, deltas, nil
}
