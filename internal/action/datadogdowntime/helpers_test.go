package datadogdowntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testNow is the time of the fake Datadog and of the action in the tests.
var testNow = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// proposalID is the proposal the tests run, and otherProposalID another one.
const (
	proposalID      = "0194f2c8-7d5e-7a41-9a3b-1c2d3e4f5a6b"
	otherProposalID = "0194f2c8-7d5e-7a41-9a3b-ffffffffffff"
)

// The monitors of the fake. Monitor 123 is listed in the limits, 456 has the
// allowed tag, 789 has neither, and 404 does not exist.
const (
	listedMonitor   = 123
	taggedMonitor   = 456
	unlistedMonitor = 789
	missingMonitor  = 404
)

// maxFakeDowntimes is how many downtimes the fake can make. Its DELETE
// routes are registered up front, because fakehttp matches paths exactly.
const maxFakeDowntimes = 9

// Scopes of the limits in the tests.
const (
	prodScope    = "env:prod"
	stagingScope = "env:staging AND service:checkout"
)

// fakeKeys resolves each secret to its own fake value.
type fakeKeys struct{}

func (fakeKeys) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	return []byte("fake-" + ref.Key + "\n"), nil
}

// fakeDowntime is a downtime of the fake Datadog.
type fakeDowntime struct {
	ID          string
	Status      string
	Scope       string
	Message     string
	MonitorID   int64
	MonitorTags []string
	Start       time.Time
	End         time.Time
	Recurring   bool
}

// fakeMonitor is a monitor of the fake Datadog.
type fakeMonitor struct {
	ID    int64
	Name  string
	Type  string
	State string
	Tags  []string
}

// fakeDD is a Datadog that keeps downtimes, as the v2 downtime API does: a
// POST makes an active downtime that starts now, a DELETE cancels one and
// keeps it in the list, and a list answers every state, a page at a time.
type fakeDD struct {
	mu sync.Mutex

	now       time.Time
	downtimes []*fakeDowntime
	monitors  []fakeMonitor
	nextID    int

	// pageCap is the most downtimes the fake lists in one page, whatever
	// page[limit] asks for. Zero means no cap.
	pageCap int
	// mangle changes the message the way the fake stores it. Nil keeps it.
	mangle func(string) string

	// posts are the bodies of the POST calls, as sent.
	posts []json.RawMessage
	// deletes are the IDs of the DELETE calls.
	deletes []string
	// listCalls counts the calls of GET /api/v2/downtime.
	listCalls int
	// monitorTags are the monitor_tags parameters of the monitor list calls.
	monitorTags []string
	// listStatus, postStatus, deleteStatus and monitorsStatus, when set, are
	// the status of the answers of those calls.
	listStatus, postStatus, deleteStatus, monitorsStatus int
}

// newFakeDD starts the fake API for one test.
func newFakeDD(t *testing.T) (*fakehttp.Server, *fakeDD) {
	t.Helper()

	api := fakehttp.New(t)
	dd := &fakeDD{
		now: testNow,
		monitors: []fakeMonitor{
			{ID: listedMonitor, Name: "Checkout latency", Type: "metric alert", State: "OK", Tags: []string{"service:checkout"}},
			{ID: taggedMonitor, Name: "Payments errors", Type: "query alert", State: "Alert", Tags: []string{"team:payments", "env:prod"}},
			{ID: unlistedMonitor, Name: "Search errors", Type: "query alert", State: "OK", Tags: []string{"team:search"}},
		},
	}

	api.HandleFunc(http.MethodGet, "/api/v2/downtime", dd.list)
	api.HandleFunc(http.MethodPost, "/api/v2/downtime", dd.post)
	api.HandleFunc(http.MethodGet, "/api/v1/monitor", dd.listMonitors)

	for _, m := range dd.monitors {
		api.HandleFunc(http.MethodGet, "/api/v1/monitor/"+strconv.FormatInt(m.ID, 10), func(w http.ResponseWriter, _ *http.Request) { dd.getMonitor(w, m.ID) })
	}

	api.HandleFunc(http.MethodGet, "/api/v1/monitor/"+strconv.Itoa(missingMonitor), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string][]string{"errors": {"Monitor not found"}})
	})

	for i := 1; i <= maxFakeDowntimes; i++ {
		id := fakeID(i)
		api.HandleFunc(http.MethodDelete, "/api/v2/downtime/"+id, func(w http.ResponseWriter, _ *http.Request) { dd.cancel(w, id) })
	}

	return api, dd
}

