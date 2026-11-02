package datadog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// monitorPathOf is the path of the monitor the tests change.
const monitorPathOf = "/api/v1/monitor/123"

// startMonitor is the monitor as Datadog sends it, with fields that only
// Datadog sets.
const startMonitor = `{
  "id": 123, "org_id": 7, "name": "Checkout latency", "type": "metric alert",
  "query": "avg(last_5m):avg:checkout.latency{env:prod} > 2",
  "message": "Latency is high @slack-ops",
  "tags": ["team:payments", "env:prod"], "priority": 3, "restricted_roles": ["role-1"],
  "options": {"thresholds": {"critical": 2, "warning": 1.5}, "notify_no_data": false, "renotify_interval": 0},
  "creator": {"handle": "ana@example.com"}, "created": "2026-01-01T00:00:00+00:00",
  "modified": "2026-10-01T00:00:00+00:00", "overall_state": "OK", "multi": false
}`

// startDefinition is what a snapshot keeps of startMonitor.
const startDefinition = `{
  "name": "Checkout latency", "type": "metric alert",
  "query": "avg(last_5m):avg:checkout.latency{env:prod} > 2",
  "message": "Latency is high @slack-ops",
  "tags": ["team:payments", "env:prod"], "priority": 3, "restricted_roles": ["role-1"],
  "options": {"thresholds": {"critical": 2, "warning": 1.5}, "notify_no_data": false, "renotify_interval": 0}
}`

// definitionFields are the fields a change sends whole, in order.
var definitionFields = []string{"message", "name", "options", "priority", "query", "restricted_roles", "tags", "type"}

// fakeKeys resolves each key secret to its own fake key.
type fakeKeys struct{}

func (fakeKeys) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	return []byte("fake-" + ref.Key + "\n"), nil
}

// fakeDatadog is a Datadog that keeps one monitor. A PUT sets each field it
// names and moves the modified time on, as Datadog does.
type fakeDatadog struct {
	mu       sync.Mutex
	monitor  map[string]json.RawMessage
	deleted  bool
	edits    int
	puts     []map[string]json.RawMessage
	deletes  int
	validate []json.RawMessage
	// validateStatus and validateBody are the answer of the validate
	// endpoint; zero means 200 with {}.
	validateStatus int
	validateBody   string
	// getStatus, when set, is the status every read answers.
	getStatus int
}

// newFakeDatadog starts the fake API for one test.
func newFakeDatadog(t *testing.T) (*fakehttp.Server, *fakeDatadog) {
	t.Helper()

	f := &fakeDatadog{}
	require.NoError(t, json.Unmarshal([]byte(startMonitor), &f.monitor))

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodGet, monitorPathOf, f.get)
	api.HandleFunc(http.MethodPut, monitorPathOf, f.put)
	api.HandleFunc(http.MethodDelete, monitorPathOf, f.remove)
	api.HandleFunc(http.MethodPost, monitorPathOf+"/validate", f.check)

	return api, f
}

func (f *fakeDatadog) get(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.getStatus != 0 {
		w.WriteHeader(f.getStatus)

		return
	}

	if f.deleted {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors": ["Monitor not found"]}`))

		return
	}

	_ = json.NewEncoder(w).Encode(f.monitor)
}

func (f *fakeDatadog) put(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	f.puts = append(f.puts, body)

	for k, v := range body {
		f.monitor[k] = v
	}

	f.edits++
	f.monitor["modified"] = json.RawMessage(fmt.Sprintf(`"2026-10-02T00:00:%02d+00:00"`, f.edits))

	_ = json.NewEncoder(w).Encode(f.monitor)
}

func (f *fakeDatadog) remove(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deletes++
	f.deleted = true

	_, _ = w.Write([]byte(`{"deleted_monitor_id": 123}`))
}

