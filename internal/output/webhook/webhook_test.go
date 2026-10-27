package webhook

import (
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// update rewrites the golden files: go test ./internal/output/webhook -update.
var update = flag.Bool("update", false, "rewrite the golden files in fixture/golden/")

// The callback receiver of the tests.
const (
	pathHook   = "/hooks/sidekick"
	testOutput = "callback"
	testInvest = "0199c9d2-7f00-7000-8000-000000000001"
	testSecret = "callback-secret"
	// tolerance is the HMAC tolerance of intake's default config.
	tolerance = 5 * time.Minute
)

// secretRef is the secret reference of the HMAC secret in the tests.
var secretRef = registry.SecretRef{Name: "sidekick-hooks", Key: "callback"}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		wantErrs []string
	}{
		{name: "valid", cfg: Config{URL: "https://hooks.internal.example.com/sidekick", HMACSecret: secretRef}},
		{name: "http on localhost", cfg: Config{URL: "http://localhost:9000/hook", HMACSecret: secretRef}},
		{name: "empty", wantErrs: []string{"url: required", "hmacSecret: required"}},
		{name: "plain http", cfg: Config{URL: "http://hooks.internal.example.com/sidekick", HMACSecret: secretRef}, wantErrs: []string{"url: must use https"}},
		{name: "not a URL", cfg: Config{URL: "hooks.internal.example.com", HMACSecret: secretRef}, wantErrs: []string{"url: must be an absolute https URL"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase output.Phase
		want  bool
	}{
		{phase: output.PhaseQueued},
		{phase: output.PhaseRunning},
		{phase: output.PhaseFinal, want: true},
		{phase: output.PhaseWatcher, want: true},
		{phase: output.PhaseResolved},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, (&Sink{}).Accepts(tt.phase))
		})
	}
}

func TestCallbackPassesIntakeHMAC(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathHook, fakehttp.Response{Status: http.StatusNoContent})
	api.ExpectAllCalled()

	got, err := testSink(t, api).Publish(t.Context(), finalUpdate())
	require.NoError(t, err)

	req := api.Requests()[0]
	auth := intake.HMAC{Secret: []byte(testSecret), Tolerance: tolerance}
	require.NoError(t, auth.Authenticate(intake.Request{Header: req.Header, Body: req.Body, ReceivedAt: time.Now()}))

	forged := append([]byte(nil), req.Body...)
	forged[len(forged)-2] = ' '
	require.ErrorIs(t, auth.Authenticate(intake.Request{Header: req.Header, Body: forged, ReceivedAt: time.Now()}), intake.ErrUnauthenticated)

	other := intake.HMAC{Secret: []byte("another secret"), Tolerance: tolerance}
	require.ErrorIs(t, other.Authenticate(intake.Request{Header: req.Header, Body: req.Body, ReceivedAt: time.Now()}), intake.ErrUnauthenticated)

	delivery := testInvest + ":" + eventFinal + ":5"
	require.Equal(t, eventFinal, req.Header.Get(headerEvent))
	require.Equal(t, delivery, req.Header.Get(headerDelivery))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Empty(t, req.Header.Get("Authorization"))
	require.NotContains(t, string(req.Body), testSecret)

	for name, values := range req.Header {
		for _, v := range values {
			require.NotContains(t, v, testSecret, name)
		}
	}

	require.Equal(t, testOutput, got.Sink)
	require.Equal(t, delivery, got.External)
	require.Equal(t, "5", got.Data[dataSeq])
}

