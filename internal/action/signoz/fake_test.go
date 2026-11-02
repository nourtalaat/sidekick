package signoz

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// storedV1 is a rule without schemaVersion as SigNoz keeps it, which is the
// body of the PUT that saved it. SigNoz reads it back as v1Rule: it adds the
// version and the window, lower-cases the operator and the match type, and
// drops the key it does not know.
const storedV1 = `{
  "alert": "Checkout errors high", "alertType": "METRIC_BASED_ALERT", "ruleType": "threshold_rule",
  "description": "Error ratio of checkout", "disabled": false, "unknownKey": 1,
  "labels": {"severity": "critical", "team": "payments"},
  "annotations": {"summary": "Checkout errors are {{$value}}"},
  "preferredChannels": ["slack-oncall"],
  "condition": {
    "compositeQuery": {
      "queries": [{"type": "builder_query", "spec": {
        "name": "A", "signal": "metrics", "source": "", "disabled": false, "legend": "",
        "aggregations": [{"metricName": "checkout_errors", "timeAggregation": "rate", "spaceAggregation": "sum"}],
        "filter": {"expression": "service.name = 'checkout'"}
      }}],
      "panelType": "graph", "queryType": "builder", "unit": "percent"
    },
    "selectedQueryName": "A", "op": "ABOVE", "target": 5, "matchType": "AT_LEAST_ONCE", "targetUnit": "percent",
    "alertOnAbsent": true, "absentFor": 10
  }
}`

// storedV2 is a v2alpha1 rule as SigNoz keeps it. SigNoz reads it back as
// v2Rule: the thresholds gain the values they did not set, and the
// notification settings gain usePolicy.
const storedV2 = `{
  "alert": "Checkout latency", "alertType": "METRIC_BASED_ALERT", "ruleType": "threshold_rule",
  "schemaVersion": "v2alpha1", "disabled": false,
  "labels": {"team": "payments"},
  "condition": {
    "compositeQuery": {
      "queries": [{"type": "builder_query", "spec": {
        "name": "A", "signal": "metrics", "source": "", "disabled": false, "legend": "",
        "aggregations": [{"metricName": "checkout_latency", "timeAggregation": "avg", "spaceAggregation": "avg"}]
      }}],
      "panelType": "graph", "queryType": "builder", "unit": "ms"
    },
    "selectedQueryName": "A",
    "thresholds": {"kind": "basic", "spec": [
      {"name": "critical", "target": 900, "matchType": "AT_LEAST_ONCE", "op": "ABOVE", "channels": ["slack-oncall"]},
      {"name": "warning", "target": 600, "matchType": "at_least_once", "op": "above"}
    ]}
  },
  "evaluation": {"kind": "rolling", "spec": {"evalWindow": "5m", "frequency": "1m"}},
  "notificationSettings": {"groupBy": ["service.name"], "renotify": {"enabled": true, "interval": "4h", "alertStates": ["FIRING"]}}
}`

// knownChannels are the notification channels of the fake SigNoz.
var knownChannels = []string{"slack-oncall", "email"}

// The keys SigNoz keeps of a rule, and of its condition. It drops the others
// when it reads the rule back.
var (
	knownKeys = []string{
		"alert", "alertType", "ruleType", "description", "version", "schemaVersion", "evalWindow", "frequency",
		"disabled", "labels", "annotations", "preferredChannels", "condition", "evaluation", "notificationSettings", "source",
	}
	knownConditionKeys = []string{
		"compositeQuery", "op", "target", "alertOnAbsent", "absentFor", "matchType", "targetUnit", "algorithm",
		"seasonality", "selectedQueryName", "requireMinPoints", "requiredNumPoints", "thresholds",
	}
	knownBuilderKeys = []string{
		"name", "stepInterval", "signal", "source", "aggregations", "bucketOptions", "disabled", "filter", "groupBy", "order",
		"selectFields", "limit", "limitBy", "offset", "cursor", "having", "secondaryAggregations", "functions", "legend",
	}
	knownPromKeys = []string{"name", "query", "disabled", "step", "stats", "skipStepAlignment", "legend"}
)