func (f *fakeDatadog) check(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var body json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.validate = append(f.validate, body)

	if f.validateStatus != 0 {
		w.WriteHeader(f.validateStatus)
	}

	body = json.RawMessage(f.validateBody)
	if f.validateBody == "" {
		body = json.RawMessage(`{}`)
	}

	_, _ = w.Write(body)
}

// someoneEdits changes the monitor in Datadog, as a person would: the
// fields given replace the fields of the monitor, and modified moves on.
func (f *fakeDatadog) someoneEdits(t *testing.T, fields string) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	var set map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(fields), &set))

	for k, v := range set {
		f.monitor[k] = v
	}

	f.edits++
	f.monitor["modified"] = json.RawMessage(fmt.Sprintf(`"2026-10-03T00:00:%02d+00:00"`, f.edits))
}

// definitionNow is the definition of the monitor as a snapshot would keep it.
func (f *fakeDatadog) definitionNow(t *testing.T) json.RawMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	def, err := definition(f.monitor)
	require.NoError(t, err)

	return def
}

func (f *fakeDatadog) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.puts)
}

// putBodies returns what each PUT sent.
func (f *fakeDatadog) putBodies() []map[string]json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.puts)
}

// validated returns what each call of the validate endpoint sent.
func (f *fakeDatadog) validated() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.validate)
}

// deleteCount returns how many times the monitor was deleted.
func (f *fakeDatadog) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.deletes
}

