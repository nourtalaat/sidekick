package board

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
)

// tokenRef names the board's token in the tests.
var tokenRef = registry.SecretRef{Name: "board", Key: "token"}

func TestPublish(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		update   output.Update
		answer   string
		method   string
		path     string
		wantCard card
		wantRef  output.Ref
	}{
		{
			name:     "the first update creates the card",
			update:   update(output.PhaseRunning, 1, nil, nil),
			answer:   `{"id": "card-7"}`,
			method:   http.MethodPost,
			path:     "/cards",
			wantCard: card{Title: "Checkout is slow", Status: "investigating", Text: "Checked recent deploys"},
			wantRef:  output.Ref{Sink: "board", External: "card-7", Data: map[string]string{dataSeq: "1"}},
		},
		{
			name:     "a later update replaces it",
			update:   update(output.PhaseFinal, 2, shown(1), &report.Report{Verdict: report.TruePositive, Reason: "Deploy 4f2c1a broke it."}),
			answer:   `{"id": "card-7"}`,
			method:   http.MethodPut,
			path:     "/cards/card-7",
			wantCard: card{Title: "Checkout is slow", Status: "true_positive", Text: "Deploy 4f2c1a broke it."},
			wantRef:  output.Ref{Sink: "board", External: "card-7", Data: map[string]string{dataSeq: "2"}},
		},
		{
			name:     "a run that could not finish",
			update:   update(output.PhaseFinal, 2, shown(1), nil),
			answer:   `{"id": "card-7"}`,
			method:   http.MethodPut,
			path:     "/cards/card-7",
			wantCard: card{Title: "Checkout is slow", Status: "failed", Text: "Could not finish"},
			wantRef:  output.Ref{Sink: "board", External: "card-7", Data: map[string]string{dataSeq: "2"}},
		},
		{
			name:    "an older update is dropped",
			update:  update(output.PhaseRunning, 1, shown(3), nil),
			wantRef: *shown(3),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if tt.method != "" {
				api.Handle(tt.method, tt.path, fakehttp.Response{Body: []byte(tt.answer)})
			}

			api.ExpectAllCalled()

			s := newTestSink(t, api)

			got, err := s.Publish(t.Context(), tt.update)
			require.NoError(t, err)
			require.Equal(t, tt.wantRef, got)

			if tt.method == "" {
				require.Empty(t, api.Requests())

				return
			}

			req := api.Requests()[0]
			require.Equal(t, "Bearer fake-token", req.Header.Get("Authorization"))

			var sent card
			require.NoError(t, json.Unmarshal(req.Body, &sent))
			require.Equal(t, tt.wantCard, sent)
		})
	}
}

func TestPublishFailure(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, "/cards", fakehttp.Response{Status: http.StatusServiceUnavailable})
	api.ExpectAllCalled()

	_, err := newTestSink(t, api).Publish(t.Context(), update(output.PhaseRunning, 1, nil, nil))
	require.ErrorIs(t, err, errHTTPStatus)
}

func TestAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase output.Phase
		want  bool
	}{
		{phase: output.PhaseQueued},
		{phase: output.PhaseRunning, want: true},
		{phase: output.PhaseFinal, want: true},
		{phase: output.PhaseWatcher},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, (&Sink{}).Accepts(tt.phase))
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr []error
	}{
		{name: "valid", cfg: Config{URL: "https://board.example.com/api", TokenSecret: tokenRef}},
		{name: "no URL", cfg: Config{TokenSecret: tokenRef}, wantErr: []error{errBadURL}},
		{name: "a relative URL", cfg: Config{URL: "/api", TokenSecret: tokenRef}, wantErr: []error{errBadURL}},
		{name: "no token", cfg: Config{URL: "https://board.example.com/api"}, wantErr: []error{errNoToken}},
		{name: "both", wantErr: []error{errBadURL, errNoToken}},
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

// fakeSecrets resolves every reference to a fake token.
type fakeSecrets struct{}

func (fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte("fake-token\n"), nil
}

// newTestSink returns a sink that talks to api.
func newTestSink(t *testing.T, api *fakehttp.Server) output.Sink {
	t.Helper()

	s, err := New(t.Context(), "board", Config{URL: api.URL() + "/", TokenSecret: tokenRef}, registry.Deps{Secrets: fakeSecrets{}, HTTP: api.Client()})
	require.NoError(t, err)

	return s
}

// update returns an update of phase with seq, the ref prev and the report r.
func update(phase output.Phase, seq int64, prev *output.Ref, r *report.Report) output.Update {
	return output.Update{
		Event:    event.Event{Title: "Checkout is slow"},
		Phase:    phase,
		Seq:      seq,
		Previous: prev,
		Progress: []output.Progress{{Text: "Read the alert"}, {Text: "Checked recent deploys"}},
		Report:   r,
	}
}

// shown is the ref of card-7 showing seq.
func shown(seq int64) *output.Ref {
	return &output.Ref{Sink: "board", External: "card-7", Data: map[string]string{dataSeq: strconv.FormatInt(seq, 10)}}
}