// fakeSigNoz is a SigNoz that keeps one rule. It stores the body of a PUT as
// it is and shapes it the way SigNoz does on every read, and it moves
// updatedAt on with each write (pkg/query-service/rules/manager.go,
// pkg/types/ruletypes/api_params.go).
type fakeSigNoz struct {
	mu      sync.Mutex
	stored  json.RawMessage
	deleted bool
	edits   int
	puts    []json.RawMessage
	deletes int
	// release is the answer of GET /api/v1/version.
	release string
	// notLoaded makes the rule one that the rule manager has not loaded, which
	// SigNoz reads as disabled whatever the stored value is.
	notLoaded bool
	// viewerOnly makes every write answer 403.
	viewerOnly bool
	// inUse makes a delete answer 409, as for a rule a planned maintenance uses.
	inUse bool
	// getStatus, when set, is the status every read of the rule answers.
	getStatus int
	// mangle changes the rule as it is stored, to play a SigNoz that keeps
	// another value than the one it was sent.
	mangle func(rule map[string]any)
}

// newFakeSigNoz starts the fake API for one test, with stored as the rule.
func newFakeSigNoz(t *testing.T, stored string) (*fakehttp.Server, *fakeSigNoz) {
	t.Helper()

	f := &fakeSigNoz{stored: json.RawMessage(stored), release: "v0.148.1"}

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodGet, "/api/v1/version", f.version)
	api.HandleFunc(http.MethodGet, "/api/v2/rules/"+ruleOne, f.get)
	api.HandleFunc(http.MethodPut, "/api/v2/rules/"+ruleOne, f.put)
	api.HandleFunc(http.MethodDelete, "/api/v2/rules/"+ruleOne, f.remove)

	return api, f
}

func (f *fakeSigNoz) version(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	_ = json.NewEncoder(w).Encode(map[string]any{"version": f.release, "ee": "Y", "setupCompleted": true})
}

func (f *fakeSigNoz) get(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case !f.authorised(w, r, false):
		return
	case f.getStatus != 0:
		fail(w, f.getStatus, "forced", nil)
	case f.deleted:
		fail(w, http.StatusNotFound, "rule with ID: "+ruleOne+" does not exist", nil)
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": f.read()})
	}
}

func (f *fakeSigNoz) put(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.authorised(w, r, true) {
		return
	}

	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "failed to parse json", nil)

		return
	}

	f.puts = append(f.puts, body)

	if f.deleted {
		fail(w, http.StatusNotFound, "rule with ID: "+ruleOne+" does not exist", nil)

		return
	}

	rule, problems := checkRule(body)
	if len(problems) > 0 {
		fail(w, http.StatusBadRequest, "alert rule is not valid", problems)

		return
	}

	if unknown := unknownChannels(rule); len(unknown) > 0 {
		fail(w, http.StatusBadRequest, fmt.Sprintf("channels: the following channels do not exist: %v", unknown), nil)

		return
	}

	if f.mangle != nil {
		f.mangle(rule)

		body, _ = json.Marshal(rule)
	}

	f.stored = body
	f.edits++

	if rule["disabled"] == false {
		f.notLoaded = false
	}

	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeSigNoz) remove(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case !f.authorised(w, r, true):
	case f.deleted:
		fail(w, http.StatusNotFound, "rule with ID: "+ruleOne+" does not exist", nil)
	case f.inUse:
		fail(w, http.StatusConflict, "cannot delete rule because it is referenced by a planned maintenance", nil)
	default:
		f.deletes++
		f.deleted = true

		w.WriteHeader(http.StatusNoContent)
	}
}

// authorised checks the key and the role. It answers the request when it
// refuses it. The caller holds the lock.
func (f *fakeSigNoz) authorised(w http.ResponseWriter, r *http.Request, write bool) bool {
	switch {
	case r.Header.Get(DefaultAPIKeyHeader) != "fake-apiKey" && r.Header.Get("X-Key") != "fake-apiKey":
		fail(w, http.StatusUnauthorized, "invalid api key", nil)
	case write && f.viewerOnly:
		fail(w, http.StatusForbidden, "only editors can do this", nil)
	default:
		return true
	}

	return false
}

// read is the rule as SigNoz reads it back: the stored rule parsed into typed
// values and written out again, with the keys SigNoz sets. The caller holds
// the lock.
func (f *fakeSigNoz) read() map[string]any {
	tree, _ := decodeTree(f.stored)
	rule, _ := tree.(map[string]any)

	for key := range rule {
		if !slices.Contains(knownKeys, key) {
			delete(rule, key)
		}
	}

	if _, ok := rule["version"]; !ok {
		rule["version"] = "v5"
	}

	if _, ok := rule["disabled"]; !ok {
		rule["disabled"] = false
	}

	condition, _ := rule["condition"].(map[string]any)
	for key := range condition {
		if !slices.Contains(knownConditionKeys, key) {
			delete(condition, key)
		}
	}

	lowerKeys(condition, "op", "matchType")

	if rule["schemaVersion"] == schemaV2Name {
		shapeV2(rule, condition)
	} else {
		shapeV1(rule, condition)
	}

	dropEmpty(rule, "description", "labels", "annotations", "preferredChannels")
	dropEmpty(condition, "alertOnAbsent", "absentFor", "requireMinPoints", "requiredNumPoints", "targetUnit")

	state := "inactive"
	if f.notLoaded {
		rule["disabled"], state = true, "disabled"
	}

	rule["id"], rule["state"] = ruleOne, state
	rule["createdAt"], rule["createdBy"] = "2026-10-01T09:00:00Z", "ana@example.com"
	rule["updatedAt"], rule["updatedBy"] = f.updatedAt(), "ben@example.com"

	return rule
}