// fakeID is the ID of the n-th downtime.
func fakeID(n int) string {
	return fmt.Sprintf("dt-%d", n)
}

// add puts a downtime in the fake, as if someone made it, and returns it.
func (dd *fakeDD) add(d fakeDowntime) *fakeDowntime {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	dd.nextID++
	d.ID = fakeID(dd.nextID)

	if d.Status == "" {
		d.Status = "active"
	}

	dd.downtimes = append(dd.downtimes, &d)

	return &d
}

// get returns a copy of the downtime with id.
func (dd *fakeDD) get(id string) fakeDowntime {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	for _, d := range dd.downtimes {
		if d.ID == id {
			return *d
		}
	}

	return fakeDowntime{}
}

// edit changes the downtime with id, as a person does in the Datadog UI.
func (dd *fakeDD) edit(id string, change func(*fakeDowntime)) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	for _, d := range dd.downtimes {
		if d.ID == id {
			change(d)
		}
	}
}

// clear removes every downtime, as Datadog does some time after a downtime
// was canceled.
func (dd *fakeDD) clear() {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	dd.downtimes = nil
}

// failWith makes the calls answer with the statuses, where zero leaves a
// call as it is.
func (dd *fakeDD) failWith(list, post, del, monitors int) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	dd.listStatus, dd.postStatus, dd.deleteStatus, dd.monitorsStatus = list, post, del, monitors
}

// advance moves the fake's time on.
func (dd *fakeDD) advance(d time.Duration) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	dd.now = dd.now.Add(d)
}

// clock returns the fake's time, for the action to use as its own.
func (dd *fakeDD) clock() time.Time {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	return dd.now
}

// postedBodies returns the bodies of the POST calls.
func (dd *fakeDD) postedBodies() []json.RawMessage {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	return append([]json.RawMessage(nil), dd.posts...)
}

// canceled is the IDs that were deleted.
func (dd *fakeDD) canceled() []string {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	return append([]string(nil), dd.deletes...)
}

// lists is how many times the list was read.
func (dd *fakeDD) lists() int {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	return dd.listCalls
}

func (dd *fakeDD) list(w http.ResponseWriter, r *http.Request) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	dd.listCalls++

	if dd.listStatus != 0 {
		writeJSON(w, dd.listStatus, map[string][]string{"errors": {"list failed"}})

		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("page[limit]"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("page[offset]"))

	if dd.pageCap > 0 {
		limit = min(limit, dd.pageCap)
	}

	data := []map[string]any{}

	for i := offset; i < min(offset+limit, len(dd.downtimes)); i++ {
		data = append(data, dd.wire(dd.downtimes[i]))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data, "meta": map[string]any{"page": map[string]int{"total_filtered_count": len(dd.downtimes)}}})
}

