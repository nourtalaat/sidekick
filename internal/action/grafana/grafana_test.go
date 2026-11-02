package grafana

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// rulePathOf is the path of the rule the tests change.
const rulePathOf = "/api/v1/provisioning/alert-rules/rule1"

// startRule is the rule as Grafana sends it, with fields that only Grafana
// sets. A rule made in the UI has no provenance.
const startRule = `{
  "id": 7, "uid": "rule1", "orgID": 1, "folderUID": "payments", "ruleGroup": "checkout",
  "title": "Checkout latency", "condition": "C",
  "data": [
    {"refId": "A", "queryType": "", "relativeTimeRange": {"from": 600, "to": 0}, "datasourceUid": "prom",
     "model": {"expr": "avg(checkout_latency_seconds)", "intervalMs": 1000, "maxDataPoints": 43200, "refId": "A"}},
    {"refId": "C", "queryType": "", "relativeTimeRange": {"from": 0, "to": 0}, "datasourceUid": "__expr__",
     "model": {"type": "threshold", "expression": "A", "conditions": [{"evaluator": {"type": "gt", "params": [2]}}],
               "intervalMs": 1000, "maxDataPoints": 43200, "refId": "C"}}
  ],
  "updated": "2026-10-01T00:00:00Z", "noDataState": "NoData", "execErrState": "Error",
  "for": "5m", "keep_firing_for": "0s", "annotations": {"summary": "Latency is high"},
  "labels": {"team": "payments"}, "isPaused": false, "notification_settings": null, "record": null
}`

// startDefinition is what a snapshot keeps of startRule.
const startDefinition = `{
  "folderUID": "payments", "ruleGroup": "checkout",
  "title": "Checkout latency", "condition": "C",
  "data": [
    {"refId": "A", "queryType": "", "relativeTimeRange": {"from": 600, "to": 0}, "datasourceUid": "prom",
     "model": {"expr": "avg(checkout_latency_seconds)", "intervalMs": 1000, "maxDataPoints": 43200, "refId": "A"}},
    {"refId": "C", "queryType": "", "relativeTimeRange": {"from": 0, "to": 0}, "datasourceUid": "__expr__",
     "model": {"type": "threshold", "expression": "A", "conditions": [{"evaluator": {"type": "gt", "params": [2]}}],
               "intervalMs": 1000, "maxDataPoints": 43200, "refId": "C"}}
  ],
  "noDataState": "NoData", "execErrState": "Error",
  "for": "5m", "keep_firing_for": "0s", "annotations": {"summary": "Latency is high"},
  "labels": {"team": "payments"}, "isPaused": false, "notification_settings": null, "record": null
}`

// definitionFields are the fields a write sends, in order.
var definitionFields = []string{
	"annotations", "condition", "data", "execErrState", "folderUID", "for", "isPaused", "keep_firing_for",
	"labels", "noDataState", "notification_settings", "record", "ruleGroup", "title",
}

// The parameters of the changes the tests make.
const (
	// setFor sets the duration as seconds, which Grafana reads back as 10m.
	setFor = `{"ruleUid": "rule1", "set": {"for": "600s"}}`
	// twoFields changes two fields, one of them inside a query.
	twoFields = `{"ruleUid": "rule1", "set": {"for": "600s", "data[1].model.conditions[0].evaluator.params": [3]}}`
	// replacedQuery replaces the first query by one whose model has none of
	// the values Grafana fills in.
	replacedQuery = `{"ruleUid": "rule1", "set": {"for": "600s", "data[0]": {"refId": "A", "queryType": "",
		"relativeTimeRange": {"from": 300, "to": 0}, "datasourceUid": "prom", "model": {"expr": "avg(new_latency_seconds)"}}}}`
)

// fakeToken resolves each secret to its own fake token.
type fakeToken struct{}

func (fakeToken) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	return []byte("fake-" + ref.Key + "\n"), nil
}

// fakeGrafana is a Grafana that keeps one rule. A PUT replaces the rule the
// way the provisioning API does: fields that are left out are reset, keys it
// does not know are dropped, the models of the queries get their defaults,
// durations are written in their canonical form, empty maps are left out,
// the rule is marked as provisioned by the API when the request lacks the
// X-Disable-Provenance header, and the updated time moves on.
type fakeGrafana struct {
	mu      sync.Mutex
	rule    map[string]json.RawMessage
	deleted bool
	edits   int
	puts    []json.RawMessage
	deletes int
	moves   int
	// getStatus, when set, is the status every read answers.
	getStatus int
	// putStatus and putBody, when set, are the answer of every write, which
	// then stores nothing.
	putStatus int
	putBody   string
}

// newFakeGrafana starts the fake API for one test.
func newFakeGrafana(t *testing.T) (*fakehttp.Server, *fakeGrafana) {
	t.Helper()

	f := &fakeGrafana{}
	require.NoError(t, json.Unmarshal([]byte(startRule), &f.rule))

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodGet, rulePathOf, f.get)
	api.HandleFunc(http.MethodPut, rulePathOf, f.put)
	api.HandleFunc(http.MethodDelete, rulePathOf, f.remove)

	return api, f
}