// configure changes how the fake answers, under its lock.
func (f *fakeDatadog) configure(change func(f *fakeDatadog)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// failReads makes every read of the monitor answer with status.
func (f *fakeDatadog) failReads(status int) {
	f.configure(func(f *fakeDatadog) { f.getStatus = status })
}

// limitsOf are the limits the tests use unless a test changes them.
func limitsOf() Limits {
	return Limits{MonitorIDs: []int64{123}, Fields: []string{"options.thresholds", "message", "tags"}}
}

// newTestAction returns the action, talking to api, with the limits given.
func newTestAction(t *testing.T, api *fakehttp.Server, limits Limits) *Monitor {
	t.Helper()

	a, err := New(t.Context(), "datadog-monitor", Config{URL: api.URL(), APIKeySecret: apiRef, AppKeySecret: appRef, Limits: limits}, registry.Deps{Secrets: fakeKeys{}, HTTP: api.Client()})
	require.NoError(t, err)

	m, ok := a.(*Monitor)
	require.True(t, ok)

	return m
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

// raiseCritical is the parameters that raise the critical threshold to 3.
const raiseCritical = `{"monitorId": 123, "set": {"options.thresholds.critical": 3}}`

// proposed makes the snapshot of raiseCritical.
func proposed(t *testing.T, m *Monitor, params string) action.Snapshot {
	t.Helper()

	snap, err := m.Snapshot(t.Context(), json.RawMessage(params))
	require.NoError(t, err)

	return snap
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())

	snap := proposed(t, m, `{"monitorId": 123, "set": {"options.thresholds.critical": 3, "message": "New text", "tags": ["team:payments"]}}`)

	require.Equal(t, "datadog:monitor/123", snap.Target)
	require.Equal(t, "2026-10-01T00:00:00+00:00", snap.Version)
	require.JSONEq(t, startDefinition, string(snap.Before), "only the fields the action can write are kept")

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Equal(t, []action.Change{
		{Path: "message", Before: new(`"Latency is high @slack-ops"`), After: new(`"New text"`)},
		{Path: "options.thresholds.critical", Before: new("2"), After: new("3")},
		{Path: "tags", Before: new(`["team:payments","env:prod"]`), After: new(`["team:payments"]`)},
	}, changes)

	checked := dd.validated()
	require.Len(t, checked, 1, "Datadog checks the new definition before it is proposed")
	require.JSONEq(t, string(snap.After), string(checked[0]))
	require.Zero(t, dd.putCount(), "proposing writes nothing")

	for _, req := range api.Requests() {
		require.Equal(t, "fake-apiKey", req.Header.Get("DD-API-KEY"))
		require.Equal(t, "fake-appKey", req.Header.Get("DD-APPLICATION-KEY"))
	}
}

func TestSnapshotOfADelete(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	limits := limitsOf()
	limits.AllowDelete = true
	m := newTestAction(t, api, limits)

	snap := proposed(t, m, `{"monitorId": 123, "delete": true}`)
	require.True(t, snap.Deletes())
	require.JSONEq(t, startDefinition, string(snap.Before))
	require.Empty(t, dd.validated(), "a delete has no new definition to validate")
}

func TestSnapshotAllowsAMonitorByItsTag(t *testing.T) {
	t.Parallel()

	api, _ := newFakeDatadog(t)

	limits := limitsOf()
	limits.MonitorIDs, limits.MonitorTags = nil, []string{"team:payments"}
	snap := proposed(t, newTestAction(t, api, limits), raiseCritical)
	require.Equal(t, "datadog:monitor/123", snap.Target)

	limits.MonitorTags = []string{"team:search"}
	_, err := newTestAction(t, api, limits).Snapshot(t.Context(), json.RawMessage(raiseCritical))
	require.ErrorIs(t, err, action.ErrLimit)
}

func TestAChangeCannotTakeAMonitorOutOfTheLimits(t *testing.T) {
	t.Parallel()

	byTag := limitsOf()
	byTag.MonitorIDs, byTag.MonitorTags = nil, []string{"team:payments"}

	tests := []struct {
		name    string
		limits  Limits
		params  string
		wantErr string
	}{
		{name: "the allowing tag is dropped", limits: byTag, params: `{"monitorId": 123, "set": {"tags": ["env:prod"]}}`, wantErr: "the change would take monitor 123 out of limits.monitorTags"},
		{name: "the tags are no list", limits: byTag, params: `{"monitorId": 123, "set": {"tags": "team:payments"}}`, wantErr: "not a list of text"},
		{name: "another tag is added", limits: byTag, params: `{"monitorId": 123, "set": {"tags": ["team:payments", "env:prod", "new"]}}`},
		{name: "a monitor allowed by its ID can lose its tags", limits: limitsOf(), params: `{"monitorId": 123, "set": {"tags": []}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, _ := newFakeDatadog(t)

			_, err := newTestAction(t, api, tt.limits).Snapshot(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
		})
	}

	// A snapshot made while the monitor was allowed by its ID is not written
	// once only the tag allows it.
	api, dd := newFakeDatadog(t)
	params := `{"monitorId": 123, "set": {"tags": ["env:prod"]}}`
	snap := proposed(t, newTestAction(t, api, limitsOf()), params)

	_, err := newTestAction(t, api, byTag).Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Zero(t, dd.putCount())
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		params      string
		limits      func(l *Limits)
		change      func(dd *fakeDatadog)
		wantLimit   bool
		wantPerm    bool
		wantErr     string
		wantNoRetry bool
	}{
		{
			name: "a field outside limits.fields", params: `{"monitorId": 123, "set": {"options.notify_no_data": true}}`,
			wantLimit: true, wantErr: `the field "options.notify_no_data" is not one of limits.fields`,
		},
		{
			name: "a whole object where only a field below it is allowed", params: `{"monitorId": 123, "set": {"options": {"thresholds": {"critical": 3}}}}`,
			limits:    func(l *Limits) { l.Fields = []string{"options.thresholds.critical"} },
			wantLimit: true, wantErr: `the field "options" is not one of limits.fields`,
		},
		{
			name: "a sibling of an allowed field", params: `{"monitorId": 123, "set": {"options.thresholds.warning": 1}}`,
			limits:    func(l *Limits) { l.Fields = []string{"options.thresholds.critical"} },
			wantLimit: true, wantErr: "options.thresholds.warning",
		},
		{name: "a monitor that is not allowed", params: `{"monitorId": 999, "set": {"message": "x"}}`, wantLimit: true, wantErr: "monitor 999"},
		{name: "a delete when delete is not allowed", params: `{"monitorId": 123, "delete": true}`, wantLimit: true, wantErr: "limits.allowDelete"},
		{
			name: "a definition Datadog does not accept", params: `{"monitorId": 123, "set": {"message": "x"}}`,
			change: func(dd *fakeDatadog) {
				dd.validateStatus, dd.validateBody = http.StatusBadRequest, `{"errors": ["The value provided for parameter 'query' is invalid"]}`
			},
			wantLimit: true, wantErr: "The value provided for parameter 'query' is invalid",
		},
		{
			name: "a monitor that does not exist", params: raiseCritical,
			change:   func(dd *fakeDatadog) { dd.getStatus = http.StatusNotFound },
			wantPerm: true, wantErr: "Datadog answered 404",
		},
		{
			name: "keys that Datadog refuses", params: raiseCritical,
			change:   func(dd *fakeDatadog) { dd.getStatus = http.StatusForbidden },
			wantPerm: true, wantErr: "Datadog answered 403",
		},
		{
			name: "Datadog failing", params: raiseCritical,
			change: func(dd *fakeDatadog) { dd.getStatus = http.StatusServiceUnavailable }, wantErr: "Datadog answered 503",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, dd := newFakeDatadog(t)

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			if tt.change != nil {
				dd.configure(tt.change)
			}

			_, err := newTestAction(t, api, limits).Snapshot(t.Context(), json.RawMessage(tt.params))
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
			require.Equal(t, tt.wantPerm, errors.Is(err, action.ErrPermanent))
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	api, _ := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())

	tests := []struct {
		name      string
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "a change inside the limits", params: raiseCritical},
		{name: "a field below an allowed one", params: `{"monitorId": 123, "set": {"options.thresholds.warning": 1}}`},
		{name: "a monitor outside the limits", params: `{"monitorId": 5, "set": {"message": "x"}}`, wantLimit: true, wantErr: "monitor 5"},
		{name: "a field outside the limits", params: `{"monitorId": 123, "set": {"query": "x"}}`, wantLimit: true, wantErr: `"query"`},
		{name: "a look-alike of an allowed field", params: `{"monitorId": 123, "set": {"messages": "x"}}`, wantLimit: true, wantErr: `"messages"`},
		{name: "a delete", params: `{"monitorId": 123, "delete": true}`, wantLimit: true, wantErr: "limits.allowDelete"},
		{name: "nothing to change", params: `{"monitorId": 123}`, wantErr: "give set"},
		{name: "a path that does not parse", params: `{"monitorId": 123, "set": {"a..b": 1}}`, wantErr: "invalid field path"},
		{name: "more fields than a person can review", params: `{"monitorId": 123, "set": {` + manyKeys(101) + `}}`, wantErr: "more than 100 fields"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := m.Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
		})
	}

	both := newTestAction(t, api, Limits{MonitorIDs: []int64{123}, Fields: []string{"message"}, AllowDelete: true})
	require.ErrorContains(t, both.Validate(t.Context(), json.RawMessage(`{"monitorId": 123, "set": {"message": "x"}, "delete": true}`)), "either set or delete")
	require.NoError(t, both.Validate(t.Context(), json.RawMessage(`{"monitorId": 123, "delete": true}`)))
}

// manyKeys writes n keys of a JSON object, each a position of the tags.
func manyKeys(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"tags[%d]": "x"`, i)
	}

	return strings.Join(parts, ", ")
}

func TestSpec(t *testing.T) {
	t.Parallel()

	api, _ := newFakeDatadog(t)
	limits := limitsOf()
	limits.MonitorTags = []string{"team:payments"}
	limits.AllowDelete = true

	spec := newTestAction(t, api, limits).Spec()
	require.Equal(t, "datadog-monitor", spec.Name)
	require.Contains(t, spec.Description, "Allowed monitor IDs: 123.")
	require.Contains(t, spec.Description, "team:payments")
	require.Contains(t, spec.Description, "options.thresholds, message, tags")
	require.Contains(t, spec.Description, "cannot be undone in Sidekick")
	require.True(t, json.Valid(spec.ParamsSchema))

	without := newTestAction(t, api, limitsOf()).Spec()
	require.NotContains(t, without.Description, "deletes it")
}

func TestExecute(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())

	params := `{"monitorId": 123, "set": {"options.thresholds.critical": 3, "message": "New text"}}`
	snap := proposed(t, m, params)

	res, err := m.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	require.Equal(t, `Changed 2 fields of monitor 123, "Checkout latency".`, res.Summary)
	require.Empty(t, res.Links, "a url replaces the site, so the address of the app is not known")

	require.Equal(t, 1, dd.putCount())
	require.Equal(t, definitionFields, sortedKeys(dd.putBodies()[0]), "the whole definition is sent, so a PUT does not depend on what Datadog does with the fields it is not sent")
	require.JSONEq(t, string(snap.After), string(dd.definitionNow(t)))
}

