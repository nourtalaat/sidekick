package grafana

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/prometheus/common/model"

	"github.com/nourtalaat/sidekick/internal/action"
)

// What Grafana does with a rule it is sent, as its source shows (tag
// v13.2.5 of github.com/grafana/grafana, with v12.4.14 and v11.6.9 where
// they differ; paths are under pkg/services/ngalert/):
//
//   - api/compat/compat.go, AlertRuleFromProvisionedAlertRule, copies a
//     fixed set of fields. A key it does not know is dropped, and a field
//     that is left out is reset.
//   - api/api_provisioning.go, RoutePutAlertRule, and
//     provisioning/alert_rules.go, UpdateAlertRule, replace id, orgID, uid
//     and updated with their own values and ignore provenance.
//   - models/alert_query.go, PreSave and GetModel, read the model of each
//     query into a map and write it back: maxDataPoints becomes 43200 and
//     intervalMs becomes 1000 when missing, zero or not a number; refId
//     becomes the refId of the query (v12.4.14 and later); and the queryType
//     of the query is taken from model.queryType when that is text.
//   - for and keep_firing_for are Prometheus durations (api/tooling/
//     definitions/provisioning_alert_rules.go), so 300s reads back as 5m.
//   - labels and annotations are omitted from the answer when empty.
//
// normalise puts a rule in the form Grafana reads back, so a definition that
// was written compares equal to the one that was sent.

// Defaults Grafana puts in the model of a query.
const (
	defaultMaxDataPoints = "43200"
	defaultIntervalMS    = "1000"
)

// readOnlyKeys are the fields of a rule that a snapshot leaves out and a
// write does not send: Grafana sets them or replaces what it is sent. All
// the other fields are kept, including ones a newer Grafana adds, so a
// write does not reset a field this code has never heard of.
var readOnlyKeys = []string{"id", "orgID", "uid", "updated", "provenance"}

// definition keeps the fields of a rule that the action can write.
func definition(live map[string]json.RawMessage) (json.RawMessage, error) {
	kept := map[string]json.RawMessage{}

	for key, value := range live {
		if !isReadOnly(key) {
			kept[key] = value
		}
	}

	raw, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("encode the definition: %w", err)
	}

	return raw, nil
}

// version is the time Grafana says the rule was last updated, or "". It is a
// date-time that changes on every write. Only its equality is used.
func version(live map[string]json.RawMessage) string {
	text, _ := textOf(live["updated"])

	return text
}

// provenance is the provenance Grafana reports for the rule: "" for a rule
// made in the UI, and "file", "api" and so on for a rule that something else
// manages.
func provenance(live map[string]json.RawMessage) string {
	text, _ := textOf(live["provenance"])

	return text
}

// isReadOnly reports whether key is one of readOnlyKeys.
func isReadOnly(key string) bool {
	return slices.Contains(readOnlyKeys, key)
}

// normalise returns the definition as it compares between a write and a
// read. Anything it does not know is left as it is. Numbers that come back
// as 600.0 for 600 are already equal for action.Diff, which compares them
// by value.
func normalise(doc json.RawMessage) (json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return nil, fmt.Errorf("decode the definition: %w", err)
	}

	for _, key := range []string{"for", "keep_firing_for"} {
		if err := mapText(top, key, canonicalDuration); err != nil {
			return nil, fmt.Errorf("normalise the %s: %w", key, err)
		}
	}

	for _, key := range []string{"labels", "annotations"} {
		if err := tidyMap(top, key); err != nil {
			return nil, fmt.Errorf("normalise the %s: %w", key, err)
		}
	}

	if err := normaliseData(top); err != nil {
		return nil, fmt.Errorf("normalise the queries: %w", err)
	}

	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("encode the definition: %w", err)
	}

	return out, nil
}

// canonicalDuration writes a Prometheus duration the way Grafana reads it
// back, or returns text that is not a duration as it is.
func canonicalDuration(text string) string {
	d, err := model.ParseDuration(text)
	if err != nil {
		return text
	}

	return d.String()
}

