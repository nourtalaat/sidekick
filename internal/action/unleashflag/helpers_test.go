package unleashflag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
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

// The parameters of the two changes, for the flag the tests change.
const (
	turnOffParams = `{"project": "default", "environment": "production", "flag": "checkout-v2", "change": "turn_off"}`
	restoreParams = `{"project": "default", "environment": "production", "flag": "checkout-v2", "change": "restore_previous"}`
)

// testToken is the token the fake secrets resolver gives, with the white
// space a Secret file often has.
const testToken = "*:production.0123456789abcdef"

// strategyIDs are the IDs the fake serves a strategy under: the three it
// starts with, and the ones it gives to strategies that are added.
var strategyIDs = []string{"s1", "s2", "s3", "n1", "n2", "n3"}

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

// fakeUnleash is an Unleash that keeps one flag in one environment, and
// stores the events its changes make, as Unleash does: a strategy change
// stores an event with the strategy, and Unleash turns the environment off
// by itself when no strategy that is enabled is left.
type fakeUnleash struct {
	mu         sync.Mutex
	enabled    bool
	strategies []map[string]any
	// events holds the events of the flag, oldest first.
	events []map[string]any
	// archived makes the flag answer 404, as Unleash does for it.
	archived bool
	// noEnvironment leaves the environment out of the flag.
	noEnvironment bool
	// writes lists what changed the flag, in order: "off", "on", "create",
	// "update s1", "delete s2". bodies holds the body of each create and
	// update.
	writes []string
	bodies []json.RawMessage
	// failures are answers by route, such as "POST off".
	failures     map[string]fakeFailure
	nextStrategy int
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
	api.HandleFunc(http.MethodPost, testEnvPath+"/strategies", f.postStrategy)

	for _, id := range strategyIDs {
		api.HandleFunc(http.MethodPut, testEnvPath+"/strategies/"+id, f.putStrategy(id))
		api.HandleFunc(http.MethodDelete, testEnvPath+"/strategies/"+id, f.deleteStrategy(id))
	}

	return api, f
}

// fail makes the route, such as "POST off", answer with status and body.
func (f *fakeUnleash) fail(route string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failures[route] = fakeFailure{status: status, body: body}
}

// changedElsewhere adds an event of the flag that Sidekick cannot restore,
// as if someone else changed something about it.
func (f *fakeUnleash) changedElsewhere() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.addEvent("feature-environment-variants-updated", nil, nil)
}

// externalEvent adds an event of a type, with a group, and no data.
func (f *fakeUnleash) externalEvent(eventType, groupID string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := f.addEvent(eventType, nil, nil)
	if groupID != "" {
		f.events[len(f.events)-1]["groupId"] = groupID
	}

	return id
}

// externalToggle turns the environment on or off, as another person does.
func (f *fakeUnleash) externalToggle(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.enabled = on
	f.addEvent(toggleEvent(on), nil, nil)
}

// externalUpdate changes a strategy, as another person does.
func (f *fakeUnleash) externalUpdate(id string, change func(m map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.update(id, change)
}

// externalAdd adds a strategy, as another person does, and returns its ID.
func (f *fakeUnleash) externalAdd(strategy map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.add(strategy)
}

// externalRemove removes a strategy, as another person does.
func (f *fakeUnleash) externalRemove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.remove(id)
}

// edit changes the flag without an event, to make the flag differ from
// what its events say.
func (f *fakeUnleash) edit(change func(f *fakeUnleash)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// strategy returns a copy of the strategy with id.
func (f *fakeUnleash) strategy(id string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return clone(f.find(id))
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

// writtenBodies returns the bodies of the creates and updates.
func (f *fakeUnleash) writtenBodies() []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.bodies)
}

// state returns the state of the flag as Sidekick reads it.
func (f *fakeUnleash) state(t *testing.T) state {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	raw, err := json.Marshal(f.strategies)
	require.NoError(t, err)

	var live []liveStrategy
	require.NoError(t, json.Unmarshal(raw, &live))

	st, err := newState(f.enabled, live)
	require.NoError(t, err)

	return st
}

// requireSameModuloIDs requires two states to hold the same values, where a
// strategy that was removed and added again has another ID.
func requireSameModuloIDs(t *testing.T, want, got state) {
	t.Helper()

	require.Equal(t, want.Enabled, got.Enabled)
	require.Len(t, got.Strategies, len(want.Strategies))

	for id, w := range want.Strategies {
		_, ok := got.idOf(w)
		require.True(t, ok, "the strategy that was %s is there", id)
	}
}