func (f *fakeGrafana) get(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.getStatus != 0 {
		w.WriteHeader(f.getStatus)

		return
	}

	if f.deleted {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	_ = json.NewEncoder(w).Encode(f.rule)
}

func (f *fakeGrafana) put(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	raw, _ := io.ReadAll(r.Body)
	f.puts = append(f.puts, raw)

	if f.putStatus != 0 {
		w.WriteHeader(f.putStatus)
		_, _ = w.Write([]byte(f.putBody))

		return
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		refuse400(w, "invalid body")

		return
	}

	// Without the header the write marks the rule as provisioned by the API,
	// and a rule that has another provenance is refused. With the header a
	// rule that has any provenance is refused.
	current := f.provenance()
	_, disabled := r.Header["X-Disable-Provenance"]

	if (disabled && current != "") || (!disabled && current != "" && current != "api") {
		refuseConflict(w, current)

		return
	}

	next, problem := f.stored(body)
	if problem != "" {
		refuse400(w, problem)

		return
	}

	f.rule = next
	f.edits++

	if !disabled {
		f.rule["provenance"] = json.RawMessage(`"api"`)
	}

	f.rule["updated"] = json.RawMessage(fmt.Sprintf(`"2026-10-02T00:00:%02dZ"`, f.edits))

	_ = json.NewEncoder(w).Encode(f.rule)
}

func (f *fakeGrafana) remove(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if current := f.provenance(); current != "" && current != "api" {
		refuseConflict(w, current)

		return
	}

	f.deletes++
	f.deleted = true

	w.WriteHeader(http.StatusNoContent)
}

// stored is the rule as Grafana keeps what a PUT sent, or the problem that
// makes it answer 400. It is called with the lock held.
func (f *fakeGrafana) stored(body map[string]json.RawMessage) (map[string]json.RawMessage, string) {
	next := map[string]json.RawMessage{}

	for _, key := range []string{"id", "uid", "orgID", "updated", "provenance"} {
		if v, ok := f.rule[key]; ok {
			next[key] = v
		}
	}

	for _, key := range []string{"folderUID", "ruleGroup", "title", "condition", "noDataState", "execErrState"} {
		next[key] = textOrEmpty(body[key])
	}

	if string(next["folderUID"]) != string(f.rule["folderUID"]) || string(next["ruleGroup"]) != string(f.rule["ruleGroup"]) {
		f.moves++
	}

	if problem := problemWith(next); problem != "" {
		return nil, problem
	}

	next["isPaused"] = json.RawMessage("false")
	if string(body["isPaused"]) == "true" {
		next["isPaused"] = json.RawMessage("true")
	}

	for _, key := range []string{"for", "keep_firing_for"} {
		if _, known := f.rule[key]; !known && key == "keep_firing_for" {
			continue
		}

		d, err := model.ParseDuration(textOrDefault(body[key], "0s"))
		if err != nil {
			return nil, key + " is not a duration"
		}

		next[key], _ = json.Marshal(d.String())
	}

	data, problem := storedQueries(body["data"])
	if problem != "" {
		return nil, problem
	}

	next["data"] = data

	for _, key := range []string{"labels", "annotations"} {
		var entries map[string]string
		if len(body[key]) > 0 {
			if err := json.Unmarshal(body[key], &entries); err != nil {
				return nil, key + " must be an object of text"
			}
		}

		if len(entries) > 0 {
			next[key], _ = json.Marshal(entries)
		}
	}

	if _, ok := next["labels"]; ok {
		var labels map[string]string

		_ = json.Unmarshal(next["labels"], &labels)

		if _, reserved := labels["__grafana_autogenerated__"]; reserved {
			return nil, "system reserved label __grafana_autogenerated__ cannot be defined"
		}
	}

	var notes map[string]string

	_ = json.Unmarshal(next["annotations"], &notes)

	if (notes["__dashboardUid__"] == "") != (notes["__panelId__"] == "") {
		return nil, "both annotations __dashboardUid__ and __panelId__ must be specified"
	}

	for _, key := range []string{"notification_settings", "record"} {
		next[key] = json.RawMessage("null")
		if v, ok := body[key]; ok {
			next[key] = v
		}
	}

	return next, ""
}

// problemWith is the reason Grafana refuses the plain fields of a rule, or "".
func problemWith(next map[string]json.RawMessage) string {
	var title, noData, execErr string

	_ = json.Unmarshal(next["title"], &title)
	_ = json.Unmarshal(next["noDataState"], &noData)
	_ = json.Unmarshal(next["execErrState"], &execErr)

	switch {
	case title == "":
		return "invalid alert rule: title is empty"
	case len(title) > maxTitleBytes:
		return "invalid alert rule: name length should not be greater than 190"
	case !slices.Contains(noDataStates, noData):
		return "unknown NoData state option " + noData
	case !slices.Contains(execErrStates, execErr):
		return "unknown Error state option " + execErr
	default:
		return ""
	}
}

// storedQueries is the data of a rule as Grafana keeps it, or the problem.
func storedQueries(raw json.RawMessage) (json.RawMessage, string) {
	var queries []struct {
		RefID             string `json:"refId"`
		QueryType         string `json:"queryType"`
		RelativeTimeRange struct {
			From float64 `json:"from"`
			To   float64 `json:"to"`
		} `json:"relativeTimeRange"`
		DatasourceUID string          `json:"datasourceUid"`
		Model         json.RawMessage `json:"model"`
	}

	if err := json.Unmarshal(raw, &queries); err != nil {
		return nil, "invalid data"
	}

	if len(queries) == 0 {
		return nil, "invalid alert rule: no queries or expressions are found"
	}

	out := make([]map[string]any, 0, len(queries))

	for _, q := range queries {
		body := map[string]any{}
		if len(q.Model) > 0 {
			_ = json.Unmarshal(q.Model, &body)
		}

		if body == nil {
			body = map[string]any{}
		}

		if n, ok := body["maxDataPoints"].(float64); !ok || n == 0 {
			body["maxDataPoints"] = 43200.0
		}

		if n, ok := body["intervalMs"].(float64); !ok || n == 0 {
			body["intervalMs"] = 1000.0
		}

		body["refId"] = q.RefID

		if kind, ok := body["queryType"].(string); ok {
			q.QueryType = kind
		}

		expression := q.DatasourceUID == "__expr__" || q.DatasourceUID == "-100"
		if !expression && q.RelativeTimeRange.From <= q.RelativeTimeRange.To {
			return nil, "invalid alert rule: invalid relative time range for " + q.RefID
		}

		out = append(out, map[string]any{
			"refId": q.RefID, "queryType": q.QueryType, "datasourceUid": q.DatasourceUID, "model": body,
			"relativeTimeRange": map[string]int64{"from": int64(q.RelativeTimeRange.From), "to": int64(q.RelativeTimeRange.To)},
		})
	}

	stored, err := json.Marshal(out)
	if err != nil {
		return nil, "cannot encode the queries"
	}

	return stored, ""
}

// provenance is the provenance of the rule, with the lock held.
func (f *fakeGrafana) provenance() string {
	var p string

	_ = json.Unmarshal(f.rule["provenance"], &p)

	return p
}

// someoneEdits changes the rule in Grafana, as a person would in the UI: the
// fields given replace the fields of the rule, and updated moves on.
func (f *fakeGrafana) someoneEdits(t *testing.T, fields string) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	var set map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(fields), &set))

	for k, v := range set {
		f.rule[k] = v
	}

	f.edits++
	f.rule["updated"] = json.RawMessage(fmt.Sprintf(`"2026-10-03T00:00:%02dZ"`, f.edits))
}

