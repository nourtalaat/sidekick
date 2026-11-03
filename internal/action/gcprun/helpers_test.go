package gcprun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// The service the tests roll back, and its paths on the fake.
const (
	testProject  = "shop-prod"
	testLocation = "europe-west1"
	testService  = "checkout"

	servicePath   = "/v2/projects/shop-prod/locations/europe-west1/services/checkout"
	revisionsPath = servicePath + "/revisions"
	operationName = "projects/shop-prod/locations/europe-west1/operations/op-1"
	operationPath = "/v2/" + operationName

	testToken = "test-access-token"
)

// paramsJSON are the parameters of a valid proposal that goes one back.
const paramsJSON = `{"project":"shop-prod","location":"europe-west1","service":"checkout"}`

// The traffic lists the tests use.
var (
	latestTraffic = []map[string]any{{"type": allocationLatest, "percent": 100}}
	pinnedTraffic = func(name string) []map[string]any {
		return []map[string]any{{"type": allocationRevision, "revision": name, "percent": 100}}
	}
)

// patchCall is one PATCH the fake Cloud Run received.
type patchCall struct {
	query url.Values
	body  map[string]any
}

// fakeRun is a Cloud Run service with revisions. A PATCH sets its traffic,
// moves its etag on and starts an operation that is done after pollsToDone
// polls.
type fakeRun struct {
	mu sync.Mutex

	etag         int
	traffic      []map[string]any
	reconciling  bool
	missing      bool
	latestReady  string
	lastModifier string
	// revisions are the revisions, newest first, as Cloud Run lists them.
	revisions []map[string]any
	// pages cuts the list into pages of this many revisions; zero means one
	// page.
	pages int

	// pollsToDone is how many polls answer "not done" before the operation
	// is done.
	pollsToDone int
	polls       int
	// opError is the error of the finished operation.
	opError map[string]any
	// patchStatus, when not zero, is the status of the PATCH answer, which
	// then changes nothing.
	patchStatus int
	// pollFailures is how many polls answer 500 before they work.
	pollFailures int

	patches []patchCall
}

// newFakeRun starts a fake Cloud Run on api, with the service following the
// latest revision.
func newFakeRun(t *testing.T, api *fakehttp.Server) *fakeRun {
	t.Helper()

	f := &fakeRun{
		etag:         1,
		traffic:      latestTraffic,
		latestReady:  "projects/shop-prod/locations/europe-west1/services/checkout/revisions/checkout-00008-bbb",
		lastModifier: "ana@example.com",
		revisions: []map[string]any{
			fakeRevision("checkout-00009-ccc", "2026-11-03T09:00:00Z", false, "ana@example.com", "registry.example.com/checkout:1.4.9"),
			fakeRevision("checkout-00008-bbb", "2026-11-03T08:00:00Z", true, "ana@example.com", "registry.example.com/checkout:1.4.8"),
			fakeRevision("checkout-00007-aaa", "2026-11-02T08:00:00Z", true, "bo@example.com", "registry.example.com/checkout:1.4.7"),
			fakeRevision("checkout-00006-zzz", "2026-11-01T08:00:00Z", false, "bo@example.com", "registry.example.com/checkout:1.4.6"),
			fakeRevision("checkout-00005-yyy", "2026-10-31T08:00:00Z", true, "", "registry.example.com/checkout:1.4.5"),
			fakeRevision("checkout-00004-xxx", "2026-10-30T08:00:00Z", true, "bo@example.com", "registry.example.com/checkout:1.4.4"),
		},
	}

	api.HandleFunc(http.MethodGet, servicePath, f.getService)
	api.HandleFunc(http.MethodPatch, servicePath, f.patchService)
	api.HandleFunc(http.MethodGet, revisionsPath, f.listRevisions)
	api.HandleFunc(http.MethodGet, operationPath, f.getOperation)

	return f
}

// fakeRevision returns a revision of the fake, with its image and Ready condition.
func fakeRevision(name, created string, ready bool, creator, image string) map[string]any {
	state := "CONDITION_FAILED"
	if ready {
		state = "CONDITION_SUCCEEDED"
	}

	rev := map[string]any{
		"name":       "projects/shop-prod/locations/europe-west1/services/checkout/revisions/" + name,
		"createTime": created,
		"containers": []map[string]any{{"image": image}},
		"conditions": []map[string]any{{"type": "Ready", "state": state}, {"type": "Active", "state": "CONDITION_SUCCEEDED"}},
	}

	if creator != "" {
		rev["creator"] = creator
	}

	return rev
}

// serviceJSON is the service as the fake answers a read.
func (f *fakeRun) serviceJSON() map[string]any {
	return map[string]any{
		"name":                "projects/shop-prod/locations/europe-west1/services/checkout",
		"etag":                fmt.Sprintf("etag-%d", f.etag),
		"lastModifier":        f.lastModifier,
		"latestReadyRevision": f.latestReady,
		"reconciling":         f.reconciling,
		"traffic":             f.traffic,
		"template":            map[string]any{"containers": []map[string]any{{"image": "registry.example.com/checkout:1.4.9"}}},
	}
}