func (dd *fakeDD) post(w http.ResponseWriter, r *http.Request) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string][]string{"errors": {"bad body"}})

		return
	}

	dd.posts = append(dd.posts, body)

	if dd.postStatus != 0 {
		writeJSON(w, dd.postStatus, map[string][]string{"errors": {"post failed"}})

		return
	}

	var p struct {
		Data struct {
			Type       string `json:"type"`
			Attributes struct {
				Message           string `json:"message"`
				Scope             string `json:"scope"`
				MonitorIdentifier struct {
					MonitorID   int64    `json:"monitor_id"`
					MonitorTags []string `json:"monitor_tags"`
				} `json:"monitor_identifier"`
				Schedule struct {
					End string `json:"end"`
				} `json:"schedule"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if json.Unmarshal(body, &p) != nil || p.Data.Type != "downtime" {
		writeJSON(w, http.StatusBadRequest, map[string][]string{"errors": {"bad downtime"}})

		return
	}

	end, err := time.Parse(endLayout, p.Data.Attributes.Schedule.End)
	if err != nil || !end.After(dd.now) {
		writeJSON(w, http.StatusBadRequest, map[string][]string{"errors": {"the end must be in the future, with a UTC offset of zero"}})

		return
	}

	message := p.Data.Attributes.Message
	if dd.mangle != nil {
		message = dd.mangle(message)
	}

	dd.nextID++
	id := fakeID(dd.nextID)
	dd.downtimes = append(dd.downtimes, &fakeDowntime{
		ID: id, Status: "active", Scope: p.Data.Attributes.Scope, Message: message,
		MonitorID: p.Data.Attributes.MonitorIdentifier.MonitorID, MonitorTags: p.Data.Attributes.MonitorIdentifier.MonitorTags,
		Start: dd.now, End: end,
	})

	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": id, "type": "downtime"}})
}

// cancel cancels the downtime with id, as DELETE /api/v2/downtime/{id} does.
func (dd *fakeDD) cancel(w http.ResponseWriter, id string) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	dd.deletes = append(dd.deletes, id)

	if dd.deleteStatus != 0 {
		writeJSON(w, dd.deleteStatus, map[string][]string{"errors": {"delete failed"}})

		return
	}

	for _, d := range dd.downtimes {
		if d.ID == id {
			d.Status = "canceled"
			w.WriteHeader(http.StatusNoContent)

			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string][]string{"errors": {"Downtime not found"}})
}

// getMonitor answers GET /api/v1/monitor/{id} with the monitor as it is now.
func (dd *fakeDD) getMonitor(w http.ResponseWriter, id int64) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	for _, m := range dd.monitors {
		if m.ID == id {
			writeJSON(w, http.StatusOK, wireMonitor{ID: m.ID, Name: m.Name, Type: m.Type, OverallState: m.State, Tags: m.Tags})

			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string][]string{"errors": {"Monitor not found"}})
}

func (dd *fakeDD) listMonitors(w http.ResponseWriter, r *http.Request) {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	tag := r.URL.Query().Get("monitor_tags")
	dd.monitorTags = append(dd.monitorTags, tag)

	if dd.monitorsStatus != 0 {
		writeJSON(w, dd.monitorsStatus, map[string][]string{"errors": {"monitors failed"}})

		return
	}

	out := []wireMonitor{}

	for _, m := range dd.monitors {
		for _, have := range m.Tags {
			if have == tag {
				out = append(out, wireMonitor{ID: m.ID, Name: m.Name, Type: m.Type, OverallState: m.State, Tags: m.Tags})

				break
			}
		}
	}

	if size, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && size < len(out) {
		out = out[:size]
	}

	writeJSON(w, http.StatusOK, out)
}

// wire writes a downtime as the JSON of the list does: the shape of the
// answers recorded in the Datadog API client's tests.
func (dd *fakeDD) wire(d *fakeDowntime) map[string]any {
	identifier := map[string]any{}
	if d.MonitorID != 0 {
		identifier["monitor_id"] = d.MonitorID
	} else {
		identifier["monitor_tags"] = d.MonitorTags
	}

	schedule := map[string]any{"start": d.Start.Format("2006-01-02T15:04:05.000000+00:00"), "end": nil}
	if !d.End.IsZero() {
		schedule["end"] = d.End.Format("2006-01-02T15:04:05.000000+00:00")
	}

	if d.Recurring {
		schedule = map[string]any{"recurrences": []map[string]string{{"duration": "1h", "rrule": "FREQ=DAILY;INTERVAL=1", "start": "2026-11-04T10:00:00"}}}
	}

	return map[string]any{
		"id": d.ID, "type": "downtime",
		"attributes": map[string]any{
			"status": d.Status, "scope": d.Scope, "message": d.Message, "monitor_identifier": identifier, "schedule": schedule,
			"created": d.Start.Format("2006-01-02T15:04:05.000000+00:00"), "canceled": nil,
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

// testConfig is a valid config for the action at url.
func testConfig(url string) Config {
	return Config{
		URL:          url,
		APIKeySecret: registry.SecretRef{Name: "datadog-downtime", Key: "apiKey"},
		AppKeySecret: registry.SecretRef{Name: "datadog-downtime", Key: "appKey"},
		Limits: Limits{
			MonitorIDs:  []int64{listedMonitor},
			MonitorTags: []string{"team:payments"},
			Scopes:      []string{prodScope, stagingScope},
		},
	}
}

// newTestDowntimer builds the action from cfg against the fake, with the
// fake's clock.
func newTestDowntimer(t *testing.T, api *fakehttp.Server, dd *fakeDD, cfg Config) *downtimer {
	t.Helper()

	require.NoError(t, cfg.Validate())

	built, err := New(t.Context(), "mute", cfg, deps(api))
	require.NoError(t, err)

	d, ok := built.(*downtimer)
	require.True(t, ok)

	d.now = dd.clock

	return d
}

// deps are the dependencies the tests give the action.
func deps(api *fakehttp.Server) registry.Deps {
	return registry.Deps{HTTP: api.Client(), Secrets: fakeKeys{}}
}

// paramsJSON writes parameters for the tests.
func paramsJSON(t *testing.T, p params) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	return raw
}

// monitorParams are parameters for the listed monitor.
func monitorParams(t *testing.T) json.RawMessage {
	t.Helper()

	return paramsJSON(t, params{MonitorID: listedMonitor, Scope: prodScope, Duration: "1h", Message: "Planned database migration"})
}

// tagParams are parameters for the monitors with the allowed tag.
func tagParams(t *testing.T) json.RawMessage {
	t.Helper()

	return paramsJSON(t, params{MonitorTag: "team:payments", Scope: stagingScope, Duration: "30m", Message: "Payments deploy"})
}

// invocation is the Invocation Execute gets for the proposal with the
// parameters and snapshot.
func invocation(raw json.RawMessage, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: proposalID, IdempotencyKey: action.IdempotencyKey(proposalID), Params: raw, Snapshot: &snap}
}

// snapshotAndRun takes the snapshot of the parameters and executes it.
func snapshotAndRun(t *testing.T, d *downtimer, raw json.RawMessage) (action.Snapshot, action.Result) {
	t.Helper()

	snap, err := d.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	res, err := d.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)

	return snap, res
}

// undoInvocation is the Invocation Undo gets for the proposal that made the
// downtime.
func undoInvocation(raw json.RawMessage, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: otherProposalID, UndoOf: proposalID, Params: raw, Snapshot: &snap}
}

// messageWithMarker is a downtime message as Execute writes it.
func messageWithMarker(text, id string) string {
	return strings.TrimSpace(text) + "\n" + mute.Marker(id)
}

// bodyAttributes returns the attributes of a posted body.
func bodyAttributes(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()

	var body struct {
		Data struct {
			Type       string                     `json:"type"`
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}

	require.NoError(t, json.Unmarshal(raw, &body))
	require.Equal(t, "downtime", body.Data.Type)

	return body.Data.Attributes
}

// requestPaths returns the method and path of every request the server got.
func requestPaths(api *fakehttp.Server) []string {
	var paths []string
	for _, r := range api.Requests() {
		paths = append(paths, r.Method+" "+r.Path)
	}

	return paths
}