// updatedAt is the time of the last write.
func (f *fakeSigNoz) updatedAt() string {
	return time.Date(2026, 11, 1, 10, 0, 0, 123456000, time.UTC).Add(time.Duration(f.edits) * time.Minute).Format(time.RFC3339Nano)
}

// shapeV1 is what SigNoz does when it reads a rule without schemaVersion: it
// adds the window, and shows none of what it makes from it.
func shapeV1(rule, condition map[string]any) {
	if _, ok := rule["evalWindow"]; !ok {
		rule["evalWindow"] = "5m"
	}

	if _, ok := rule["frequency"]; !ok {
		rule["frequency"] = "1m"
	}

	delete(rule, "evaluation")
	delete(rule, "notificationSettings")
	delete(rule, "schemaVersion")
	delete(condition, "thresholds")
}

// shapeV2 is what SigNoz does when it reads a v2alpha1 rule: each threshold
// shows what it did not set, and the notification settings show usePolicy.
func shapeV2(rule, condition map[string]any) {
	thresholds, _ := condition["thresholds"].(map[string]any)
	specs, _ := thresholds["spec"].([]any)

	for _, item := range specs {
		spec, _ := item.(map[string]any)

		for key, value := range map[string]any{"targetUnit": "", "recoveryTarget": nil, "channels": nil} {
			if _, ok := spec[key]; !ok {
				spec[key] = value
			}
		}

		lowerKeys(spec, "op", "matchType")
	}

	settings, _ := rule["notificationSettings"].(map[string]any)
	if _, ok := settings["usePolicy"]; !ok {
		settings["usePolicy"] = false
	}

	renotify, _ := settings["renotify"].(map[string]any)
	if states, ok := renotify["alertStates"].([]any); ok {
		for i, state := range states {
			text, _ := state.(string)
			states[i] = strings.ToLower(text)
		}
	}
}

// lowerKeys writes the text at keys in lower case, without space around it.
func lowerKeys(obj map[string]any, keys ...string) {
	for _, key := range keys {
		if text, ok := obj[key].(string); ok {
			obj[key] = strings.ToLower(strings.TrimSpace(text))
		}
	}
}

// dropEmpty removes the keys whose value is empty, as SigNoz does for an
// optional value when it writes a rule out.
func dropEmpty(obj map[string]any, keys ...string) {
	for _, key := range keys {
		if isEmpty(obj[key]) {
			delete(obj, key)
		}
	}
}

// checkRule is the check SigNoz makes of a rule it is sent: the form of the
// text, the durations, the query specs and what a schema version needs.
func checkRule(body json.RawMessage) (map[string]any, []string) {
	tree, err := decodeTree(body)

	rule, ok := tree.(map[string]any)
	if err != nil || !ok {
		return nil, []string{"failed to parse json"}
	}

	var problems []string

	if name, _ := rule["alert"].(string); name == "" {
		problems = append(problems, "alert: field is required")
	}

	if version, ok := rule["version"].(string); ok && version != "v5" {
		problems = append(problems, fmt.Sprintf("version: only v5 is supported, got %q", version))
	}

	for _, group := range []string{"labels", "annotations"} {
		names, _ := rule[group].(map[string]any)
		for name := range names {
			if !validLabelName(name) {
				problems = append(problems, "invalid label name: "+name)
			}
		}
	}

	problems = append(problems, durationProblems(rule)...)
	problems = append(problems, queryProblems(rule)...)

	condition, _ := rule["condition"].(map[string]any)

	if rule["schemaVersion"] == schemaV2Name {
		if condition["thresholds"] == nil || rule["evaluation"] == nil || rule["notificationSettings"] == nil {
			problems = append(problems, "condition.thresholds, evaluation and notificationSettings are required for schemaVersion v2alpha1")
		}
	} else if condition["target"] == nil || condition["op"] == nil || condition["matchType"] == nil {
		problems = append(problems, "condition.target, condition.op and condition.matchType are required")
	}

	return rule, problems
}

