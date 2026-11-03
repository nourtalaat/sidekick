package alertmanagersilence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// testNow is the time of the fake Alertmanager and of the action in the
// tests.
var testNow = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// proposalID is the proposal the tests run, and otherProposalID another one.
const (
	proposalID      = "0194f2c8-7d5e-7a41-9a3b-1c2d3e4f5a6b"
	otherProposalID = "0194f2c8-7d5e-7a41-9a3b-ffffffffffff"
)

// maxFakeSilences is how many silences the fake can make. Its DELETE
// routes are registered up front, because fakehttp matches paths exactly.
const maxFakeSilences = 9

// scopes are the scopes the tests allow.
var scopes = []mute.Scope{
	{Labels: map[string][]string{"alertname": {"HighLatency", "HighErrorRate"}, "service": {"checkout-*"}}},
	{Labels: map[string][]string{"team": {"payments"}}},
}

// fakeToken resolves each secret to its own fake value.
type fakeToken struct{}

func (fakeToken) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	return []byte("fake-" + ref.Key + "\n"), nil
}

// fakeSilence is a silence of the fake Alertmanager.
type fakeSilence struct {
	ID        string
	Matchers  []mute.Matcher
	StartsAt  time.Time
	EndsAt    time.Time
	CreatedBy string
	Comment   string
}

// fakeAlert is an alert of the fake Alertmanager.
type fakeAlert struct {
	Labels map[string]string
	State  string
}

// fakeAM is an Alertmanager that keeps silences, as the API v2 of
// Alertmanager does: a POST makes a silence and moves a start in the past to
// now, a DELETE ends one without removing it, and a list answers every
// state.
type fakeAM struct {
	mu sync.Mutex

	now      time.Time
	silences []*fakeSilence
	alerts   []fakeAlert
	nextID   int

	// posts are the bodies of the POST calls, as sent.
	posts []json.RawMessage
	// deletes are the IDs of the DELETE calls.
	deletes []string
	// alertFilters are the filter parameters of the GET /alerts calls.
	alertFilters [][]string
	// listStatus, postStatus and alertsStatus, when set, are the status of
	// the answers of the three calls.
	listStatus, postStatus, alertsStatus int
	// deleteStatus, when set, is the status of the DELETE answers.
	deleteStatus int
}

// newFakeAM starts the fake API for one test, under prefix (for example
// "" or "/alertmanager").
func newFakeAM(t *testing.T, prefix string) (*fakehttp.Server, *fakeAM) {
	t.Helper()

	api := fakehttp.New(t)
	am := &fakeAM{now: testNow}

	api.HandleFunc(http.MethodGet, prefix+"/api/v2/silences", am.list)
	api.HandleFunc(http.MethodPost, prefix+"/api/v2/silences", am.post)
	api.HandleFunc(http.MethodGet, prefix+"/api/v2/alerts", am.listAlerts)

	for i := 1; i <= maxFakeSilences; i++ {
		id := fakeID(i)
		api.HandleFunc(http.MethodDelete, prefix+"/api/v2/silence/"+id, func(w http.ResponseWriter, _ *http.Request) { am.expire(w, id) })
	}

	return api, am
}

// fakeID is the ID of the n-th silence.
func fakeID(n int) string {
	return fmt.Sprintf("sil-%d", n)
}

// add puts a silence in the fake, as if someone made it, and returns it.
func (am *fakeAM) add(s fakeSilence) *fakeSilence {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.nextID++
	s.ID = fakeID(am.nextID)
	am.silences = append(am.silences, &s)

	return &s
}

// get returns a copy of the silence with id.
func (am *fakeAM) get(id string) fakeSilence {
	am.mu.Lock()
	defer am.mu.Unlock()

	for _, s := range am.silences {
		if s.ID == id {
			return *s
		}
	}

	return fakeSilence{}
}

// edit changes the silence with id, as a person does in the Alertmanager UI.
func (am *fakeAM) edit(id string, change func(*fakeSilence)) {
	am.mu.Lock()
	defer am.mu.Unlock()

	for _, s := range am.silences {
		if s.ID == id {
			change(s)
		}
	}
}

// setAlerts sets the alerts the fake lists.
func (am *fakeAM) setAlerts(alerts ...fakeAlert) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.alerts = alerts
}

