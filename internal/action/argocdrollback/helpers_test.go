package argocdrollback

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testToken is the token the fake secrets resolver gives, with the white
// space a Secret file often has.
const testToken = "argocd-00000000-test-token"

// The paths the fake serves, for the application shop/checkout.
const (
	appPath      = "/api/v1/applications/checkout"
	rollbackPath = appPath + "/rollback"
)

// The parameters of the tests.
const (
	oneBack = `{"application": "shop/checkout"}`
	twoBack = `{"application": "shop/checkout", "revisionsBack": 2}`
)

// fakeSecrets resolves every secret to the test token.
type fakeSecrets struct{}

func (fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(testToken + "\n"), nil
}

// fakeFailure is an answer a test makes a route give in place of its own.
type fakeFailure struct {
	status  int
	message string
}

// fakeArgo is an Argo CD that keeps one application. A rollback sets an
// operation, as Argo CD does, and the operation ends after the application has
// been read pollsToEnd times: the history gets an entry with the next ID and
// the revision of the entry rolled back to, and the operation is cleared.
type fakeArgo struct {
	mu sync.Mutex

	history   []map[string]any
	autoSync  bool
	hydrator  bool
	sources   int
	images    []string
	limit     *int
	namespace string

	// operation is the revision of the operation that is set, or empty.
	operation string
	// phase is the phase the operation ends with.
	phase string
	// message is the message of the state of the operation that ends.
	message string
	// pollsToEnd is how many reads show the operation running before it ends.
	// A negative number never ends it.
	pollsToEnd int
	polls      int
	// ended is the state of the operation that ended last.
	ended map[string]any

	// rollbacks holds the history ID of each rollback call, and bodies its
	// body.
	rollbacks []int64
	bodies    []json.RawMessage
	// rollbackFailure and getFailure are answers in place of the route's own.
	rollbackFailure *fakeFailure
	getFailure      *fakeFailure
}

// newFakeArgo starts the fake API for one test: auto-sync off, and a history
// of three deployments by ana, which ran revA, revB and revC, with IDs 5, 6
// and 7. The application runs revC.
func newFakeArgo(t *testing.T) (*fakehttp.Server, *fakeArgo) {
	t.Helper()

	f := &fakeArgo{namespace: "argocd", phase: "Succeeded", pollsToEnd: 1, images: []string{"registry.example.com/shop/checkout:1.4"}}
	f.deploy(5, revA, "ana")
	f.deploy(6, revB, "ana")
	f.deploy(7, revC, "ana")

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodGet, appPath, f.getApplication)
	api.HandleFunc(http.MethodPost, rollbackPath, f.postRollback)

	return api, f
}

// deploy adds a history entry.
func (f *fakeArgo) deploy(id int64, revision, user string) {
	f.history = append(f.history, map[string]any{
		"id":          id,
		"revision":    revision,
		"deployedAt":  time.Date(2026, 11, 1, 9, int(id), 0, 0, time.UTC).Format(time.RFC3339),
		"source":      map[string]any{"repoURL": "https://git.example.com/shop/deploy.git", "path": "apps/checkout", "targetRevision": "main"},
		"initiatedBy": map[string]any{"username": user},
	})
}

// deployAgain is a deployment by someone else, after the proposal.
func (f *fakeArgo) deployAgain(revision string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deploy(f.newestID()+1, revision, "bob")
}

// update changes the fake while no request is served.
func (f *fakeArgo) update(change func(f *fakeArgo)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// rolledBack returns the IDs the rollback calls named.
func (f *fakeArgo) rolledBack() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]int64(nil), f.rollbacks...)
}

// newestID is the highest history ID, or -1 for no history.
func (f *fakeArgo) newestID() int64 {
	newest := int64(-1)

	for _, e := range f.history {
		if id, ok := e["id"].(int64); ok {
			newest = max(newest, id)
		}
	}

	return newest
}

func (f *fakeArgo) getApplication(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.getFailure != nil {
		answerError(w, *f.getFailure)

		return
	}

	if f.operation != "" {
		f.polls++
		if f.pollsToEnd >= 0 && f.polls > f.pollsToEnd {
			f.endOperation()
		}
	}

	answer(w, http.StatusOK, f.application())
}

func (f *fakeArgo) postRollback(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var body struct {
		ID json.Number `json:"id"`
	}

	dec := json.NewDecoder(r.Body)
	dec.UseNumber()

	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil || json.Unmarshal(raw, &body) != nil {
		answerError(w, fakeFailure{status: http.StatusBadRequest, message: "bad body"})

		return
	}

	id, _ := body.ID.Int64()
	f.rollbacks = append(f.rollbacks, id)
	f.bodies = append(f.bodies, raw)

	if f.rollbackFailure != nil {
		answerError(w, *f.rollbackFailure)

		return
	}

	for _, e := range f.history {
		if e["id"] == id {
			f.operation = e["revision"].(string)
			f.polls = 0
			f.ended = nil

			answer(w, http.StatusOK, f.application())

			return
		}
	}

	answerError(w, fakeFailure{status: http.StatusBadRequest, message: "application checkout does not have deployment with id " + body.ID.String()})
}