// addEvent stores an event, and returns its ID. The caller holds f.mu.
func (f *fakeUnleash) addEvent(eventType string, data, preData any) int64 {
	id := int64(len(f.events)) + 100

	ev := map[string]any{
		"id": id, "type": eventType, "createdBy": "ana@example.com", "createdAt": "2026-11-03T10:00:00.000Z",
		"environment": testEnv, "project": testProject, "featureName": testFlag,
	}

	if data != nil {
		ev["data"] = data
	}

	if preData != nil {
		ev["preData"] = preData
	}

	f.events = append(f.events, ev)

	return id
}

// find returns the strategy with id, or nil. The caller holds f.mu.
func (f *fakeUnleash) find(id string) map[string]any {
	for _, s := range f.strategies {
		if s["id"] == id {
			return s
		}
	}

	return nil
}

// add adds a strategy and stores its event. The caller holds f.mu.
func (f *fakeUnleash) add(strategy map[string]any) string {
	f.nextStrategy++

	id := fmt.Sprintf("n%d", f.nextStrategy)
	added := clone(strategy)
	added["id"] = id
	added["milestoneId"] = nil
	f.strategies = append(f.strategies, added)
	f.addEvent("feature-strategy-add", eventData(added), nil)

	return id
}

// update sets the strategy with id as change says and stores its event,
// unless nothing changed. Unleash then turns the environment off when no
// enabled strategy is left. The caller holds f.mu.
func (f *fakeUnleash) update(id string, change func(m map[string]any)) {
	current := f.find(id)
	if current == nil {
		return
	}

	pre := eventData(current)

	change(current)

	if !reflect.DeepEqual(pre, eventData(current)) {
		f.addEvent("feature-strategy-update", eventData(current), pre)
	}

	f.turnOffWithoutEnabledStrategy()
}

// remove removes the strategy with id and stores its event, after Unleash
// turned the environment off when no enabled strategy is left, which is the
// order Unleash stores them in. The caller holds f.mu.
func (f *fakeUnleash) remove(id string) {
	removed := f.find(id)
	if removed == nil {
		return
	}

	f.strategies = slices.DeleteFunc(f.strategies, func(s map[string]any) bool { return s["id"] == id })
	f.turnOffWithoutEnabledStrategy()
	f.addEvent("feature-strategy-remove", nil, eventData(removed))
}

// turnOffWithoutEnabledStrategy turns the environment off when every
// strategy is disabled, or there is none. The caller holds f.mu.
func (f *fakeUnleash) turnOffWithoutEnabledStrategy() {
	if !f.enabled {
		return
	}

	if !slices.ContainsFunc(f.strategies, func(s map[string]any) bool { return s["disabled"] != true }) {
		f.enabled = false
		f.addEvent(toggleEvent(false), nil, nil)
	}
}

// failed answers with the failure for route, if a test set one. The caller
// holds f.mu.
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
		events = append(events, f.events[n-1])
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
		f.addEvent(toggleEvent(on), nil, nil)
	}
}

func (f *fakeUnleash) postStrategy(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failed(w, "POST create") {
		return
	}

	body, strategy := readBody(w, r)
	if strategy == nil {
		return
	}

	f.writes = append(f.writes, "create")
	f.bodies = append(f.bodies, body)

	id := f.add(strategy)

	_ = json.NewEncoder(w).Encode(f.find(id))
}

func (f *fakeUnleash) putStrategy(id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if f.failed(w, "PUT update") {
			return
		}

		body, strategy := readBody(w, r)
		if strategy == nil {
			return
		}

		f.writes = append(f.writes, "update "+id)
		f.bodies = append(f.bodies, body)

		f.update(id, func(m map[string]any) {
			for k, v := range strategy {
				m[k] = v
			}
		})

		_ = json.NewEncoder(w).Encode(f.find(id))
	}
}

func (f *fakeUnleash) deleteStrategy(id string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if f.failed(w, "DELETE delete") {
			return
		}

		f.writes = append(f.writes, "delete "+id)
		f.remove(id)
	}
}

// readBody reads the strategy of a request. It answers 400 and returns a nil
// strategy when the body is not one.
func readBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, map[string]any) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return nil, nil
	}

	var strategy map[string]any
	if err := json.Unmarshal(raw, &strategy); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return nil, nil
	}

	return raw, strategy
}

// toggleEvent is the type of the event of turning the environment on or off.
func toggleEvent(on bool) string {
	if on {
		return "feature-environment-enabled"
	}

	return "feature-environment-disabled"
}

// eventData is the strategy as an event holds it, without the milestone.
func eventData(strategy map[string]any) map[string]any {
	data := clone(strategy)
	delete(data, "milestoneId")

	return data
}

// clone returns a deep copy of a JSON value.
func clone[T any](v T) T {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}

	return out
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

// allowRestore lets the action restore the previous state.
func allowRestore(c *Config) {
	c.Limits.Allow = []string{changeTurnOff, changeRestorePrevious}
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