// failWith makes the three calls answer with the statuses, where zero
// leaves a call as it is.
func (am *fakeAM) failWith(list, post, alerts int) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.listStatus, am.postStatus, am.alertsStatus = list, post, alerts
}

// failDeleteWith makes the DELETE calls answer with status.
func (am *fakeAM) failDeleteWith(status int) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.deleteStatus = status
}

// filters returns the filter parameters of the GET /alerts calls.
func (am *fakeAM) filters() [][]string {
	am.mu.Lock()
	defer am.mu.Unlock()

	return append([][]string(nil), am.alertFilters...)
}

// postedBodies returns the bodies of the POST calls.
func (am *fakeAM) postedBodies() []json.RawMessage {
	am.mu.Lock()
	defer am.mu.Unlock()

	return append([]json.RawMessage(nil), am.posts...)
}

// state is the state of a silence at the fake's time.
func (am *fakeAM) state(s *fakeSilence) string {
	switch {
	case am.now.Before(s.StartsAt):
		return "pending"
	case !am.now.Before(s.EndsAt):
		return "expired"
	default:
		return "active"
	}
}

// stateOf is the state of the silence with id at the fake's time.
func (am *fakeAM) stateOf(id string) string {
	am.mu.Lock()
	defer am.mu.Unlock()

	for _, s := range am.silences {
		if s.ID == id {
			return am.state(s)
		}
	}

	return ""
}

// advance moves the fake's time on.
func (am *fakeAM) advance(d time.Duration) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.now = am.now.Add(d)
}

// clock returns the fake's time, for the action to use as its own.
func (am *fakeAM) clock() time.Time {
	am.mu.Lock()
	defer am.mu.Unlock()

	return am.now
}

// postCount is how many silences were posted.
func (am *fakeAM) postCount() int {
	am.mu.Lock()
	defer am.mu.Unlock()

	return len(am.posts)
}

// deleted is the IDs that were deleted.
func (am *fakeAM) deleted() []string {
	am.mu.Lock()
	defer am.mu.Unlock()

	return append([]string(nil), am.deletes...)
}

func (am *fakeAM) list(w http.ResponseWriter, _ *http.Request) {
	am.mu.Lock()
	defer am.mu.Unlock()

	if am.listStatus != 0 {
		http.Error(w, `"list failed"`, am.listStatus)

		return
	}

	out := make([]map[string]any, 0, len(am.silences))

	for _, s := range am.silences {
		matchers := make([]map[string]any, 0, len(s.Matchers))
		for _, m := range s.Matchers {
			matchers = append(matchers, map[string]any{"name": m.Name, "value": m.Value, "isRegex": m.IsRegex, "isEqual": m.IsEqual})
		}

		out = append(out, map[string]any{
			"id": s.ID, "status": map[string]string{"state": am.state(s)}, "matchers": matchers,
			"startsAt": s.StartsAt, "endsAt": s.EndsAt, "createdBy": s.CreatedBy, "comment": s.Comment, "updatedAt": am.now,
		})
	}

	writeJSON(w, http.StatusOK, out)
}

func (am *fakeAM) post(w http.ResponseWriter, r *http.Request) {
	am.mu.Lock()
	defer am.mu.Unlock()

	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `"bad body"`, http.StatusBadRequest)

		return
	}

	am.posts = append(am.posts, body)

	if am.postStatus != 0 {
		http.Error(w, `"post failed"`, am.postStatus)

		return
	}

	var p struct {
		Matchers  []mute.Matcher `json:"matchers"`
		StartsAt  time.Time      `json:"startsAt"`
		EndsAt    time.Time      `json:"endsAt"`
		CreatedBy string         `json:"createdBy"`
		Comment   string         `json:"comment"`
	}

	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, `"bad silence"`, http.StatusBadRequest)

		return
	}

	switch {
	case !p.StartsAt.Before(p.EndsAt):
		http.Error(w, `"Failed to create silence: start time must be before end time"`, http.StatusBadRequest)

		return
	case p.EndsAt.Before(am.now):
		http.Error(w, `"Failed to create silence: end time can't be in the past"`, http.StatusBadRequest)

		return
	}

	if p.StartsAt.Before(am.now) {
		p.StartsAt = am.now
	}

	am.nextID++
	id := fakeID(am.nextID)
	am.silences = append(am.silences, &fakeSilence{ID: id, Matchers: p.Matchers, StartsAt: p.StartsAt, EndsAt: p.EndsAt, CreatedBy: p.CreatedBy, Comment: p.Comment})

	writeJSON(w, http.StatusOK, map[string]string{"silenceID": id})
}