func TestWatcherCallback(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathHook, fakehttp.Response{Status: http.StatusOK})
	api.ExpectAllCalled()

	prev := output.Ref{Sink: testOutput, External: testInvest + ":" + eventFinal + ":5", Data: map[string]string{dataSeq: "5"}}

	u := watcherUpdate()
	u.Previous = &prev

	got, err := testSink(t, api).Publish(t.Context(), u)
	require.NoError(t, err)

	req := api.Requests()[0]
	auth := intake.HMAC{Secret: []byte(testSecret), Tolerance: tolerance}
	require.NoError(t, auth.Authenticate(intake.Request{Header: req.Header, Body: req.Body, ReceivedAt: time.Now()}))

	var body map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &body))
	require.NotContains(t, body, "report")
	require.NotContains(t, body, "status")

	delivery := testInvest + ":watcher.recovered:6"
	require.Equal(t, "watcher.recovered", req.Header.Get(headerEvent))
	require.Equal(t, delivery, req.Header.Get(headerDelivery))
	require.Equal(t, delivery, got.External)
	require.Equal(t, "6", got.Data[dataSeq])
}

func TestWatcherUpdateWithoutAWatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		previous *output.Ref
		want     output.Ref
	}{
		{
			name: "nothing delivered before",
			want: output.Ref{Sink: testOutput, Data: map[string]string{dataSeq: "6"}},
		},
		{
			name:     "after the final result",
			previous: &output.Ref{Sink: testOutput, External: "final", Data: map[string]string{dataSeq: "5", dataDeliveredAt: "1"}},
			want:     output.Ref{Sink: testOutput, External: "final", Data: map[string]string{dataSeq: "6", dataDeliveredAt: "1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			u := watcherUpdate()
			u.Watcher = nil
			u.Previous = tt.previous

			got, err := testSink(t, api).Publish(t.Context(), u)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Empty(t, api.Requests())
		})
	}
}

func TestPayloadGolden(t *testing.T) {
	t.Parallel()

	failed := finalUpdate()
	failed.Report = &report.Report{Verdict: report.Inconclusive, StopReason: report.StopError, StopDetail: "The model provider was down."}

	tests := []struct {
		name string
		u    output.Update
	}{
		{name: "final", u: finalUpdate()},
		{name: "failed", u: failed},
		{name: "watcher", u: watcherUpdate()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := newPayload(tt.u).encode()
			require.NoError(t, err)

			var indented map[string]any
			require.NoError(t, json.Unmarshal(data, &indented))

			got, err := json.MarshalIndent(indented, "", "  ")
			require.NoError(t, err)

			path := filepath.Join("fixture", "golden", tt.name+".json")
			if *update {
				require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o600))
			}

			want, err := os.ReadFile(path) //nolint:gosec // A fixture path chosen by the test.
			require.NoError(t, err, "run go test ./internal/output/webhook -update to create the golden file")
			require.JSONEq(t, string(want), string(data))
		})
	}
}

func TestPayloadEscapesHTML(t *testing.T) {
	t.Parallel()

	u := finalUpdate()
	u.Report.Reason = `<script>alert("x")</script> & more`

	data, err := newPayload(u).encode()
	require.NoError(t, err)
	require.NotContains(t, string(data), "<script>")
	require.Contains(t, string(data), `\u003cscript\u003e`)
}

func TestPublishRetriesAfterAServerError(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathHook, fakehttp.Response{Status: http.StatusServiceUnavailable}, fakehttp.Response{Status: http.StatusOK})
	api.ExpectAllCalled()

	sink := testSink(t, api)
	clock := time.Unix(1_793_000_000, 0)
	sink.now = func() time.Time { return clock }

	_, err := sink.Publish(t.Context(), finalUpdate())
	statusErr, ok := errors.AsType[*sinkhttp.StatusError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, http.StatusServiceUnavailable, statusErr.Status)

	clock = clock.Add(time.Minute)

	_, err = sink.Publish(t.Context(), finalUpdate())
	require.NoError(t, err)

	first, retry := api.Requests()[0], api.Requests()[1]
	require.Equal(t, first.Body, retry.Body)
	require.Equal(t, first.Header.Get(headerDelivery), retry.Header.Get(headerDelivery))
	require.Equal(t, strconv.FormatInt(clock.Unix(), 10), retry.Header.Get(intake.HeaderTimestamp), "the retry is signed again")
	require.NotEqual(t, first.Header.Get(intake.HeaderSignature), retry.Header.Get(intake.HeaderSignature))
}

func TestPublishFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		wantStatus int
	}{
		{name: "the receiver refuses the signature", status: http.StatusUnauthorized, wantStatus: http.StatusUnauthorized},
		{name: "the receiver fails", status: http.StatusInternalServerError, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, pathHook, fakehttp.Response{Status: tt.status, Body: []byte("echo of " + testSecret)})
			api.ExpectAllCalled()

			_, err := testSink(t, api).Publish(t.Context(), finalUpdate())
			statusErr, ok := errors.AsType[*sinkhttp.StatusError](err)
			require.True(t, ok, "got %v", err)
			require.Equal(t, tt.wantStatus, statusErr.Status)
			require.NotContains(t, err.Error(), testSecret)
		})
	}
}

func TestPublishDropsAnOlderUpdate(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	prev := output.Ref{Sink: testOutput, External: "x", Data: map[string]string{dataSeq: "7"}}

	u := finalUpdate()
	u.Previous = &prev

	got, err := testSink(t, api).Publish(t.Context(), u)
	require.NoError(t, err)
	require.Equal(t, prev, got)
	require.Empty(t, api.Requests())
}

func TestRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		otherHost   bool
		wantErr     error
		wantArrival bool
	}{
		{name: "a 307 on the same host is followed", status: http.StatusTemporaryRedirect, wantArrival: true},
		{name: "a 308 on the same host is followed", status: http.StatusPermanentRedirect, wantArrival: true},
		{name: "a 302 would drop the body", status: http.StatusFound, wantErr: errMethodChange},
		{name: "a 307 to another host is refused", status: http.StatusTemporaryRedirect, otherHost: true, wantErr: errOtherHost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			other := fakehttp.New(t)

			target := api.URL() + "/moved"
			if tt.otherHost {
				target = other.URL() + "/moved"
			}

			api.Handle(http.MethodPost, pathHook, fakehttp.Response{Status: tt.status, Headers: map[string]string{"Location": target}})

			if tt.wantArrival {
				api.Handle(http.MethodPost, "/moved", fakehttp.Response{Status: http.StatusNoContent})
			}

			api.ExpectAllCalled()

			_, err := testSink(t, api).Publish(t.Context(), finalUpdate())
			require.ErrorIs(t, err, tt.wantErr)
			require.Empty(t, other.Requests(), "the signed body never reaches another host")

			if tt.wantArrival {
				moved := api.Requests()[1]
				require.Equal(t, api.Requests()[0].Body, moved.Body)
				require.Equal(t, api.Requests()[0].Header.Get(intake.HeaderSignature), moved.Header.Get(intake.HeaderSignature))
			}
		})
	}
}

func TestTooManyRedirects(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.HandleFunc(http.MethodPost, pathHook, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, pathHook, http.StatusTemporaryRedirect)
	})

	_, err := testSink(t, api).Publish(t.Context(), finalUpdate())
	require.ErrorIs(t, err, errManyRedirects)
}

func TestSecretProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   []byte
		err     error
		wantErr error
	}{
		{name: "missing", err: os.ErrNotExist, wantErr: os.ErrNotExist},
		{name: "empty", value: []byte("\n"), wantErr: errEmptySecret},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			sec := registry.NewMockSecrets(gomock.NewController(t))
			sec.EXPECT().Resolve(gomock.Any(), secretRef).Return(tt.value, tt.err)

			sink, err := New(t.Context(), testOutput, Config{URL: api.URL() + pathHook, HMACSecret: secretRef}, registry.Deps{Secrets: sec, HTTP: api.Client()})
			require.NoError(t, err)

			_, err = sink.Publish(t.Context(), finalUpdate())
			require.ErrorIs(t, err, tt.wantErr)
			require.Empty(t, api.Requests())
		})
	}
}