// endOperation ends the operation: a successful one adds a history entry.
func (f *fakeArgo) endOperation() {
	state := map[string]any{
		"phase":     f.phase,
		"message":   f.message,
		"startedAt": "2026-11-03T10:00:00Z",
		"operation": map[string]any{"sync": map[string]any{"revision": f.operation}},
	}

	if f.phase == "Succeeded" {
		f.deploy(f.newestID()+1, f.operation, "sidekick")
	}

	f.ended = state
	f.operation = ""
}

// application is the application as Argo CD answers a get.
func (f *fakeArgo) application() map[string]any {
	spec := map[string]any{
		"project":     "shop",
		"source":      map[string]any{"repoURL": "https://git.example.com/shop/deploy.git", "path": "apps/checkout", "targetRevision": "main"},
		"destination": map[string]any{"name": "prod", "namespace": "shop"},
	}

	if f.autoSync {
		spec["syncPolicy"] = map[string]any{"automated": map[string]any{"prune": true}}
	}

	if f.sources > 0 {
		sources := make([]map[string]any, f.sources)
		for i := range sources {
			sources[i] = map[string]any{"repoURL": "https://git.example.com/shop/deploy.git"}
		}

		spec["sources"] = sources
		delete(spec, "source")
	}

	if f.hydrator {
		spec["sourceHydrator"] = map[string]any{
			"drySource":  map[string]any{"repoURL": "https://git.example.com/shop/deploy.git", "targetRevision": "main", "path": "apps"},
			"syncSource": map[string]any{"targetBranch": "env/prod", "path": "apps"},
		}
		delete(spec, "source")
	}

	if f.limit != nil {
		spec["revisionHistoryLimit"] = *f.limit
	}

	status := map[string]any{
		"sync":    map[string]any{"status": "OutOfSync", "revision": revD},
		"health":  map[string]any{"status": "Degraded"},
		"history": f.history,
		"summary": map[string]any{"images": f.images},
	}

	app := map[string]any{
		"metadata": map[string]any{"name": "checkout", "namespace": f.namespace},
		"spec":     spec,
		"status":   status,
	}

	switch {
	case f.operation != "" && f.polls > 0:
		running := map[string]any{"sync": map[string]any{"revision": f.operation}}
		app["operation"] = running
		status["operationState"] = map[string]any{"phase": "Running", "operation": running, "startedAt": "2026-11-03T10:00:00Z"}
	case f.operation != "":
		app["operation"] = map[string]any{"sync": map[string]any{"revision": f.operation}}
	case f.ended != nil:
		status["operationState"] = f.ended
	}

	return app
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(body)
}

func answerError(w http.ResponseWriter, f fakeFailure) {
	answer(w, f.status, map[string]any{"error": f.message, "code": 9, "message": f.message})
}

// newTestAction builds the action against the fake.
func newTestAction(t *testing.T, api *fakehttp.Server, change func(c *Config)) *Rollback {
	t.Helper()

	cfg := Config{
		BaseURL:     api.URL(),
		TokenSecret: tokenRef,
		Limits:      Limits{Applications: argocd.Allowlist{"shop/*"}},
	}

	if change != nil {
		change(&cfg)
	}

	a, err := New(t.Context(), "argocd-rollback", cfg, registry.Deps{Secrets: fakeSecrets{}, HTTP: api.Client()})
	require.NoError(t, err)

	rb, ok := a.(*Rollback)
	require.True(t, ok)

	rb.pollEvery = time.Millisecond
	rb.waitFor = 5 * time.Second

	return rb
}

// allowBack lets the action go back up to n revisions.
func allowBack(n int) func(c *Config) {
	return func(c *Config) { c.MaxRevisionsBack = intPtr(n) }
}

// snapshotOf takes the snapshot of params, which must work.
func snapshotOf(t *testing.T, a *Rollback, params string) action.Snapshot {
	t.Helper()

	snap, err := a.Snapshot(t.Context(), json.RawMessage(params))
	require.NoError(t, err)

	return snap
}

// invocationOf is the approved request to run params with snap.
func invocationOf(params string, snap *action.Snapshot) action.Invocation {
	return action.Invocation{
		ProposalID:     "proposal-1",
		Params:         json.RawMessage(params),
		IdempotencyKey: "key-1",
		ApprovedBy:     "user-1",
		Snapshot:       snap,
	}
}