// tidyMap removes the entries of a map of text whose value is null, and the
// map itself when nothing is left: Grafana leaves an empty map out of its
// answer.
func tidyMap(top map[string]json.RawMessage, key string) error {
	raw, ok := top[key]
	if !ok {
		return nil
	}

	entries, ok := objectOf(raw)
	if !ok {
		if isNull(raw) {
			delete(top, key)
		}

		return nil
	}

	for name, value := range entries {
		if isNull(value) {
			delete(entries, name)
		}
	}

	if len(entries) == 0 {
		delete(top, key)

		return nil
	}

	out, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode the entries: %w", err)
	}

	top[key] = out

	return nil
}

// normaliseData puts the model of each query in the form Grafana keeps it.
func normaliseData(top map[string]json.RawMessage) error {
	queries, ok := listOf(top["data"])
	if !ok {
		return nil
	}

	for i, raw := range queries {
		query, ok := objectOf(raw)
		if !ok {
			continue
		}

		if err := normaliseQuery(query); err != nil {
			return fmt.Errorf("query %d: %w", i, err)
		}

		out, err := json.Marshal(query)
		if err != nil {
			return fmt.Errorf("encode query %d: %w", i, err)
		}

		queries[i] = out
	}

	out, err := json.Marshal(queries)
	if err != nil {
		return fmt.Errorf("encode the queries: %w", err)
	}

	top["data"] = out

	return nil
}

// normaliseQuery applies to one query what Grafana does to it on write.
func normaliseQuery(query map[string]json.RawMessage) error {
	body, ok := objectOf(query["model"])
	if !ok {
		return nil
	}

	for key, fallback := range map[string]string{"maxDataPoints": defaultMaxDataPoints, "intervalMs": defaultIntervalMS} {
		var n float64
		if json.Unmarshal(body[key], &n) != nil || n == 0 {
			body[key] = json.RawMessage(fallback)
		}
	}

	if refID, ok := textOf(query["refId"]); ok {
		quoted, err := json.Marshal(refID)
		if err != nil {
			return fmt.Errorf("encode the refId: %w", err)
		}

		body["refId"] = quoted
	}

	if kind, ok := textOf(body["queryType"]); ok {
		quoted, err := json.Marshal(kind)
		if err != nil {
			return fmt.Errorf("encode the queryType: %w", err)
		}

		query["queryType"] = quoted
	}

	out, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the model: %w", err)
	}

	query["model"] = out

	return nil
}

// holdsAfter reports whether the live definition has the value the change
// writes at every field it changes, as Grafana reads values back. Fields the
// change does not touch are not looked at, so what Grafana adds to a rule
// when it is written does not matter.
func holdsAfter(live json.RawMessage, snap action.Snapshot) (bool, error) {
	cur, deltas, err := normalised(live, snap)
	if err != nil {
		return false, fmt.Errorf("normalise for the after check: %w", err)
	}

	ok, err := action.AtAfter(cur, deltas)
	if err != nil {
		return false, fmt.Errorf("compare the rule with the after values: %w", err)
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
		return false, fmt.Errorf("compare the rule with the before values: %w", err)
	}

	return ok, nil
}

// sameDefinition reports whether two definitions are the same, as Grafana
// reads them back.
func sameDefinition(a, b json.RawMessage) (bool, error) {
	a, err := normalise(a)
	if err != nil {
		return false, fmt.Errorf("normalise the first definition: %w", err)
	}

	b, err = normalise(b)
	if err != nil {
		return false, fmt.Errorf("normalise the second definition: %w", err)
	}

	deltas, err := action.Diff(a, b)
	if err != nil {
		return false, fmt.Errorf("compare the definitions: %w", err)
	}

	return len(deltas) == 0, nil
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

// objectOf returns the fields of a JSON object, and false for anything else,
// including nothing.
func objectOf(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}

	return fields, true
}

// listOf returns the elements of a JSON array, and false for anything else,
// including nothing.
func listOf(raw json.RawMessage) ([]json.RawMessage, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, false
	}

	return items, true
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
// including null and nothing.
func textOf(raw json.RawMessage) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}

	text, ok := value.(string)

	return text, ok
}

// isNull reports whether raw is the JSON null.
func isNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}