// definitionNow is the definition of the rule as a snapshot would keep it.
func (f *fakeGrafana) definitionNow(t *testing.T) json.RawMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	def, err := definition(f.rule)
	require.NoError(t, err)

	return def
}

// provenanceNow is the provenance of the rule: "" while the UI can edit it.
func (f *fakeGrafana) provenanceNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.provenance()
}

func (f *fakeGrafana) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.puts)
}

// putBodies returns what each PUT sent, as fields.
func (f *fakeGrafana) putBodies(t *testing.T) []map[string]json.RawMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]map[string]json.RawMessage, 0, len(f.puts))

	for _, raw := range f.puts {
		var body map[string]json.RawMessage

		require.NoError(t, json.Unmarshal(raw, &body))

		out = append(out, body)
	}

	return out
}

func (f *fakeGrafana) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.deletes
}

func (f *fakeGrafana) moveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.moves
}

// configure changes how the fake answers, under its lock.
func (f *fakeGrafana) configure(change func(f *fakeGrafana)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// failReads makes every read of the rule answer with status.
func (f *fakeGrafana) failReads(status int) {
	f.configure(func(f *fakeGrafana) { f.getStatus = status })
}

// requireHoldsAfter requires the rule to have the values the snapshot
// writes, as Grafana reads them back.
func requireHoldsAfter(t *testing.T, g *fakeGrafana, snap action.Snapshot) {
	t.Helper()

	ok, err := holdsAfter(g.definitionNow(t), snap)
	require.NoError(t, err)
	require.True(t, ok, "the rule has the new values")
}

// requireHoldsBefore requires the rule to have the values from before the
// snapshot's change.
func requireHoldsBefore(t *testing.T, g *fakeGrafana, snap action.Snapshot) {
	t.Helper()

	ok, err := holdsBefore(g.definitionNow(t), snap)
	require.NoError(t, err)
	require.True(t, ok, "the rule has the old values")
}

// limitsOf are the limits the tests use unless a test changes them.
func limitsOf() Limits {
	return Limits{
		FolderUIDs: []string{"payments"},
		Fields:     []string{"for", "data", "labels", "annotations", "noDataState", "execErrState", "title", "condition", "isPaused", "keep_firing_for"},
	}
}

// newTestAction returns the action, talking to api, with the limits given.
func newTestAction(t *testing.T, api *fakehttp.Server, limits Limits) *Rule {
	t.Helper()

	a, err := New(t.Context(), "grafana-rule", Config{URL: api.URL(), TokenSecret: tokenRef, Limits: limits}, registry.Deps{Secrets: fakeToken{}, HTTP: api.Client()})
	require.NoError(t, err)

	r, ok := a.(*Rule)
	require.True(t, ok)

	return r
}

// invocationOf is the approved request to run params with snap.
func invocationOf(params string, snap *action.Snapshot) action.Invocation {
	return action.Invocation{
		ProposalID:      "0192d3a4-0000-7000-8000-0000000000f1",
		InvestigationID: "0192d3a4-0000-7000-8000-0000000000e2",
		Params:          json.RawMessage(params),
		IdempotencyKey:  "sidekick/0192d3a4-0000-7000-8000-0000000000f1",
		ApprovedBy:      "u1",
		Snapshot:        snap,
	}
}

// proposed makes the snapshot of params.
func proposed(t *testing.T, r *Rule, params string) action.Snapshot {
	t.Helper()

	snap, err := r.Snapshot(t.Context(), json.RawMessage(params))
	require.NoError(t, err)

	return snap
}

// writes returns the requests that change something.
func writes(api *fakehttp.Server) []fakehttp.Request {
	var out []fakehttp.Request

	for _, req := range api.Requests() {
		if req.Method != http.MethodGet {
			out = append(out, req)
		}
	}

	return out
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())

	snap := proposed(t, r, `{"ruleUid": "rule1", "set": {"for": "600s", "labels.severity": "critical", "data[1].model.conditions[0].evaluator.params": [3]}}`)

	require.Equal(t, "grafana:alert_rule/rule1", snap.Target)
	require.Equal(t, "2026-10-01T00:00:00Z", snap.Version)
	require.JSONEq(t, startDefinition, string(snap.Before), "only the fields the action can write are kept")

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Equal(t, []action.Change{
		{Path: "data[1].model.conditions[0].evaluator.params", Before: new("[2]"), After: new("[3]")},
		{Path: "for", Before: new(`"5m"`), After: new(`"600s"`)},
		{Path: "labels.severity", After: new(`"critical"`)},
	}, changes)

	require.Zero(t, g.putCount(), "proposing writes nothing")
	require.Empty(t, writes(api))

	for _, req := range api.Requests() {
		require.Equal(t, "Bearer fake-token", req.Header.Get("Authorization"))
		require.Equal(t, rulePathOf, req.Path)
	}
}

func TestSnapshotOfADelete(t *testing.T) {
	t.Parallel()

	api, _ := newFakeGrafana(t)
	limits := limitsOf()
	limits.AllowDelete = true
	r := newTestAction(t, api, limits)

	snap := proposed(t, r, `{"ruleUid": "rule1", "delete": true}`)
	require.True(t, snap.Deletes())
	require.JSONEq(t, startDefinition, string(snap.Before))
	require.Equal(t, "2026-10-01T00:00:00Z", snap.Version)
}