// durationProblems checks the durations of a rule, as SigNoz parses them.
func durationProblems(rule map[string]any) []string {
	var problems []string

	check := func(where string, value any) {
		if text, ok := value.(string); ok {
			if _, err := time.ParseDuration(text); err != nil {
				problems = append(problems, fmt.Sprintf("%s: invalid duration %q", where, text))
			}
		}
	}

	check("evalWindow", rule["evalWindow"])
	check("frequency", rule["frequency"])

	evaluation, _ := rule["evaluation"].(map[string]any)
	spec, _ := evaluation["spec"].(map[string]any)
	check("evaluation.spec.evalWindow", spec["evalWindow"])
	check("evaluation.spec.frequency", spec["frequency"])

	settings, _ := rule["notificationSettings"].(map[string]any)
	renotify, _ := settings["renotify"].(map[string]any)
	check("notificationSettings.renotify.interval", renotify["interval"])

	if renotify["enabled"] == true {
		if text, _ := renotify["interval"].(string); text == "" {
			problems = append(problems, "notificationSettings.renotify.interval: must be a positive duration when renotify is enabled")
		}
	}

	return problems
}

// queryProblems rejects a key in a query spec that SigNoz does not know.
func queryProblems(rule map[string]any) []string {
	var problems []string

	condition, _ := rule["condition"].(map[string]any)
	composite, _ := condition["compositeQuery"].(map[string]any)
	queries, _ := composite["queries"].([]any)

	for _, item := range queries {
		query, _ := item.(map[string]any)
		spec, _ := query["spec"].(map[string]any)

		known := knownBuilderKeys
		if query["type"] == "promql" {
			known = knownPromKeys
		}

		for key := range spec {
			if !slices.Contains(known, key) {
				problems = append(problems, fmt.Sprintf("unknown field %q in query spec", key))
			}
		}
	}

	return problems
}

// unknownChannels returns the channels of a rule that SigNoz does not have.
func unknownChannels(rule map[string]any) []string {
	var names []any

	if list, ok := rule["preferredChannels"].([]any); ok {
		names = append(names, list...)
	}

	condition, _ := rule["condition"].(map[string]any)
	thresholds, _ := condition["thresholds"].(map[string]any)
	specs, _ := thresholds["spec"].([]any)

	for _, item := range specs {
		spec, _ := item.(map[string]any)
		if list, ok := spec["channels"].([]any); ok {
			names = append(names, list...)
		}
	}

	var unknown []string

	for _, name := range names {
		if text, _ := name.(string); !slices.Contains(knownChannels, text) {
			unknown = append(unknown, text)
		}
	}

	return unknown
}

// fail answers with an error in the form SigNoz writes.
func fail(w http.ResponseWriter, status int, message string, problems []string) {
	details := []map[string]string{}
	for _, p := range problems {
		details = append(details, map[string]string{"message": p})
	}

	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "error",
		"error":  map[string]any{"type": "invalid-input", "code": "invalid_input", "message": message, "errors": details},
	})
}

// someoneEdits changes the stored rule as a person would in the SigNoz web
// app: the fields given are set in the rule, and updatedAt moves on.
func (f *fakeSigNoz) someoneEdits(t *testing.T, set map[string]string) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	edits := make([]action.Edit, 0, len(set))

	for _, key := range slices.Sorted(maps.Keys(set)) {
		path, err := action.ParsePath(key)
		require.NoError(t, err)

		edits = append(edits, action.Edit{Path: path, Value: json.RawMessage(set[key])})
	}

	stored, err := action.Apply(f.stored, edits)
	require.NoError(t, err)

	f.stored = stored
	f.edits++
}

// someoneSaves saves the rule in the web app without changing a field.
func (f *fakeSigNoz) someoneSaves() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.edits++
}

// configure changes how the fake answers, under its lock.
func (f *fakeSigNoz) configure(change func(f *fakeSigNoz)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// definitionNow is the definition of the rule as a snapshot would keep it.
func (f *fakeSigNoz) definitionNow(t *testing.T) json.RawMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	raw, err := json.Marshal(f.read())
	require.NoError(t, err)

	var live map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &live))

	def, err := definition(live)
	require.NoError(t, err)

	return def
}

// updatedNow is the updatedAt of the rule now, as the snapshot keeps it.
func (f *fakeSigNoz) updatedNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.updatedAt()
}

// putCount returns how many PUTs the fake received.
func (f *fakeSigNoz) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.puts)
}

// putBodies returns what each PUT sent.
func (f *fakeSigNoz) putBodies() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.puts)
}

// deleteCount returns how many times the rule was deleted.
func (f *fakeSigNoz) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.deletes
}
