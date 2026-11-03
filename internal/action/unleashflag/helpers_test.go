package unleashflag

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// The flag the tests change.
const (
	testProject = "default"
	testEnv     = "production"
	testFlag    = "checkout-v2"
)

// testFlagPath is the path of the flag, and testEnvPath of the flag in its
// environment.
const (
	testFlagPath = "/api/admin/projects/default/features/checkout-v2"
	testEnvPath  = testFlagPath + "/environments/production"
)

// turnOffParams is the parameters that turn the flag off.
const turnOffParams = `{"project": "default", "environment": "production", "flag": "checkout-v2", "change": "turn_off"}`

// testToken is the token the fake secrets resolver gives, with the white
// space a Secret file often has.
const testToken = "*:production.0123456789abcdef"

// fakeSecrets resolves every secret to the test token.
type fakeSecrets struct{}

func (fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(testToken + "\n"), nil
}

// fakeFailure is an answer a test makes a route give in place of its own.
type fakeFailure struct {
	status int
	body   string
}

// fakeUnleash is an Unleash that keeps one flag in one environment.
type fakeUnleash struct {
	mu         sync.Mutex
	enabled    bool
	strategies []map[string]any
	// events holds the IDs of the events of the flag, oldest first.
	events []int64
	// archived makes the flag answer 404, as Unleash does for it.
	archived bool
	// noEnvironment leaves the environment out of the flag.
	noEnvironment bool
	// writes lists what changed the flag, in order: "off", "on".
	writes []string
	// failures are answers by route, such as "POST off".
	failures map[string]fakeFailure
}

// newFakeUnleash starts the fake API for one test, with the flag on, three
// strategies and no events.
func newFakeUnleash(t *testing.T) (*fakehttp.Server, *fakeUnleash) {
	t.Helper()

	data, err := os.ReadFile("fixture/strategies.json")
	require.NoError(t, err)

	f := &fakeUnleash{enabled: true, failures: map[string]fakeFailure{}}
	require.NoError(t, json.Unmarshal(data, &f.strategies))

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodGet, testFlagPath, f.getFlag)
	api.HandleFunc(http.MethodGet, testEnvPath+"/strategies", f.getStrategies)
	api.HandleFunc(http.MethodGet, "/api/admin/search/events", f.getEvents)
	api.HandleFunc(http.MethodPost, testEnvPath+"/off", func(w http.ResponseWriter, _ *http.Request) { f.toggle(w, "off", false) })
	api.HandleFunc(http.MethodPost, testEnvPath+"/on", func(w http.ResponseWriter, _ *http.Request) { f.toggle(w, "on", true) })

	return api, f
}

// fail makes the route, such as "POST off", answer with status and body.
func (f *fakeUnleash) fail(route string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failures[route] = fakeFailure{status: status, body: body}
}

// changedElsewhere adds an event of the flag, as if someone else changed it.
func (f *fakeUnleash) changedElsewhere() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.addEvent()
}

// addEvent stores an event, and returns its ID. The caller holds f.mu.
func (f *fakeUnleash) addEvent() int64 {
	id := int64(len(f.events)) + 100
	f.events = append(f.events, id)

	return id
}

// isEnabled reports whether the flag is on.
func (f *fakeUnleash) isEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.enabled
}

// written returns what changed the flag.
func (f *fakeUnleash) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.writes)
}

// failed answers with the failure for route, if a test set one.
func (f *fakeUnleash) failed(w http.ResponseWriter, route string) bool {
	failure, ok := f.failures[route]
	if !ok {
		return false
	}

	w.WriteHeader(failure.status)
	_, _ = w.Write([]byte(failure.body))

	return true
}

func (f *fakeUnleash) getFlag(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failed(w, "GET flag") {
		return
	}

	if f.archived {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"name": "NotFoundError", "message": "No feature toggle found"}`))

		return
	}

	environments := []map[string]any{{"name": "staging", "enabled": true}}
	if !f.noEnvironment {
		environments = append(environments, map[string]any{"name": testEnv, "enabled": f.enabled, "strategies": f.strategies})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"name": testFlag, "archived": false, "environments": environments})
}

func (f *fakeUnleash) getStrategies(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failed(w, "GET strategies") {
		return
	}

	_ = json.NewEncoder(w).Encode(f.strategies)
}

func (f *fakeUnleash) getEvents(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failed(w, "GET events") {
		return
	}

	events := []map[string]any{}
	if n := len(f.events); n > 0 {
		events = append(events, map[string]any{"id": f.events[n-1], "type": "feature-environment-enabled", "createdBy": "ana@example.com"})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "total": len(f.events)})
}

// toggle sets the environment on or off. Turning it to the state it has
// changes nothing and stores no event, as in Unleash.
func (f *fakeUnleash) toggle(w http.ResponseWriter, verb string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failed(w, "POST "+verb) {
		return
	}

	f.writes = append(f.writes, verb)

	if f.enabled != on {
		f.enabled = on
		f.addEvent()
	}
}

// newTestAction returns the action, talking to api, with limits the tests
// use unless a test changes them.
func newTestAction(t *testing.T, api *fakehttp.Server, change func(c *Config)) *Flag {
	t.Helper()

	cfg := Config{
		BaseURL:     api.URL(),
		TokenSecret: tokenRef,
		Limits: Limits{Projects: []Project{
			{Key: testProject, Environments: []string{testEnv}, Flags: []string{"checkout-*", "new-search"}},
		}},
	}

	if change != nil {
		change(&cfg)
	}

	a, err := New(t.Context(), "unleash-flag", cfg, registry.Deps{Secrets: fakeSecrets{}, HTTP: api.Client()})
	require.NoError(t, err)

	flag, ok := a.(*Flag)
	require.True(t, ok)

	return flag
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
func proposed(t *testing.T, a *Flag, params string) action.Snapshot {
	t.Helper()

	snap, err := a.Snapshot(t.Context(), json.RawMessage(params))
	require.NoError(t, err)

	return snap
}