func TestNewDoesNotChangeTheSharedClient(t *testing.T) {
	t.Parallel()

	shared := &http.Client{}

	_, err := New(t.Context(), testOutput, Config{}, registry.Deps{Secrets: registry.NewMockSecrets(gomock.NewController(t)), HTTP: shared})
	require.NoError(t, err)
	require.Nil(t, shared.CheckRedirect)

	_, err = New(t.Context(), testOutput, Config{}, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)
}

// testSink builds a webhook output that posts to api, with a secret whose
// file ends in a newline.
func testSink(t *testing.T, api *fakehttp.Server) *Sink {
	t.Helper()

	sec := registry.NewMockSecrets(gomock.NewController(t))
	sec.EXPECT().Resolve(gomock.Any(), secretRef).Return([]byte(testSecret+"\n"), nil).AnyTimes()

	sink, err := New(t.Context(), testOutput, Config{URL: api.URL() + pathHook, HMACSecret: secretRef},
		registry.Deps{Secrets: sec, HTTP: api.Client(), Metrics: prometheus.NewRegistry()})
	require.NoError(t, err)

	s, ok := sink.(*Sink)
	require.True(t, ok)

	return s
}

// finalUpdate is a final update with a full report.
func finalUpdate() output.Update {
	received := time.Date(2026, 10, 26, 10, 4, 0, 0, time.UTC)

	return output.Update{
		InvestigationID: testInvest,
		URL:             "https://sidekick.example.com/investigations/" + testInvest,
		Phase:           output.PhaseFinal,
		Seq:             5,
		Events:          2,
		Event: event.Event{
			Intake:      "alertmanager",
			Kind:        event.KindAlert,
			AlertName:   "CheckoutLatencyHigh",
			Title:       "checkout-api p99 latency above 2s",
			Text:        "not sent",
			Service:     "checkout-api",
			Severity:    event.SeverityCritical,
			Labels:      map[string]string{"service": "checkout-api", "env": "prod"},
			Fingerprint: "3f2a9c",
			StartsAt:    received.Add(-2 * time.Minute),
			ReceivedAt:  received,
			Links:       []event.Link{{Title: "Source", URL: "https://prometheus.example.com/graph"}},
			Raw:         json.RawMessage(`{"not":"sent"}`),
		},
		Report: &report.Report{
			Verdict:    report.TruePositive,
			Confidence: report.High,
			Reason:     "A deploy at 10:02 doubled the database calls per checkout.",
			Cause:      "Deploy 4f2a removed a query cache.",
			Impact:     "Checkout p99 is 2.8s.",
			Findings:   []report.Finding{{ID: "f1", Claim: "Calls doubled.", Evidence: []source.Evidence{{Kind: "query", Title: "DB calls", URL: "https://signoz.example.com/q/1"}}}},
			Fixes:      []report.Fix{{ID: "x1", Title: "Roll back deploy 4f2a", Detail: "Restores the cache."}},
			Checked:    []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 3}},
			StopReason: report.StopSubmitted,
		},
	}
}

// watcherUpdate is the update of a watcher that recovered after the final
// result.
func watcherUpdate() output.Update {
	u := finalUpdate()
	started := time.Date(2026, 10, 26, 10, 6, 0, 0, time.UTC)
	state := output.WatcherState{
		ID:              "0199c9d2-8a00-7000-8000-000000000002",
		Name:            "checkout-health",
		CheckType:       "http",
		Status:          output.WatcherRecovered,
		PassesInARow:    2,
		PassesToRecover: 2,
		Checks:          9,
		LastCheckAt:     started.Add(4 * time.Minute),
		LastResult:      "HTTP 200",
		StartedAt:       started,
		EndedAt:         started.Add(4 * time.Minute),
	}

	u.Phase = output.PhaseWatcher
	u.Seq = 6
	u.Watcher = &state
	u.Watchers = []output.WatcherState{state}

	return u
}