func TestSnapshotLimits(t *testing.T) {
	t.Parallel()

	rename := `{"ruleUid": "rule1", "set": {"title": "Renamed"}}`
	renameInside := `{"ruleUid": "rule1", "set": {"title": "Checkout errors"}}`

	tests := []struct {
		name    string
		limits  func(l *Limits)
		params  string
		wantErr string
	}{
		{name: "a rule in an allowed folder", params: setFor},
		{name: "a rule in another folder", limits: func(l *Limits) { l.FolderUIDs = []string{"search"} }, params: setFor, wantErr: "limits.folderUids"},
		{name: "a UID that is not listed", limits: func(l *Limits) { l.RuleUIDs = []string{"other"} }, params: setFor, wantErr: "limits.ruleUids"},
		{name: "a listed UID", limits: func(l *Limits) { l.RuleUIDs = []string{"rule1"} }, params: setFor},
		{name: "a title that matches a pattern", limits: func(l *Limits) { l.TitlePatterns = []string{"Checkout *"} }, params: setFor},
		{name: "a title that matches no pattern", limits: func(l *Limits) { l.TitlePatterns = []string{"Search *"} }, params: setFor, wantErr: "limits.titlePatterns"},
		{name: "a rename that leaves the pattern", limits: func(l *Limits) { l.TitlePatterns = []string{"Checkout *"} }, params: rename, wantErr: "out of limits.titlePatterns"},
		{name: "a rename that stays in the pattern", limits: func(l *Limits) { l.TitlePatterns = []string{"Checkout *"} }, params: renameInside},
		{
			name: "a rename of a rule that is allowed by its UID", params: rename,
			limits: func(l *Limits) { l.TitlePatterns, l.RuleUIDs = []string{"Checkout *"}, []string{"rule1"} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, _ := newFakeGrafana(t)

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			_, err := newTestAction(t, api, limits).Snapshot(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		limits  func(l *Limits)
		prepare func(t *testing.T, g *fakeGrafana)
		wantErr string
	}{
		{name: "a field outside limits.fields", params: `{"ruleUid": "rule1", "set": {"labels.x": "y"}}`, limits: func(l *Limits) { l.Fields = []string{"for"} }, wantErr: "is not one of limits.fields"},
		{name: "the folder cannot be changed", params: `{"ruleUid": "rule1", "set": {"folderUID": "other"}}`, wantErr: `the field "folderUID" is not one of limits.fields`},
		{name: "the group cannot be changed", params: `{"ruleUid": "rule1", "set": {"ruleGroup": "other"}}`, wantErr: `the field "ruleGroup" is not one of limits.fields`},
		{name: "a no data state Grafana does not know", params: `{"ruleUid": "rule1", "set": {"noDataState": "Bogus"}}`, wantErr: "the value must be one of"},
		{name: "an error state Grafana does not know", params: `{"ruleUid": "rule1", "set": {"execErrState": "NoData"}}`, wantErr: "the value must be one of"},
		{name: "a duration that is no duration", params: `{"ruleUid": "rule1", "set": {"for": "soon"}}`, wantErr: "is not a duration"},
		{name: "a duration that is a number", params: `{"ruleUid": "rule1", "set": {"for": 5}}`, wantErr: "a duration must be text"},
		{name: "a duration that is null", params: `{"ruleUid": "rule1", "set": {"for": null}}`, wantErr: "a duration must be text"},
		{name: "an empty title", params: `{"ruleUid": "rule1", "set": {"title": ""}}`, wantErr: "the title must not be empty"},
		{name: "a title that is too long", params: fmt.Sprintf(`{"ruleUid": "rule1", "set": {"title": %q}}`, strings.Repeat("a", 191)), wantErr: "Grafana keeps at most 190"},
		{name: "a pause flag that is no flag", params: `{"ruleUid": "rule1", "set": {"isPaused": "yes"}}`, wantErr: "true or false"},
		{name: "a label that is no text", params: `{"ruleUid": "rule1", "set": {"labels.n": 3}}`, wantErr: "must be text"},
		{name: "a label Grafana reserves", params: `{"ruleUid": "rule1", "set": {"labels.__grafana_autogenerated__": "true"}}`, wantErr: "reserved by Grafana"},
		{name: "the dashboard annotation", params: `{"ruleUid": "rule1", "set": {"annotations.__dashboardUid__": "d1"}}`, wantErr: "ties the rule to a dashboard panel"},
		{name: "a condition that is no query", params: `{"ruleUid": "rule1", "set": {"condition": "Z"}}`, wantErr: `the condition "Z" is not the refId of any query`},
		{name: "data that is no list", params: `{"ruleUid": "rule1", "set": {"data": "x"}}`, wantErr: "data must be a list"},
		{name: "a key of a query that Grafana drops", params: `{"ruleUid": "rule1", "set": {"data[0].extra": 1}}`, wantErr: `Grafana drops "extra"`},
		{name: "a time range that does not reach back", params: `{"ruleUid": "rule1", "set": {"data[0].relativeTimeRange.from": 0}}`, wantErr: "from must be greater than"},
		{name: "a time range in fractions of a second", params: `{"ruleUid": "rule1", "set": {"data[0].relativeTimeRange.from": 600.5}}`, wantErr: "whole number of seconds"},
		{name: "a query type that is no text", params: `{"ruleUid": "rule1", "set": {"data[0].model.queryType": 3}}`, wantErr: "model.queryType must be text"},
		{name: "a model default set to zero", params: `{"ruleUid": "rule1", "set": {"data[0].model.maxDataPoints": 0}}`, wantErr: "must be a number other than 0"},
		{name: "a model refId that is not the query's", params: `{"ruleUid": "rule1", "set": {"data[0].model.refId": "Z"}}`, wantErr: "must be the refId of the query"},
		{name: "a change that changes nothing", params: `{"ruleUid": "rule1", "set": {"for": "300s"}}`, wantErr: "has these values already"},
		{
			name: "a field this Grafana does not show", params: `{"ruleUid": "rule1", "set": {"keep_firing_for": "5m"}}`,
			prepare: func(t *testing.T, g *fakeGrafana) {
				g.configure(func(f *fakeGrafana) { delete(f.rule, "keep_firing_for") })
			},
			wantErr: "would drop a value written there",
		},
		{
			name: "a recording rule", params: setFor, wantErr: "recording rule",
			prepare: func(t *testing.T, g *fakeGrafana) {
				g.someoneEdits(t, `{"record": {"metric": "checkout_latency", "from": "A"}}`)
			},
		},
		{
			name: "a rule from files", params: setFor, wantErr: `provisioned (provenance "file")`,
			prepare: func(t *testing.T, g *fakeGrafana) { g.someoneEdits(t, `{"provenance": "file"}`) },
		},
		{
			name: "a rule made through the API", params: setFor, wantErr: `provisioned (provenance "api")`,
			prepare: func(t *testing.T, g *fakeGrafana) { g.someoneEdits(t, `{"provenance": "api"}`) },
		},
		{
			name: "a delete of a provisioned rule", params: `{"ruleUid": "rule1", "delete": true}`, wantErr: "provisioned",
			limits:  func(l *Limits) { l.AllowDelete = true },
			prepare: func(t *testing.T, g *fakeGrafana) { g.someoneEdits(t, `{"provenance": "file"}`) },
		},
		{name: "a delete the limits do not allow", params: `{"ruleUid": "rule1", "delete": true}`, wantErr: "limits.allowDelete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, g := newFakeGrafana(t)

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			if tt.prepare != nil {
				tt.prepare(t, g)
			}

			_, err := newTestAction(t, api, limits).Snapshot(t.Context(), json.RawMessage(tt.params))
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
			require.Zero(t, g.putCount())
		})
	}
}

// TestTheRefusalToChangeAProvisionedRuleSaysWhoCanAllowIt checks the message
// the model reads: the owner can allow changes, and the model is told not to
// look for another way.
func TestTheRefusalToChangeAProvisionedRuleSaysWhoCanAllowIt(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	g.someoneEdits(t, `{"provenance": "file"}`)

	_, err := newTestAction(t, api, limitsOf()).Snapshot(t.Context(), json.RawMessage(setFor))
	require.ErrorContains(t, err, "Only the owner of the rule can allow changes, by removing the provisioning in Grafana")
	require.ErrorContains(t, err, "Do not propose another way to change this rule")
}

func TestSnapshotOfARuleThatIsGone(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	g.failReads(http.StatusNotFound)

	_, err := newTestAction(t, api, limitsOf()).Snapshot(t.Context(), json.RawMessage(setFor))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "Grafana answered 404")
}

// TestRemovingALabelNeverSendsANull checks that a label the model sets to
// null is removed: Grafana would store a null label as an empty text.
func TestRemovingALabelNeverSendsANull(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())

	params := `{"ruleUid": "rule1", "set": {"labels.team": null}}`
	snap := proposed(t, r, params)

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Equal(t, []action.Change{{Path: "labels.team", Before: new(`"payments"`)}}, changes)

	var after map[string]json.RawMessage

	require.NoError(t, json.Unmarshal(snap.After, &after))
	require.JSONEq(t, `{}`, string(after["labels"]))

	_, err = r.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)

	require.JSONEq(t, `{}`, string(g.putBodies(t)[0]["labels"]), "no null goes to Grafana")
	require.NotContains(t, string(g.definitionNow(t)), `"labels"`, "Grafana leaves an empty map out of its answer")

	_, err = r.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err, "the label is gone: the change is in place")
	require.Equal(t, 1, g.putCount())

	_, err = r.Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.NoError(t, err, "what Grafana kept is the change, in another form")
	require.JSONEq(t, `{"team": "payments"}`, labelsNow(t, g))
	requireHoldsBefore(t, g, snap)
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		limits    func(l *Limits)
		wantErr   string
		wantLimit bool
	}{
		{name: "a change", params: setFor},
		{name: "a delete the limits allow", params: `{"ruleUid": "rule1", "delete": true}`, limits: func(l *Limits) { l.AllowDelete = true }},
		{name: "a delete the limits do not allow", params: `{"ruleUid": "rule1", "delete": true}`, wantErr: "limits.allowDelete", wantLimit: true},
		{name: "no rule", params: `{"set": {"for": "5m"}}`, wantErr: "is not the UID of a Grafana rule"},
		{name: "a UID that is not one", params: `{"ruleUid": "a b", "set": {"for": "5m"}}`, wantErr: "is not the UID of a Grafana rule"},
		{name: "a parameter the action does not have", params: `{"ruleUid": "rule1", "set": {"for": "5m"}, "force": true}`, wantErr: "force"},
		{name: "set and delete", params: `{"ruleUid": "rule1", "set": {"for": "5m"}, "delete": true}`, wantErr: "either set or delete"},
		{name: "neither set nor delete", params: `{"ruleUid": "rule1"}`, wantErr: "give set with the fields to change"},
		{name: "a field outside limits.fields", params: `{"ruleUid": "rule1", "set": {"provenance": "x"}}`, wantErr: "is not one of limits.fields", wantLimit: true},
		{name: "a path that is no path", params: `{"ruleUid": "rule1", "set": {"data..x": 1}}`, wantErr: "invalid field path"},
		{name: "too many fields", params: manyKeys(action.MaxChanges + 1), wantErr: "set names more than"},
		{name: "a UID that is not listed", params: setFor, limits: func(l *Limits) { l.RuleUIDs = []string{"other"} }, wantErr: "limits.ruleUids", wantLimit: true},
		{name: "a UID that is listed", params: setFor, limits: func(l *Limits) { l.RuleUIDs = []string{"rule1"} }},
		{name: "a UID a title pattern may allow", params: setFor, limits: func(l *Limits) { l.RuleUIDs, l.TitlePatterns = []string{"other"}, []string{"*"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, _ := newFakeGrafana(t)

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			err := newTestAction(t, api, limits).Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)

			if tt.wantLimit {
				require.ErrorIs(t, err, action.ErrLimit)
			}

			require.Empty(t, api.Requests(), "validation makes no call")
		})
	}
}

