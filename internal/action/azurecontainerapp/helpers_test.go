package azurecontainerapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// The app the tests roll back.
const (
	testSub   = "11111111-2222-3333-4444-555555555555"
	testGroup = "shop"
	testApp   = "checkout"
	testToken = "fake-token"
)

// appPath is the path of the app on the fake, and revisionsPath the path of
// its revisions.
const (
	appPath       = "/subscriptions/" + testSub + "/resourceGroups/" + testGroup + "/providers/Microsoft.App/containerApps/" + testApp
	revisionsPath = appPath + "/revisions"
	operationPath = "/operations/1"
)

// testStart is when the oldest revision of the fake app was created.
var testStart = time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)

// fakeCredential gives the same token for every scope.
type fakeCredential struct{}

// GetToken implements azcore.TokenCredential.
func (fakeCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: testToken, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// fakeRevision is one revision of the fake app.
type fakeRevision struct {
	name    string
	created *time.Time
	active  bool
	image   string
}

// fakeARM is a Container App on a fake Azure Resource Manager. It keeps the
// state the tests change between calls, and it applies a PATCH the way a
// JSON merge patch does: the traffic list is replaced whole.
type fakeARM struct {
	api *fakehttp.Server

	mu          sync.Mutex
	mode        string
	ingress     bool
	traffic     []map[string]any
	latest      string
	latestReady string
	state       string
	revisions   []fakeRevision
	// weightOverride gives a revision a reported weight other than the one
	// the traffic list implies.
	weightOverride map[string]int
	// getStatus, patchStatus and operationStatus make the route answer with
	// the status and an Azure error body, in place of its own answer.
	getStatus       int
	patchStatus     int
	operationStatus int
	patches         [][]byte
	polls           int
}

// newFakeARM starts the fake with an app in Multiple mode that has four
// revisions, created an hour apart: r1 (inactive), r2, r3 and r4. All the
// traffic goes to r3, by name.
func newFakeARM(t *testing.T) *fakeARM {
	t.Helper()

	f := &fakeARM{
		api:         fakehttp.New(t, fakehttp.WithTLS()),
		mode:        "Multiple",
		ingress:     true,
		traffic:     []map[string]any{{"revisionName": "checkout--r3", "weight": 100}},
		latest:      "checkout--r4",
		latestReady: "checkout--r4",
		state:       "Succeeded",
	}

	for i, active := range []bool{false, true, true, true} {
		at := testStart.Add(time.Duration(i) * time.Hour)
		f.revisions = append(f.revisions, fakeRevision{
			name:    fmt.Sprintf("checkout--r%d", i+1),
			created: &at,
			active:  active,
			image:   fmt.Sprintf("shop.azurecr.io/checkout:1.4.%d", i+1),
		})
	}

	f.api.HandleFunc(http.MethodGet, appPath, f.getApp)
	f.api.HandleFunc(http.MethodPatch, appPath, f.patchApp)
	f.api.HandleFunc(http.MethodGet, revisionsPath, f.listRevisions)
	f.api.HandleFunc(http.MethodGet, operationPath, f.operation)

	return f
}

// change runs fn on the fake's state.
func (f *fakeARM) change(fn func(f *fakeARM)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(f)
}

// patchBodies returns the bodies of the PATCH requests so far.
func (f *fakeARM) patchBodies() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([][]byte(nil), f.patches...)
}

// reportedWeight is the traffic weight Azure reports for a revision.
func (f *fakeARM) reportedWeight(name string) int {
	if w, ok := f.weightOverride[name]; ok {
		return w
	}

	total := 0

	for _, e := range f.traffic {
		weight, _ := e["weight"].(int)
		entry, _ := e["revisionName"].(string)

		if latest, _ := e["latestRevision"].(bool); latest {
			entry = f.latestReady
		}

		if entry == name {
			total += weight
		}
	}

	return total
}

// appJSON is the app as Azure answers a read.
func (f *fakeARM) appJSON() map[string]any {
	configuration := map[string]any{"activeRevisionsMode": f.mode}
	if f.mode == "" {
		delete(configuration, "activeRevisionsMode")
	}

	if f.ingress {
		configuration["ingress"] = map[string]any{"external": true, "targetPort": 8080, "traffic": f.traffic}
	}

	return map[string]any{
		"name":     testApp,
		"location": "East US",
		"properties": map[string]any{
			"provisioningState":       f.state,
			"latestRevisionName":      f.latest,
			"latestReadyRevisionName": f.latestReady,
			"configuration":           configuration,
		},
	}
}

// failure writes an Azure error body with status.
func failure(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_, _ = fmt.Fprintf(w, `{"error":{"code":"Failure%d","message":"it failed"}}`, status)
}