func (f *fakeRun) getService(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.missing {
		reply(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404, "status": "NOT_FOUND", "message": "service not found"}})

		return
	}

	reply(w, http.StatusOK, f.serviceJSON())
}

func (f *fakeRun) patchService(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	data, _ := io.ReadAll(req.Body)

	var body map[string]any

	_ = json.Unmarshal(data, &body)

	f.patches = append(f.patches, patchCall{query: req.URL.Query(), body: body})

	if f.patchStatus != 0 {
		reply(w, f.patchStatus, map[string]any{"error": map[string]any{"code": f.patchStatus, "status": "ABORTED", "message": "the etag does not match"}})

		return
	}

	raw, _ := json.Marshal(body["traffic"])

	var traffic []map[string]any

	_ = json.Unmarshal(raw, &traffic)

	f.traffic = traffic
	f.etag++
	f.polls = 0

	reply(w, http.StatusOK, map[string]any{"name": operationName, "done": false})
}

func (f *fakeRun) listRevisions(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	start := 0
	if token := req.URL.Query().Get("pageToken"); token != "" {
		_, _ = fmt.Sscanf(token, "page-%d", &start)
	}

	end := len(f.revisions)
	if f.pages > 0 && start+f.pages < end {
		end = start + f.pages
	}

	page := map[string]any{"revisions": f.revisions[start:end]}
	if end < len(f.revisions) {
		page["nextPageToken"] = fmt.Sprintf("page-%d", end)
	}

	reply(w, http.StatusOK, page)
}

func (f *fakeRun) getOperation(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.pollFailures > 0 {
		f.pollFailures--

		reply(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": 500, "message": "internal"}})

		return
	}

	f.polls++

	op := map[string]any{"name": operationName, "done": f.polls > f.pollsToDone}
	if f.opError != nil && f.polls > f.pollsToDone {
		op["error"] = f.opError
	}

	reply(w, http.StatusOK, op)
}

// reply writes a JSON answer.
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

// patchCount is how many PATCH requests the fake received.
func (f *fakeRun) patchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.patches)
}

// update changes the fake with its lock held, so a request that is being
// answered does not race with the test.
func (f *fakeRun) update(change func(*fakeRun)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// trafficNow is the traffic of the fake as a JSON text.
func (f *fakeRun) trafficNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return jsonText(f.traffic)
}

// calls returns the PATCH requests the fake received.
func (f *fakeRun) calls() []patchCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]patchCall(nil), f.patches...)
}

// jsonText writes v as JSON, for comparing traffic lists whose numbers have
// different Go types.
func jsonText(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}

	return string(data)
}

// setTraffic changes the traffic as a person would, moving the etag on.
func (f *fakeRun) setTraffic(traffic []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.traffic = traffic
	f.etag++
}

// deploy makes a new revision the latest one, as a deploy does, which moves
// the etag on.
func (f *fakeRun) deploy() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.etag++
	f.latestReady = "checkout-00010-ddd"
}

// limitedConfig is a valid config that allows the test service.
func limitedConfig() Config {
	return Config{Limits: Limits{
		Projects:  []string{testProject},
		Locations: []string{testLocation},
		Services:  []string{"checkout", "payments-*"},
	}}
}

// buildAction builds the action for cfg and points it at api.
func buildAction(t *testing.T, api *fakehttp.Server, cfg Config) *Rollback {
	t.Helper()

	built, err := New(context.Background(), "cloud-run-rollback", cfg, registry.Deps{HTTP: api.Client()})
	require.NoError(t, err)

	a, ok := built.(*Rollback)
	require.True(t, ok)

	a.client.baseURL = api.URL()
	a.client.tokens = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testToken})
	a.pollInterval = time.Millisecond

	return a
}

// snapshotOf takes the snapshot of a proposal, or fails the test.
func snapshotOf(t *testing.T, a *Rollback, params string) action.Snapshot {
	t.Helper()

	snap, err := a.Snapshot(context.Background(), json.RawMessage(params))
	require.NoError(t, err)

	return snap
}

// invocation is the approved run of a proposal with snap.
func invocation(snap *action.Snapshot) action.Invocation {
	return action.Invocation{
		ProposalID:     "p1",
		Params:         json.RawMessage(paramsJSON),
		IdempotencyKey: "key-1",
		ApprovedBy:     "user-1",
		Snapshot:       snap,
	}
}

// onlyGets reports whether every request the fake received was a GET.
func onlyGets(api *fakehttp.Server) bool {
	for _, r := range api.Requests() {
		if r.Method != http.MethodGet {
			return false
		}
	}

	return true
}
