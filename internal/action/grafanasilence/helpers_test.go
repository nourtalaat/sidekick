package grafanasilence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/alertmanagersilence"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// proposalID is the proposal the tests run, and otherProposalID another one.
const (
	proposalID      = "0194f2c8-7d5e-7a41-9a3b-1c2d3e4f5a6b"
	otherProposalID = "0194f2c8-7d5e-7a41-9a3b-ffffffffffff"
)

// maxFakeSilences is how many silences the fake can make. Its DELETE routes
// are registered up front, because fakehttp matches paths exactly.
const maxFakeSilences = 9

// tokenKey is the key of the secret that holds the token; fakeSecrets turns
// it into the token the fake Grafana accepts.
const (
	tokenKey   = "token"
	validToken = "fake-" + tokenKey
)

// scopes are the scopes the tests allow.
var scopes = []mute.Scope{
	{Labels: map[string][]string{"alertname": {"HighLatency", "HighErrorRate"}, "service": {"checkout-*"}}},
	{Labels: map[string][]string{"grafana_folder": {"payments"}}},
}

// fakeSecrets resolves each secret to "fake-" and its key, and counts the
// calls.
type fakeSecrets struct {
	calls atomic.Int32
}

func (f *fakeSecrets) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	f.calls.Add(1)

	return []byte("fake-" + ref.Key + "\n"), nil
}

// fakeSilence is a silence of the fake Grafana.
type fakeSilence struct {
	ID        string
	Matchers  []mute.Matcher
	StartsAt  time.Time
	EndsAt    time.Time
	CreatedBy string
	Comment   string
}

// fakeAlert is an alert of the fake Grafana.
type fakeAlert struct {
	Labels map[string]string
	State  string
}

// fakeGrafana is the Alertmanager API of Grafana, as its source shows: it
// wants the bearer token, answers a POST with 202, ends a silence on DELETE
// without removing it, and lists every state.
type fakeGrafana struct {
	mu sync.Mutex

	// offset moves the fake's clock ahead of the real one.
	offset   time.Duration
	silences []*fakeSilence
	alerts   []fakeAlert
	nextID   int

	posts   []json.RawMessage
	deletes []string
	// listStatus, postStatus, alertsStatus and deleteStatus, when set, are
	// the status of the answers of the calls, each with its message.
	listStatus, postStatus, alertsStatus, deleteStatus int
	postMessage                                        string
}

// newFakeGrafana starts the fake under base (for example "" or "/grafana",
// where Grafana is served from a sub-path).
func newFakeGrafana(t *testing.T, base string) (*fakehttp.Server, *fakeGrafana) {
	t.Helper()

	api := fakehttp.New(t)
	g := &fakeGrafana{}
	root := base + apiPath

	api.HandleFunc(http.MethodGet, root+"/silences", g.guard(g.list))
	api.HandleFunc(http.MethodPost, root+"/silences", g.guard(g.post))
	api.HandleFunc(http.MethodGet, root+"/alerts", g.guard(g.listAlerts))

	for i := 1; i <= maxFakeSilences; i++ {
		id := fakeID(i)
		api.HandleFunc(http.MethodDelete, root+"/silence/"+id, g.guard(func(w http.ResponseWriter, _ *http.Request) { g.expire(w, id) }))
	}

	return api, g
}

func fakeID(n int) string {
	return fmt.Sprintf("sil-%d", n)
}

// now is the fake's time.
func (g *fakeGrafana) now() time.Time {
	return time.Now().UTC().Add(g.offset)
}

// guard refuses a request that has not the token, as Grafana does.
func (g *fakeGrafana) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+validToken {
			writeMessage(w, http.StatusUnauthorized, "Unauthorized")

			return
		}

		next(w, r)
	}
}

// add puts a silence in the fake, as if someone made it, and returns it.
func (g *fakeGrafana) add(s fakeSilence) *fakeSilence {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.nextID++
	s.ID = fakeID(g.nextID)
	g.silences = append(g.silences, &s)

	return &s
}

// edit changes the silence with id, as a person does in the Grafana UI.
func (g *fakeGrafana) edit(id string, change func(*fakeSilence)) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, s := range g.silences {
		if s.ID == id {
			change(s)
		}
	}
}

// setAlerts sets the alerts the fake lists.
func (g *fakeGrafana) setAlerts(alerts ...fakeAlert) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.alerts = alerts
}

// failWith makes the calls answer with the statuses, where zero leaves a
// call as it is.
func (g *fakeGrafana) failWith(list, post, alerts, del int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.listStatus, g.postStatus, g.alertsStatus, g.deleteStatus = list, post, alerts, del
}

// failPostWith makes the POST answer with status and message.
func (g *fakeGrafana) failPostWith(status int, message string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.postStatus, g.postMessage = status, message
}

// advance moves the fake's time on.
func (g *fakeGrafana) advance(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.offset += d
}

// stateOf is the state of the silence with id now.
func (g *fakeGrafana) stateOf(id string) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, s := range g.silences {
		if s.ID == id {
			return g.state(s)
		}
	}

	return ""
}

// posted returns the bodies of the POST calls.
func (g *fakeGrafana) posted() []json.RawMessage {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]json.RawMessage(nil), g.posts...)
}

// deleted returns the IDs that were deleted.
func (g *fakeGrafana) deleted() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), g.deletes...)
}

// state is the state of a silence at the fake's time.
func (g *fakeGrafana) state(s *fakeSilence) string {
	now := g.now()

	switch {
	case now.Before(s.StartsAt):
		return "pending"
	case !now.Before(s.EndsAt):
		return "expired"
	default:
		return "active"
	}
}