// manyKeys is the parameters of a change that names n labels.
func manyKeys(n int) string {
	set := make([]string, n)
	for i := range set {
		set[i] = fmt.Sprintf(`"labels.k%d": "v"`, i)
	}

	return `{"ruleUid": "rule1", "set": {` + strings.Join(set, ", ") + `}}`
}

func TestSpec(t *testing.T) {
	t.Parallel()

	api, _ := newFakeGrafana(t)

	limits := limitsOf()
	limits.RuleUIDs, limits.TitlePatterns = []string{"rule1"}, []string{"Checkout *"}
	spec := newTestAction(t, api, limits).Spec()

	require.Equal(t, "grafana-rule", spec.Name)
	require.True(t, json.Valid(spec.ParamsSchema))
	require.Contains(t, spec.Description, "Allowed folder UIDs: payments")
	require.Contains(t, spec.Description, "Allowed rule UIDs: rule1")
	require.Contains(t, spec.Description, "Checkout *")
	require.Contains(t, spec.Description, "Fields that may change, with every field below them: for, data")
	require.Contains(t, spec.Description, "provisioned")
	require.NotContains(t, spec.Description, "deletes it", "delete is not allowed")

	limits.AllowDelete = true
	require.Contains(t, newTestAction(t, api, limits).Spec().Description, "A delete cannot be undone in Sidekick")
}