func (am *fakeAM) listAlerts(w http.ResponseWriter, r *http.Request) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.alertFilters = append(am.alertFilters, r.URL.Query()["filter"])

	if am.alertsStatus != 0 {
		http.Error(w, `"alerts failed"`, am.alertsStatus)

		return
	}

	out := make([]map[string]any, 0, len(am.alerts))
	for _, a := range am.alerts {
		out = append(out, map[string]any{"labels": a.Labels, "status": map[string]string{"state": a.State}, "fingerprint": "f"})
	}

	writeJSON(w, http.StatusOK, out)
}

// expire ends the silence with id, as DELETE /api/v2/silence/{id} does.
func (am *fakeAM) expire(w http.ResponseWriter, id string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.deletes = append(am.deletes, id)

	if am.deleteStatus != 0 {
		http.Error(w, `"delete failed"`, am.deleteStatus)

		return
	}

	for _, s := range am.silences {
		if s.ID != id {
			continue
		}

		switch am.state(s) {
		case "active":
			s.EndsAt = am.now
		case "pending":
			s.StartsAt, s.EndsAt = am.now, am.now
		}

		w.WriteHeader(http.StatusOK)

		return
	}

	http.Error(w, "", http.StatusNotFound)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

// testConfig is a valid config for the action at url.
func testConfig(url string) Config {
	return Config{URL: url, Limits: Limits{Scopes: scopes}}
}

// newTestSilencer builds the action from cfg against the fake, with the
// fake's clock.
func newTestSilencer(t *testing.T, api *fakehttp.Server, am *fakeAM, cfg Config) *silencer {
	t.Helper()

	require.NoError(t, cfg.Validate())

	built, err := New(t.Context(), "mute", cfg, deps(api))
	require.NoError(t, err)

	s, ok := built.(*silencer)
	require.True(t, ok)

	s.now = am.clock

	return s
}

// deps are the dependencies the tests give the action.
func deps(api *fakehttp.Server) registry.Deps {
	return registry.Deps{HTTP: api.Client(), Secrets: fakeToken{}}
}

// paramsJSON writes parameters for the tests.
func paramsJSON(t *testing.T, p params) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	return raw
}

// eq is an equality matcher.
func eq(name, value string) mute.Matcher {
	return mute.Matcher{Name: name, Value: value, IsEqual: true}
}

// checkoutParams are parameters inside the first scope.
func checkoutParams(t *testing.T) json.RawMessage {
	t.Helper()

	return paramsJSON(t, params{
		Matchers: []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "checkout-api"), {Name: "env", Value: "prod|stage", IsRegex: true, IsEqual: true}},
		Duration: "1h",
		Comment:  "Planned database migration",
	})
}

// invocation is the Invocation Execute gets for the proposal with the
// parameters and snapshot.
func invocation(raw json.RawMessage, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: proposalID, IdempotencyKey: action.IdempotencyKey(proposalID), Params: raw, Snapshot: &snap}
}

// snapshotAndRun takes the snapshot of the parameters and executes it.
func snapshotAndRun(t *testing.T, s *silencer, raw json.RawMessage) (action.Snapshot, action.Result) {
	t.Helper()

	snap, err := s.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	res, err := s.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)

	return snap, res
}

// undoInvocation is the Invocation Undo gets for the proposal that made the
// silence.
func undoInvocation(raw json.RawMessage, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: otherProposalID, UndoOf: proposalID, Params: raw, Snapshot: &snap}
}

// bodyKeys returns the keys of a posted body.
func bodyKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

// requestPaths returns the method and path of every request the server got.
func requestPaths(api *fakehttp.Server) []string {
	var paths []string
	for _, r := range api.Requests() {
		paths = append(paths, r.Method+" "+r.Path)
	}

	return paths
}

// commentWithMarker is a silence comment as Execute writes it.
func commentWithMarker(text, id string) string {
	return strings.TrimSpace(text) + "\n" + mute.Marker(id)
}

// basicAuthOf returns the basic auth of a recorded request.
func basicAuthOf(t *testing.T, r fakehttp.Request) (user, password string, ok bool) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), r.Method, "http://example.com", http.NoBody)
	require.NoError(t, err)

	req.Header = r.Header

	return req.BasicAuth()
}
