package launchdarklyflag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// The paths of the flag the tests change.
const (
	flagPath      = "/api/v2/flags/shop/checkout-new"
	envPath       = "/api/v2/projects/shop/environments/production"
	scheduledPath = "/api/v2/projects/shop/flags/checkout-new/environments/production/scheduled-changes"
	workflowsPath = "/api/v2/projects/shop/flags/checkout-new/environments/production/workflows"
)

// paramsJSON are the parameters of a valid proposal.
const paramsJSON = `{"project":"shop","environment":"production","flag":"checkout-new","change":"turn_off"}`

// fakeToken resolves every secret to a fake token.
type fakeToken struct{}

func (fakeToken) Resolve(_ context.Context, _ registry.SecretRef) ([]byte, error) {
	return []byte("fake-token\n"), nil
}

// patch is one PATCH the fake LaunchDarkly received.
type patch struct {
	contentType string
	body        string
}

// fakeLaunchDarkly is a LaunchDarkly that keeps one flag in one
// environment. A semantic patch sets on and moves the version on.
type fakeLaunchDarkly struct {
	mu       sync.Mutex
	on       bool
	version  int
	archived bool
	tags     []string
	// missing makes the flag read answer 404.
	missing bool
	// noConfig makes the flag read answer with no configuration for the
	// environment.
	noConfig bool

	approvalsRequired bool
	approvalTags      []string
	scheduled         int
	workflows         int

	// envStatus, scheduledStatus, workflowsStatus and flagStatus, when set,
	// are the status those reads answer.
	envStatus       int
	scheduledStatus int
	workflowsStatus int
	flagStatus      int
	// patchStatus, when set, is the status a PATCH answers, with no change.
	patchStatus int

	patches []patch
}

// newFake starts the fake API for one test, with the flag on at version 7.
func newFake(t *testing.T) (*fakehttp.Server, *fakeLaunchDarkly) {
	t.Helper()

	f := &fakeLaunchDarkly{on: true, version: 7, tags: []string{"checkout"}}

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodGet, flagPath, f.getFlag)
	api.HandleFunc(http.MethodPatch, flagPath, f.patchFlag)
	api.HandleFunc(http.MethodGet, envPath, f.getEnvironment)
	api.HandleFunc(http.MethodGet, scheduledPath, f.getScheduled)
	api.HandleFunc(http.MethodGet, workflowsPath, f.getWorkflows)

	return api, f
}

func (f *fakeLaunchDarkly) getFlag(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case f.flagStatus != 0:
		w.WriteHeader(f.flagStatus)
	case f.missing:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"Unknown feature flag"}`))
	case r.URL.Query().Get("env") != "production":
		w.WriteHeader(http.StatusBadRequest)
	default:
		environments := map[string]any{}
		if !f.noConfig {
			environments["production"] = map[string]any{
				"on": f.on, "archived": false, "version": f.version,
				"_site": map[string]string{"href": "/shop/production/features/checkout-new"},
			}
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"key": "checkout-new", "archived": f.archived, "tags": f.tags, "environments": environments,
		})
	}
}

func (f *fakeLaunchDarkly) patchFlag(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var body struct {
		Comment        string              `json:"comment"`
		EnvironmentKey string              `json:"environmentKey"`
		Instructions   []map[string]string `json:"instructions"`
	}

	buf, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	f.patches = append(f.patches, patch{contentType: r.Header.Get("Content-Type"), body: string(buf)})

	if f.patchStatus != 0 {
		w.WriteHeader(f.patchStatus)
		_, _ = w.Write([]byte(`{"code":"refused","message":"The change was refused"}`))

		return
	}

	if err := json.Unmarshal(buf, &body); err != nil || len(body.Instructions) != 1 {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	want := body.Instructions[0]["kind"] == "turnFlagOn"
	if f.on != want {
		f.on = want
		f.version++
	}

	_, _ = w.Write([]byte(`{}`))
}

func (f *fakeLaunchDarkly) getEnvironment(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.envStatus != 0 {
		w.WriteHeader(f.envStatus)

		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"key": "production",
		"approvalSettings": map[string]any{
			"required": f.approvalsRequired, "requiredApprovalTags": f.approvalTags,
		},
	})
}

func (f *fakeLaunchDarkly) getScheduled(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.scheduledStatus != 0 {
		w.WriteHeader(f.scheduledStatus)

		return
	}

	items := make([]map[string]string, f.scheduled)
	for i := range items {
		items[i] = map[string]string{"_id": "sc"}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func (f *fakeLaunchDarkly) getWorkflows(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.workflowsStatus != 0 {
		w.WriteHeader(f.workflowsStatus)

		return
	}

	if r.URL.Query().Get("status") != "active" {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "totalCount": f.workflows})
}

// state returns the flag's on setting and version.
func (f *fakeLaunchDarkly) state() (on bool, version int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.on, f.version
}

// patchLog returns the PATCH requests received so far.
func (f *fakeLaunchDarkly) patchLog() []patch {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]patch(nil), f.patches...)
}

// setup changes the fake before a test uses it.
func (f *fakeLaunchDarkly) setup(change func(f *fakeLaunchDarkly)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// limitedConfig is a config whose limits allow the flag of the tests.
func limitedConfig(api *fakehttp.Server) Config {
	return Config{
		TokenSecret: tokenRef,
		BaseURL:     api.URL(),
		Limits: Limits{Projects: []Project{
			{Key: "shop", Environments: []string{"production"}, Flags: []string{"checkout-*"}},
		}},
	}
}

// buildAction builds the action against api, with cfg.
func buildAction(t *testing.T, api *fakehttp.Server, cfg Config) action.Action {
	t.Helper()

	a, err := New(context.Background(), "flag-off", cfg, registry.Deps{Secrets: fakeToken{}, HTTP: api.Client()})
	require.NoError(t, err)

	return a
}

// snapshotOf takes the snapshot of the flag of the tests.
func snapshotOf(t *testing.T, a action.Action) action.Snapshot {
	t.Helper()

	snap, err := a.(action.Snapshotter).Snapshot(context.Background(), json.RawMessage(paramsJSON))
	require.NoError(t, err)

	return snap
}

// invocation is the approved request to run the snapshot.
func invocation(snap action.Snapshot) action.Invocation {
	return action.Invocation{
		ProposalID:     "p1",
		Params:         json.RawMessage(paramsJSON),
		IdempotencyKey: action.IdempotencyKey("p1"),
		ApprovedBy:     "user-1",
		Snapshot:       &snap,
	}
}