func TestExecute(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, twoFields)

	res, err := r.Execute(t.Context(), invocationOf(twoFields, &snap))
	require.NoError(t, err)
	require.Equal(t, `Changed 2 fields of rule rule1, "Checkout latency".`, res.Summary)
	require.Equal(t, []action.Link{{Title: "Rule rule1", URL: strings.TrimSuffix(api.URL(), "/") + "/alerting/grafana/rule1/view"}}, res.Links)

	require.Equal(t, 1, g.putCount())
	body := g.putBodies(t)[0]
	require.Equal(t, definitionFields, sortedKeys(body), "the whole rule is sent, without the fields Grafana sets itself")
	requireHoldsAfter(t, g, snap)

	require.Empty(t, g.provenanceNow(), "the rule can still be edited in the Grafana UI")
	require.Zero(t, g.moveCount(), "the rule stays in its folder and group")
}

// TestWritesCarryTheProvenanceHeader checks that every request that changes
// something, a change, an Undo and a delete, sends X-Disable-Provenance, so
// Grafana does not mark the rule as provisioned and lock it in the UI.
func TestWritesCarryTheProvenanceHeader(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	limits := limitsOf()
	limits.AllowDelete = true
	r := newTestAction(t, api, limits)

	snap := proposed(t, r, setFor)
	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.NoError(t, err)

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, false)
	require.NoError(t, err)

	remove := `{"ruleUid": "rule1", "delete": true}`
	deleteSnap := proposed(t, r, remove)
	_, err = r.Execute(t.Context(), invocationOf(remove, &deleteSnap))
	require.NoError(t, err)

	changing := writes(api)
	require.Len(t, changing, 3)
	require.Equal(t, []string{http.MethodPut, http.MethodPut, http.MethodDelete}, []string{changing[0].Method, changing[1].Method, changing[2].Method})

	for _, req := range changing {
		require.Equal(t, "true", req.Header.Get("X-Disable-Provenance"), req.Method)
		require.Equal(t, "Bearer fake-token", req.Header.Get("Authorization"))
	}

	require.Equal(t, 1, g.deleteCount())
}

// TestExecuteSendsEveryFieldAsItIs checks that a field the change does not
// touch goes back with the value it has.
func TestExecuteSendsEveryFieldAsItIs(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.NoError(t, err)

	var want map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(startDefinition), &want))

	body := g.putBodies(t)[0]

	for _, key := range definitionFields {
		if key == "for" {
			require.JSONEq(t, `"600s"`, string(body[key]))

			continue
		}

		require.JSONEq(t, string(want[key]), string(body[key]), key)
	}
}

// TestAFieldGrafanaDoesNotKnowGoesBack checks that a field a newer Grafana
// adds to a rule is sent back as it is, not reset by the write.
func TestAFieldGrafanaDoesNotKnowGoesBack(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	g.someoneEdits(t, `{"somethingNew": {"level": 2}}`)

	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.NoError(t, err)
	require.JSONEq(t, `{"level": 2}`, string(g.putBodies(t)[0]["somethingNew"]))
}

func TestExecuteWritesWhatTheApproverRead(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	// Different parameters, the same snapshot: the snapshot is what runs.
	_, err := r.Execute(t.Context(), invocationOf(`{"ruleUid": "rule1", "set": {"for": "99m"}}`, &snap))
	require.NoError(t, err)
	require.JSONEq(t, `"600s"`, string(g.putBodies(t)[0]["for"]))
}

func TestExecuteRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prepare  func(t *testing.T, g *fakeGrafana)
		limits   func(l *Limits)
		snapshot func(t *testing.T, snap *action.Snapshot)
		wantErr  error
	}{
		{
			name: "someone edited the rule since the snapshot", wantErr: action.ErrConflict,
			prepare: func(t *testing.T, g *fakeGrafana) {
				g.someoneEdits(t, `{"annotations": {"summary": "Edited by a person"}}`)
			},
		},
		{
			name: "the same field was edited to another value", wantErr: action.ErrConflict,
			prepare: func(t *testing.T, g *fakeGrafana) { g.someoneEdits(t, `{"for": "15m"}`) },
		},
		{
			name: "the rule was saved again with the same content", wantErr: action.ErrConflict,
			prepare: func(t *testing.T, g *fakeGrafana) { g.someoneEdits(t, `{}`) },
		},
		{
			name: "the rule became provisioned", wantErr: action.ErrLimit,
			prepare: func(t *testing.T, g *fakeGrafana) { g.someoneEdits(t, `{"provenance": "file"}`) },
		},
		{
			name: "limits tightened since the proposal", wantErr: action.ErrLimit,
			limits: func(l *Limits) { l.Fields = []string{"labels"} },
		},
		{
			name: "the folder taken out of the limits", wantErr: action.ErrLimit,
			limits: func(l *Limits) { l.FolderUIDs = []string{"search"} },
		},
		{
			name: "a snapshot of another rule", wantErr: action.ErrPermanent,
			snapshot: func(_ *testing.T, snap *action.Snapshot) { snap.Target = "grafana:alert_rule/other" },
		},
		{
			name: "a snapshot that moves the rule to another folder", wantErr: action.ErrLimit,
			snapshot: func(t *testing.T, snap *action.Snapshot) {
				t.Helper()

				var after map[string]json.RawMessage

				require.NoError(t, json.Unmarshal(snap.After, &after))

				after["folderUID"] = json.RawMessage(`"search"`)

				moved, err := json.Marshal(after)
				require.NoError(t, err)

				snap.After = moved
			},
		},
		{
			name: "the rule is gone", wantErr: action.ErrPermanent,
			prepare: func(t *testing.T, g *fakeGrafana) { g.failReads(http.StatusNotFound) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, g := newFakeGrafana(t)
			snap := proposed(t, newTestAction(t, api, limitsOf()), setFor)

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			if tt.snapshot != nil {
				tt.snapshot(t, &snap)
			}

			if tt.prepare != nil {
				tt.prepare(t, g)
			}

			_, err := newTestAction(t, api, limits).Execute(t.Context(), invocationOf(setFor, &snap))
			require.ErrorIs(t, err, tt.wantErr)
			require.Zero(t, g.putCount(), "nothing was written")
		})
	}

	api, _ := newFakeGrafana(t)
	_, err := newTestAction(t, api, limitsOf()).Execute(t.Context(), invocationOf(setFor, nil))
	require.ErrorIs(t, err, action.ErrPermanent, "a proposal with no snapshot")
}

// TestExecuteTwiceHasTheEffectOfOne is a worker that died after the write and
// before the result was saved: the job runs again, finds the rule as the
// snapshot says, and writes nothing more.
func TestExecuteTwiceHasTheEffectOfOne(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, twoFields)

	first, err := r.Execute(t.Context(), invocationOf(twoFields, &snap))
	require.NoError(t, err)

	second, err := r.Execute(t.Context(), invocationOf(twoFields, &snap))
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Equal(t, 1, g.putCount())
}

// TestExecuteTwiceAfterGrafanaNormalisedTheWrite is a worker that died after
// the write when Grafana had changed the form of what it kept: 600s reads
// back as 10m, and the model of the new query gains the defaults Grafana
// fills in. The job runs again, finds the change in place, and writes
// nothing more instead of reporting a conflict.
func TestExecuteTwiceAfterGrafanaNormalisedTheWrite(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, replacedQuery)

	_, err := r.Execute(t.Context(), invocationOf(replacedQuery, &snap))
	require.NoError(t, err)

	now := string(g.definitionNow(t))
	require.Contains(t, now, `"for":"10m"`, "Grafana reads the duration back in its own form")
	require.Contains(t, now, `"maxDataPoints":43200`, "and adds the defaults of the model")
	require.Contains(t, now, `"model":{"expr":"avg(new_latency_seconds)","intervalMs":1000,"maxDataPoints":43200,"refId":"A"}`)

	_, err = r.Execute(t.Context(), invocationOf(replacedQuery, &snap))
	require.NoError(t, err, "the change is in place")
	require.Equal(t, 1, g.putCount())
}

func TestExecuteStillFindsAnEditAfterANormalisedWrite(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, replacedQuery)

	_, err := r.Execute(t.Context(), invocationOf(replacedQuery, &snap))
	require.NoError(t, err)

	g.someoneEdits(t, `{"for": "15m"}`)

	_, err = r.Execute(t.Context(), invocationOf(replacedQuery, &snap))
	require.ErrorIs(t, err, action.ErrConflict)
	require.Equal(t, 1, g.putCount())
}

func TestUndoAfterANormalisedWriteNeedsNoForce(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, replacedQuery)

	_, err := r.Execute(t.Context(), invocationOf(replacedQuery, &snap))
	require.NoError(t, err)

	_, err = r.Undo(t.Context(), invocationOf(replacedQuery, &snap), snap, false)
	require.NoError(t, err, "what Grafana kept is the change, in another form")
	requireHoldsBefore(t, g, snap)
	require.Contains(t, string(g.definitionNow(t)), `"for":"5m"`)
	require.Contains(t, string(g.definitionNow(t)), `"expr":"avg(checkout_latency_seconds)"`)
}

func TestGrafanaRefusingTheRuleFailsTheProposalAndChangesNothing(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	g.configure(func(f *fakeGrafana) {
		f.putStatus, f.putBody = http.StatusConflict, `{"message": "a rule with this title exists in the folder"}`
	})

	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "a rule with this title exists in the folder")
	requireHoldsBefore(t, g, snap)
}

func TestAServerErrorIsTriedAgain(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	g.configure(func(f *fakeGrafana) { f.putStatus = http.StatusBadGateway })

	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent)
}

func TestUndo(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())

	params := `{"ruleUid": "rule1", "set": {"for": "600s", "labels.severity": "critical"}}`
	snap := proposed(t, r, params)

	_, err := r.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	require.JSONEq(t, `{"team": "payments", "severity": "critical"}`, labelsNow(t, g))

	// Someone changes a field the action did not touch.
	g.someoneEdits(t, `{"annotations": {"summary": "Edited by a person"}}`)

	res, err := r.Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, `Restored 2 fields of rule rule1, "Checkout latency".`, res.Summary)
	require.Equal(t, "Rule rule1", res.Links[0].Title)

	requireHoldsBefore(t, g, snap)
	require.JSONEq(t, `{"team": "payments"}`, labelsNow(t, g))
	require.Contains(t, string(g.definitionNow(t)), `"summary":"Edited by a person"`, "the changed fields are back, the other edit stays")

	puts := g.putBodies(t)
	require.Equal(t, definitionFields, sortedKeys(puts[len(puts)-1]), "the whole rule is sent")
	require.Empty(t, g.provenanceNow(), "the rule can still be edited in the Grafana UI")
}