// writeJSON writes v as a JSON answer.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeARM) getApp(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.getStatus != 0 {
		failure(w, f.getStatus)

		return
	}

	writeJSON(w, http.StatusOK, f.appJSON())
}

func (f *fakeARM) listRevisions(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	value := []map[string]any{}

	for _, r := range f.revisions {
		props := map[string]any{
			"active":        r.active,
			"trafficWeight": f.reportedWeight(r.name),
			"template":      map[string]any{"containers": []map[string]any{{"name": "main", "image": r.image}}},
		}

		if r.created != nil {
			props["createdTime"] = r.created.Format(time.RFC3339)
		}

		value = append(value, map[string]any{"name": r.name, "properties": props})
	}

	writeJSON(w, http.StatusOK, map[string]any{"value": value, "nextLink": nil})
}

func (f *fakeARM) patchApp(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.patches = append(f.patches, body)

	if f.patchStatus != 0 {
		failure(w, f.patchStatus)

		return
	}

	var patch struct {
		Properties struct {
			Configuration struct {
				Ingress struct {
					Traffic []map[string]any `json:"traffic"`
				} `json:"ingress"`
			} `json:"configuration"`
		} `json:"properties"`
	}

	if err := json.Unmarshal(body, &patch); err != nil {
		failure(w, http.StatusBadRequest)

		return
	}

	f.traffic = nil

	for _, e := range patch.Properties.Configuration.Ingress.Traffic {
		if weight, ok := e["weight"].(float64); ok {
			e["weight"] = int(weight)
		}

		f.traffic = append(f.traffic, e)
	}

	f.state, f.polls = "InProgress", 0

	w.Header().Set("Location", f.api.URL()+operationPath+"?api-version=2026-07-01")
	w.WriteHeader(http.StatusAccepted)
}

// operation answers the poll of a PATCH: still running the first time, done
// the second.
func (f *fakeARM) operation(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.operationStatus != 0 {
		failure(w, f.operationStatus)

		return
	}

	f.polls++

	if f.polls < 2 {
		w.Header().Set("Location", f.api.URL()+operationPath+"?api-version=2026-07-01")
		w.WriteHeader(http.StatusAccepted)

		return
	}

	f.state = "Succeeded"

	writeJSON(w, http.StatusOK, f.appJSON())
}

// newTestAction returns the action on the fake, with limits that allow the
// test app and maxBack as maxRevisionsBack.
func newTestAction(t *testing.T, f *fakeARM, maxBack int) *Rollback {
	t.Helper()

	cfg := Config{
		RevisionLimit: action.RevisionLimit{MaxRevisionsBack: &maxBack},
		Limits:        Limits{Subscriptions: []string{testSub}, ResourceGroups: []string{testGroup}, Apps: []string{"checkout-*", testApp}},
	}

	require.NoError(t, cfg.Validate())

	r := newRollback("rollback", cfg, fakeCredential{}, &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: f.api.Client(),
			Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {Endpoint: f.api.URL(), Audience: "https://management.core.windows.net/"},
			}},
			Retry: policy.RetryOptions{MaxRetries: -1},
		},
		DisableRPRegistration: true,
	})
	r.arms.pollEvery = time.Millisecond

	return r
}

// params returns the parameters of a rollback of the test app, revisions
// back.
func params(back int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"subscription": %q, "resourceGroup": %q, "app": %q, "revisionsBack": %d}`, testSub, testGroup, testApp, back))
}

// snapshotOf returns the snapshot of a rollback revisions back.
func snapshotOf(t *testing.T, r *Rollback, back int) action.Snapshot {
	t.Helper()

	snap, err := r.Snapshot(t.Context(), params(back))
	require.NoError(t, err)

	return snap
}

// invocation returns the invocation of a rollback revisions back.
func invocation(back int, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: "p1", Params: params(back), IdempotencyKey: "k1", ApprovedBy: "u1", Snapshot: &snap}
}

// trafficJSON returns the traffic lists the PATCH bodies hold, one per
// body, after checking that a body has only the location and the traffic.
func trafficJSON(t *testing.T, bodies [][]byte) []string {
	t.Helper()

	var out []string

	for _, body := range bodies {
		var doc struct {
			Location   string `json:"location"`
			Properties struct {
				Configuration struct {
					Ingress map[string]json.RawMessage `json:"ingress"`
				} `json:"configuration"`
			} `json:"properties"`
		}

		require.NoError(t, json.Unmarshal(body, &doc))
		require.Equal(t, "East US", doc.Location)
		require.Len(t, doc.Properties.Configuration.Ingress, 1, "only the traffic list is sent: %s", body)

		var generic map[string]json.RawMessage

		require.NoError(t, json.Unmarshal(body, &generic))
		require.Len(t, generic, 2, "only the location and the properties are sent: %s", body)

		out = append(out, string(doc.Properties.Configuration.Ingress["traffic"]))
	}

	return out
}
