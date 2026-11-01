package note

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// tokenRef names the service's token in the tests.
var tokenRef = registry.SecretRef{Name: "notes", Key: "token"}

// fakeSecrets resolves every reference to a fake token.
type fakeSecrets struct{}

func (fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte("fake-token\n"), nil
}

// newTestNote returns an action that talks to api, with the limits given.
func newTestNote(t *testing.T, api *fakehttp.Server, limits Limits) action.Action {
	t.Helper()

	a, err := New(t.Context(), "monitor-note", Config{URL: api.URL() + "/", TokenSecret: tokenRef, Limits: limits}, registry.Deps{Secrets: fakeSecrets{}, HTTP: api.Client()})
	require.NoError(t, err)

	return a
}

// invocation is an approved request to write the note.
func invocation(params string) action.Invocation {
	return action.Invocation{
		ProposalID:      "0192d3a4-0000-7000-8000-0000000000f1",
		InvestigationID: "0192d3a4-0000-7000-8000-0000000000e2",
		Params:          json.RawMessage(params),
		IdempotencyKey:  "sidekick/0192d3a4-0000-7000-8000-0000000000f1",
		ApprovedBy:      "u1",
	}
}

const goodParams = `{"board": "ops", "title": "Slow checkout", "body": "p99 doubled after the deploy."}`

var opsOnly = Limits{Boards: []string{"ops"}, MaxBodyChars: 100}

func TestExecute(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, "/boards/ops/notes", fakehttp.Response{Status: http.StatusCreated, Body: []byte(`{"id": "n-7", "url": "https://notes.example.com/n/7"}`)})
	api.ExpectAllCalled()

	got, err := newTestNote(t, api, opsOnly).Execute(t.Context(), invocation(goodParams))
	require.NoError(t, err)
	require.Equal(t, action.Result{
		Summary: `Wrote the note "Slow checkout" on board ops.`,
		Links:   []action.Link{{Title: "Note n-7", URL: "https://notes.example.com/n/7"}},
	}, got)

	req := api.Requests()[0]
	require.Equal(t, "Bearer fake-token", req.Header.Get("Authorization"))
	require.Equal(t, "sidekick/0192d3a4-0000-7000-8000-0000000000f1", req.Header.Get(idempotencyHeader))
	require.JSONEq(t, `{"title": "Slow checkout", "body": "p99 doubled after the deploy."}`, string(req.Body))
}

// TestExecuteTwiceWithTheSameKey is the crash-and-retry case: Sidekick
// calls Execute again with the same idempotency key, and the service, which
// keeps the notes by key, makes one note.
func TestExecuteTwiceWithTheSameKey(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		notes = map[string]string{}
	)

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodPost, "/boards/ops/notes", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		key := r.Header.Get(idempotencyHeader)
		id, repeated := notes[key]

		if !repeated {
			id = fmt.Sprintf("n-%d", len(notes)+1)
			notes[key] = id
		}

		if repeated {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusCreated)
		}

		_, _ = fmt.Fprintf(w, `{"id": %q, "url": "https://notes.example.com/n/%s"}`, id, id)
	})

	a := newTestNote(t, api, opsOnly)

	first, err := a.Execute(t.Context(), invocation(goodParams))
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), invocation(goodParams))
	require.NoError(t, err)

	require.Equal(t, first, second, "the repeat finds the note")

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, notes, 1, "and the service made it once")
}

func TestExecuteFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		params        string
		limits        Limits
		wantPermanent bool
		wantLimit     bool
		wantCalls     int
	}{
		{name: "a refused note will not be accepted on a repeat", status: http.StatusBadRequest, params: goodParams, limits: opsOnly, wantPermanent: true, wantCalls: 1},
		{name: "a board that is gone", status: http.StatusNotFound, params: goodParams, limits: opsOnly, wantPermanent: true, wantCalls: 1},
		{name: "a server error may pass", status: http.StatusBadGateway, params: goodParams, limits: opsOnly, wantCalls: 1},
		{name: "a rate limit may pass", status: http.StatusTooManyRequests, params: goodParams, limits: opsOnly, wantCalls: 1},
		{name: "limits tightened since the approval", params: goodParams, limits: Limits{Boards: []string{"billing"}}, wantLimit: true, wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if tt.status != 0 {
				api.Handle(http.MethodPost, "/boards/ops/notes", fakehttp.Response{Status: tt.status})
			}

			_, err := newTestNote(t, api, tt.limits).Execute(t.Context(), invocation(tt.params))
			require.Error(t, err)
			require.Len(t, api.Requests(), tt.wantCalls)

			// Sidekick fails a proposal at once for these errors, and tries
			// again for any other.
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.Equal(t, tt.wantLimit, isLimit(err))
		})
	}
}

func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent) || errors.Is(err, action.ErrLimit) || errors.Is(err, action.ErrConflict)
}

func isLimit(err error) bool {
	return errors.Is(err, action.ErrLimit)
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		limits  Limits
		wantErr string
	}{
		{name: "inside the limits", params: goodParams, limits: opsOnly},
		{name: "a board that is not allowed", params: `{"board": "finance", "title": "t"}`, limits: opsOnly, wantErr: `board "finance"`},
		{name: "a body that is too long", params: `{"board": "ops", "title": "t", "body": "` + repeat("x", 101) + `"}`, limits: opsOnly, wantErr: "101 characters"},
		{name: "a body at the default limit", params: `{"board": "ops", "title": "t", "body": "` + repeat("x", defaultMaxBodyChars) + `"}`, limits: Limits{Boards: []string{"ops"}}},
		{name: "text that is not JSON", params: `[`, limits: opsOnly, wantErr: "decode the parameters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := newTestNote(t, fakehttp.New(t), tt.limits)

			err := a.Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	spec := newTestNote(t, fakehttp.New(t), Limits{Boards: []string{"ops", "billing"}}).Spec()

	require.Equal(t, "monitor-note", spec.Name)
	require.Contains(t, spec.Description, "Allowed boards: ops, billing.")
	require.True(t, json.Valid(spec.ParamsSchema))
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr []error
	}{
		{name: "valid", cfg: Config{URL: "https://notes.example.com", TokenSecret: tokenRef, Limits: opsOnly}},
		{name: "http on loopback", cfg: Config{URL: "http://127.0.0.1:8080", TokenSecret: tokenRef, Limits: opsOnly}},
		{name: "http elsewhere", cfg: Config{URL: "http://notes.example.com", TokenSecret: tokenRef, Limits: opsOnly}, wantErr: []error{errBadURL}},
		{name: "a relative URL", cfg: Config{URL: "/notes", TokenSecret: tokenRef, Limits: opsOnly}, wantErr: []error{errBadURL}},
		{name: "no token", cfg: Config{URL: "https://notes.example.com", Limits: opsOnly}, wantErr: []error{errNoToken}},
		{name: "no boards", cfg: Config{URL: "https://notes.example.com", TokenSecret: tokenRef}, wantErr: []error{errNoBoards}},
		{name: "a negative limit", cfg: Config{URL: "https://notes.example.com", TokenSecret: tokenRef, Limits: Limits{Boards: []string{"ops"}, MaxBodyChars: -1}}, wantErr: []error{errNegative}},
		{name: "everything", wantErr: []error{errBadURL, errNoToken, errNoBoards}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErr {
				require.ErrorIs(t, err, want)
			}
		})
	}
}

// TestRegistered builds the action the way the config loader does, from a
// YAML section, through the registry the init function registered it in.
func TestRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), Type)

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(`
url: https://notes.example.com
tokenSecret: { name: notes, key: token }
limits: { boards: [ops] }
`), &doc))

	a, err := registry.Actions.Build(t.Context(), Type, "monitor-note", doc.Content[0], registry.Deps{Secrets: fakeSecrets{}})
	require.NoError(t, err)
	require.Equal(t, "monitor-note", a.Spec().Name)
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}

	return string(out)
}