func TestUndoAfterSomeoneChangedAFieldTheActionChanged(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.NoError(t, err)

	g.someoneEdits(t, `{"for": "15m"}`)
	before := g.putCount()

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, false)

	var conflict *action.ConflictError

	require.ErrorAs(t, err, &conflict)
	require.ErrorIs(t, err, action.ErrConflict)
	require.JSONEq(t, string(g.definitionNow(t)), string(conflict.Live), "the error carries the rule as it is")
	require.Equal(t, before, g.putCount(), "nothing was written")

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, true)
	require.NoError(t, err)
	requireHoldsBefore(t, g, snap)
}

func TestUndoTwiceHasTheEffectOfOne(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.NoError(t, err)

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, false)
	require.NoError(t, err)

	before := g.putCount()

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, false)
	require.NoError(t, err, "the rule is restored already")
	require.Equal(t, before, g.putCount())
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	r := newTestAction(t, api, limitsOf())
	snap := proposed(t, r, setFor)

	_, err := r.Execute(t.Context(), invocationOf(setFor, &snap))
	require.NoError(t, err)

	tight := newTestAction(t, api, Limits{FolderUIDs: []string{"payments"}, Fields: []string{"labels"}})
	_, err = tight.Undo(t.Context(), invocationOf(`{"ruleUid": "rule1", "set": {"labels.x": "y"}}`, &snap), snap, false)
	require.ErrorIs(t, err, action.ErrLimit, "limits tightened since the change")

	gone := action.Snapshot{Target: snap.Target, Before: snap.Before}
	_, err = r.Undo(t.Context(), invocationOf(setFor, &gone), gone, false)
	require.ErrorIs(t, err, action.ErrPermanent, "a delete cannot be undone")

	g.someoneEdits(t, `{"provenance": "file"}`)

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, true)
	require.ErrorIs(t, err, action.ErrLimit, "a rule that became provisioned is left alone, even with force")

	g.failReads(http.StatusInternalServerError)

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, false)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent, "a server error is tried again")

	g.failReads(http.StatusUnauthorized)

	_, err = r.Undo(t.Context(), invocationOf(setFor, &snap), snap, false)
	require.ErrorIs(t, err, action.ErrPermanent, "a token Grafana refuses is not tried again")
}

func TestUndoCannotTakeTheRuleOutOfItsTitlePattern(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)

	limits := limitsOf()
	limits.TitlePatterns = []string{"Checkout *"}
	r := newTestAction(t, api, limits)

	params := `{"ruleUid": "rule1", "set": {"title": "Checkout errors"}}`
	snap := proposed(t, r, params)

	_, err := r.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)

	// The patterns change after the change was made: the old title is out.
	limits.TitlePatterns = []string{"Checkout err*"}
	_, err = newTestAction(t, api, limits).Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.ErrorIs(t, err, action.ErrLimit)
	require.Contains(t, string(g.definitionNow(t)), `"title":"Checkout errors"`)
}

func TestDelete(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	limits := limitsOf()
	limits.AllowDelete = true
	r := newTestAction(t, api, limits)

	params := `{"ruleUid": "rule1", "delete": true}`
	snap := proposed(t, r, params)

	res, err := r.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	require.Equal(t, `Deleted rule rule1, "Checkout latency".`, res.Summary)
	require.Equal(t, 1, g.deleteCount())

	again, err := r.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err, "a rule that is gone already counts as deleted")
	require.Equal(t, "Rule rule1 is deleted.", again.Summary)
	require.Equal(t, 1, g.deleteCount())

	_, err = r.Undo(t.Context(), invocationOf(params, &snap), snap, true)
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestDeleteNotAfterAnEdit(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t)
	limits := limitsOf()
	limits.AllowDelete = true
	r := newTestAction(t, api, limits)

	params := `{"ruleUid": "rule1", "delete": true}`
	snap := proposed(t, r, params)

	g.someoneEdits(t, `{"for": "15m"}`)

	_, err := r.Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorIs(t, err, action.ErrConflict)
	require.Zero(t, g.deleteCount())

	tight := newTestAction(t, api, limitsOf())
	_, err = tight.Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorIs(t, err, action.ErrLimit, "delete was allowed when it was proposed, not now")
}

func TestNewNeedsSecrets(t *testing.T) {
	t.Parallel()

	_, err := New(t.Context(), "x", Config{}, registry.Deps{})
	require.Error(t, err)
}

func TestPluginIsRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), Type)
}

// labelsNow returns the labels of the rule as text.
func labelsNow(t *testing.T, g *fakeGrafana) string {
	t.Helper()

	var def struct {
		Labels json.RawMessage `json:"labels"`
	}

	require.NoError(t, json.Unmarshal(g.definitionNow(t), &def))

	return string(def.Labels)
}

// sortedKeys returns the keys of m in order.
func sortedKeys(m map[string]json.RawMessage) []string {
	return slices.Sorted(maps.Keys(m))
}

// refuse400 answers as Grafana does for a rule it refuses.
func refuse400(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusBadRequest)

	body, _ := json.Marshal(map[string]string{"message": message})
	_, _ = w.Write(body)
}

// refuseConflict answers as Grafana does for a rule with another provenance.
func refuseConflict(w http.ResponseWriter, stored string) {
	w.WriteHeader(http.StatusConflict)

	body, _ := json.Marshal(map[string]string{"message": "cannot update with provided provenance '', needs '" + stored + "'"})
	_, _ = w.Write(body)
}

// textOrEmpty is the JSON text raw holds, or the empty text.
func textOrEmpty(raw json.RawMessage) json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return json.RawMessage(`""`)
	}

	out, _ := json.Marshal(text)

	return out
}

// textOrDefault is the text raw holds, or fallback.
func textOrDefault(raw json.RawMessage, fallback string) string {
	if text, ok := textOf(raw); ok {
		return text
	}

	return fallback
}