// TestExecuteSendsEveryFieldAsItIs checks that a field the change does not
// touch goes back with the value it has, including restricted_roles.
func TestExecuteSendsEveryFieldAsItIs(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())
	snap := proposed(t, m, raiseCritical)

	_, err := m.Execute(t.Context(), invocationOf(raiseCritical, &snap))
	require.NoError(t, err)

	var want map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(startDefinition), &want))

	body := dd.putBodies()[0]

	for _, key := range definitionFields {
		if key == "options" {
			continue
		}

		require.JSONEq(t, string(want[key]), string(body[key]), key)
	}

	require.JSONEq(t, `["role-1"]`, string(body["restricted_roles"]))
}

// TestAMutedMonitorStaysMuted checks that options.silenced, which a monitor
// keeps when it is muted, goes back as it was through a change and an Undo.
func TestAMutedMonitorStaysMuted(t *testing.T) {
	t.Parallel()

	const muted = `{"thresholds": {"critical": 2, "warning": 1.5}, "notify_no_data": false, "renotify_interval": 0, "silenced": {"env:prod": 1893456000}}`

	api, dd := newFakeDatadog(t)
	dd.someoneEdits(t, `{"options": `+muted+`}`)

	m := newTestAction(t, api, limitsOf())
	snap := proposed(t, m, raiseCritical)

	_, err := m.Execute(t.Context(), invocationOf(raiseCritical, &snap))
	require.NoError(t, err)

	silenced := func() string {
		var def struct {
			Options struct {
				Silenced json.RawMessage `json:"silenced"`
			} `json:"options"`
		}

		require.NoError(t, json.Unmarshal(dd.definitionNow(t), &def))

		return string(def.Options.Silenced)
	}

	require.JSONEq(t, `{"env:prod": 1893456000}`, silenced(), "after the change")

	_, err = m.Undo(t.Context(), invocationOf(raiseCritical, &snap), snap, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"env:prod": 1893456000}`, silenced(), "after the undo")
}

func TestExecuteWritesWhatTheApproverRead(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())
	snap := proposed(t, m, raiseCritical)

	// Different parameters, the same snapshot: the snapshot is what runs.
	_, err := m.Execute(t.Context(), invocationOf(`{"monitorId": 123, "set": {"options.thresholds.critical": 50}}`, &snap))
	require.NoError(t, err)

	var options struct {
		Thresholds struct {
			Critical float64 `json:"critical"`
		} `json:"thresholds"`
	}

	require.NoError(t, json.Unmarshal(dd.putBodies()[0]["options"], &options))
	require.InDelta(t, 3, options.Thresholds.Critical, 0)
}

func TestExecuteRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		prepare   func(t *testing.T, dd *fakeDatadog)
		limits    func(l *Limits)
		snapshot  func(snap *action.Snapshot)
		wantErr   error
		wantMsg   string
		wantWrite bool
	}{
		{
			name: "someone edited the monitor since the snapshot", wantErr: action.ErrConflict,
			prepare: func(t *testing.T, dd *fakeDatadog) {
				dd.someoneEdits(t, `{"message": "Edited by a person"}`)
			},
		},
		{
			name: "the same fields were edited to other values", wantErr: action.ErrConflict,
			prepare: func(t *testing.T, dd *fakeDatadog) {
				dd.someoneEdits(t, `{"options": {"thresholds": {"critical": 9, "warning": 1.5}, "notify_no_data": false, "renotify_interval": 0}}`)
			},
		},
		{
			name: "limits tightened since the proposal", wantErr: action.ErrLimit,
			limits: func(l *Limits) { l.Fields = []string{"message"} },
		},
		{
			name: "a monitor taken out of the limits", wantErr: action.ErrLimit,
			limits: func(l *Limits) { l.MonitorIDs = []int64{5} },
		},
		{
			name: "a snapshot of another monitor", wantErr: action.ErrPermanent,
			snapshot: func(snap *action.Snapshot) { snap.Target = "datadog:monitor/9" },
		},
		{
			name: "the monitor is gone", wantErr: action.ErrPermanent,
			prepare: func(t *testing.T, dd *fakeDatadog) { dd.failReads(http.StatusNotFound) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, dd := newFakeDatadog(t)
			snap := proposed(t, newTestAction(t, api, limitsOf()), raiseCritical)

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			if tt.snapshot != nil {
				tt.snapshot(&snap)
			}

			if tt.prepare != nil {
				tt.prepare(t, dd)
			}

			_, err := newTestAction(t, api, limits).Execute(t.Context(), invocationOf(raiseCritical, &snap))
			require.ErrorIs(t, err, tt.wantErr)
			require.Zero(t, dd.putCount(), "nothing was written")
		})
	}

	api, _ := newFakeDatadog(t)
	_, err := newTestAction(t, api, limitsOf()).Execute(t.Context(), invocationOf(raiseCritical, nil))
	require.ErrorIs(t, err, action.ErrPermanent, "a proposal with no snapshot")
}

// TestExecuteTwiceHasTheEffectOfOne is a worker that died after the write and
// before the result was saved: the job runs again, finds the monitor as the
// snapshot says, and writes nothing more.
func TestExecuteTwiceHasTheEffectOfOne(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())
	snap := proposed(t, m, raiseCritical)

	first, err := m.Execute(t.Context(), invocationOf(raiseCritical, &snap))
	require.NoError(t, err)

	second, err := m.Execute(t.Context(), invocationOf(raiseCritical, &snap))
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Equal(t, 1, dd.putCount())
}

func TestUndo(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())

	params := `{"monitorId": 123, "set": {"options.thresholds.critical": 3, "message": "New text"}}`
	snap := proposed(t, m, params)

	_, err := m.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)

	// Someone changes a field the action did not touch.
	dd.someoneEdits(t, `{"tags": ["team:payments", "team:sre"]}`)

	res, err := m.Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, `Restored 2 fields of monitor 123, "Checkout latency".`, res.Summary)

	want := strings.Replace(startDefinition, `"tags": ["team:payments", "env:prod"]`, `"tags": ["team:payments", "team:sre"]`, 1)
	require.JSONEq(t, want, string(dd.definitionNow(t)), "the changed fields are back, the other edit stays")

	puts := dd.putBodies()
	last := puts[len(puts)-1]
	require.Equal(t, definitionFields, sortedKeys(last), "the whole definition is sent")
}

func TestUndoAfterSomeoneChangedAFieldTheActionChanged(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())
	snap := proposed(t, m, raiseCritical)

	_, err := m.Execute(t.Context(), invocationOf(raiseCritical, &snap))
	require.NoError(t, err)

	dd.someoneEdits(t, `{"options": {"thresholds": {"critical": 9, "warning": 1.5}, "notify_no_data": false, "renotify_interval": 0}}`)
	writes := dd.putCount()

	_, err = m.Undo(t.Context(), invocationOf(raiseCritical, &snap), snap, false)

	var conflict *action.ConflictError

	require.ErrorAs(t, err, &conflict)
	require.ErrorIs(t, err, action.ErrConflict)
	require.JSONEq(t, string(dd.definitionNow(t)), string(conflict.Live), "the error carries the monitor as it is")
	require.Equal(t, writes, dd.putCount(), "nothing was written")

	_, err = m.Undo(t.Context(), invocationOf(raiseCritical, &snap), snap, true)
	require.NoError(t, err)
	require.JSONEq(t, startDefinition, string(dd.definitionNow(t)))
}

func TestUndoTwiceHasTheEffectOfOne(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())
	snap := proposed(t, m, raiseCritical)

	_, err := m.Execute(t.Context(), invocationOf(raiseCritical, &snap))
	require.NoError(t, err)

	_, err = m.Undo(t.Context(), invocationOf(raiseCritical, &snap), snap, false)
	require.NoError(t, err)

	writes := dd.putCount()

	_, err = m.Undo(t.Context(), invocationOf(raiseCritical, &snap), snap, false)
	require.NoError(t, err, "the monitor is restored already")
	require.Equal(t, writes, dd.putCount())
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	m := newTestAction(t, api, limitsOf())
	snap := proposed(t, m, raiseCritical)

	_, err := m.Execute(t.Context(), invocationOf(raiseCritical, &snap))
	require.NoError(t, err)

	tight := newTestAction(t, api, Limits{MonitorIDs: []int64{123}, Fields: []string{"message"}})
	_, err = tight.Undo(t.Context(), invocationOf(`{"monitorId": 123, "set": {"message": "x"}}`, &snap), snap, false)
	require.ErrorIs(t, err, action.ErrLimit, "limits tightened since the change")

	gone := action.Snapshot{Target: snap.Target, Before: snap.Before}
	_, err = m.Undo(t.Context(), invocationOf(raiseCritical, &gone), gone, false)
	require.ErrorIs(t, err, action.ErrPermanent, "a delete cannot be undone")

	dd.failReads(http.StatusInternalServerError)

	_, err = m.Undo(t.Context(), invocationOf(raiseCritical, &snap), snap, false)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent, "a server error is tried again")
}

func TestDelete(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	limits := limitsOf()
	limits.AllowDelete = true
	m := newTestAction(t, api, limits)

	params := `{"monitorId": 123, "delete": true}`
	snap := proposed(t, m, params)

	res, err := m.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	require.Equal(t, `Deleted monitor 123, "Checkout latency".`, res.Summary)
	require.Equal(t, 1, dd.deleteCount())

	again, err := m.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err, "a monitor that is gone already counts as deleted")
	require.Equal(t, "Monitor 123 is deleted.", again.Summary)
	require.Equal(t, 1, dd.deleteCount())

	_, err = m.Undo(t.Context(), invocationOf(params, &snap), snap, true)
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestDeleteNotAfterAnEdit(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDatadog(t)
	limits := limitsOf()
	limits.AllowDelete = true
	m := newTestAction(t, api, limits)

	params := `{"monitorId": 123, "delete": true}`
	snap := proposed(t, m, params)

	dd.someoneEdits(t, `{"message": "Edited by a person"}`)

	_, err := m.Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorIs(t, err, action.ErrConflict)
	require.Zero(t, dd.deleteCount())

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

// sortedKeys returns the keys of m in order.
func sortedKeys(m map[string]json.RawMessage) []string {
	return slices.Sorted(maps.Keys(m))
}