func (g *fakeGrafana) list(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.listStatus != 0 {
		writeMessage(w, g.listStatus, "list failed")

		return
	}

	out := make([]map[string]any, 0, len(g.silences))

	for _, s := range g.silences {
		matchers := make([]map[string]any, 0, len(s.Matchers))
		for _, m := range s.Matchers {
			matchers = append(matchers, map[string]any{"name": m.Name, "value": m.Value, "isRegex": m.IsRegex, "isEqual": m.IsEqual})
		}

		out = append(out, map[string]any{
			"id": s.ID, "status": map[string]string{"state": g.state(s)}, "matchers": matchers,
			"startsAt": s.StartsAt, "endsAt": s.EndsAt, "createdBy": s.CreatedBy, "comment": s.Comment, "updatedAt": g.now(),
		})
	}

	writeJSON(w, http.StatusOK, out)
}

func (g *fakeGrafana) post(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeMessage(w, http.StatusBadRequest, "bad body")

		return
	}

	g.posts = append(g.posts, body)

	if g.postStatus != 0 {
		writeMessage(w, g.postStatus, g.postMessage)

		return
	}

	var p struct {
		Matchers  []mute.Matcher `json:"matchers"`
		StartsAt  time.Time      `json:"startsAt"`
		EndsAt    time.Time      `json:"endsAt"`
		CreatedBy string         `json:"createdBy"`
		Comment   string         `json:"comment"`
	}

	if err := json.Unmarshal(body, &p); err != nil || p.CreatedBy == "" || p.Comment == "" {
		writeMessage(w, http.StatusBadRequest, "silence failed validation")

		return
	}

	switch now := g.now(); {
	case !p.StartsAt.Before(p.EndsAt):
		writeMessage(w, http.StatusBadRequest, "start time must be before end time")

		return
	case p.EndsAt.Before(now):
		writeMessage(w, http.StatusBadRequest, "end time can't be in the past")

		return
	default:
		if p.StartsAt.Before(now) {
			p.StartsAt = now
		}
	}

	g.nextID++
	id := fakeID(g.nextID)
	g.silences = append(g.silences, &fakeSilence{ID: id, Matchers: p.Matchers, StartsAt: p.StartsAt, EndsAt: p.EndsAt, CreatedBy: p.CreatedBy, Comment: p.Comment})

	writeJSON(w, http.StatusAccepted, map[string]string{"silenceID": id})
}

func (g *fakeGrafana) listAlerts(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.alertsStatus != 0 {
		writeMessage(w, g.alertsStatus, "alerts failed")

		return
	}

	out := make([]map[string]any, 0, len(g.alerts))
	for _, a := range g.alerts {
		out = append(out, map[string]any{"labels": a.Labels, "status": map[string]string{"state": a.State}, "fingerprint": "f"})
	}

	writeJSON(w, http.StatusOK, out)
}

// expire ends the silence with id, as DELETE .../silence/{id} does.
func (g *fakeGrafana) expire(w http.ResponseWriter, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.deletes = append(g.deletes, id)

	if g.deleteStatus != 0 {
		writeMessage(w, g.deleteStatus, "delete failed")

		return
	}

	for _, s := range g.silences {
		if s.ID != id {
			continue
		}

		now := g.now()

		switch g.state(s) {
		case "active":
			s.EndsAt = now
		case "pending":
			s.StartsAt, s.EndsAt = now, now
		}

		writeMessage(w, http.StatusOK, "silence deleted")

		return
	}

	writeMessage(w, http.StatusNotFound, "silence "+id+" not found")
}

func writeMessage(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

// testConfig is a valid config for the action at url.
func testConfig(url string) Config {
	return Config{
		URL:         url,
		TokenSecret: registry.SecretRef{Name: "sidekick-grafana-silence", Key: tokenKey},
		Limits:      alertmanagersilence.Limits{Scopes: scopes},
	}
}

// silencer builds the action from cfg against the fake.
func newSilencer(t *testing.T, api *fakehttp.Server, cfg Config) (action.Action, *fakeSecrets) {
	t.Helper()

	require.NoError(t, cfg.Validate())

	secrets := &fakeSecrets{}

	built, err := New(t.Context(), "mute", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
	require.NoError(t, err)

	return built, secrets
}

// snapshotter and undoer are the parts of the action the tests call.
func snapshotter(t *testing.T, a action.Action) action.Snapshotter {
	t.Helper()

	s, ok := a.(action.Snapshotter)
	require.True(t, ok)

	return s
}

func undoer(t *testing.T, a action.Action) action.Undoer {
	t.Helper()

	u, ok := a.(action.Undoer)
	require.True(t, ok)

	return u
}

// params are the parameters the model fills in.
type params struct {
	Matchers []mute.Matcher `json:"matchers"`
	Duration string         `json:"duration"`
	Comment  string         `json:"comment"`
}

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

// invocation is the Invocation Execute gets for the proposal.
func invocation(raw json.RawMessage, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: proposalID, IdempotencyKey: action.IdempotencyKey(proposalID), Params: raw, Snapshot: &snap}
}

// undoInvocation is the Invocation Undo gets for the proposal that made the
// silence.
func undoInvocation(raw json.RawMessage, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: otherProposalID, UndoOf: proposalID, Params: raw, Snapshot: &snap}
}

// snapshotAndRun takes the snapshot of the parameters and executes it.
func snapshotAndRun(t *testing.T, a action.Action, raw json.RawMessage) (action.Snapshot, action.Result) {
	t.Helper()

	snap, err := snapshotter(t, a).Snapshot(t.Context(), raw)
	require.NoError(t, err)

	res, err := a.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)

	return snap, res
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
