//go:build integration

package watch

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/ids"
)

func TestTickCountsPassesInARow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statuses   []int
		passes     int
		wantStatus string
		wantChecks []string
		// wantRecoveredCheck is the index of the check whose start is the
		// recovery time.
		wantRecoveredCheck int
	}{
		{
			name:               "two passes in a row recover",
			statuses:           []int{http.StatusOK, http.StatusOK},
			passes:             2,
			wantStatus:         StatusRecovered,
			wantChecks:         []string{"pass", "pass"},
			wantRecoveredCheck: 0,
		},
		{
			name:               "a failure starts the count again",
			statuses:           []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusOK, http.StatusOK},
			passes:             2,
			wantStatus:         StatusRecovered,
			wantChecks:         []string{"pass", "fail", "pass", "pass"},
			wantRecoveredCheck: 2,
		},
		{
			name:       "fewer passes than needed keep it running",
			statuses:   []int{http.StatusOK, http.StatusOK},
			passes:     3,
			wantStatus: StatusRunning,
			wantChecks: []string{"pass", "pass"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			api := fakehttp.New(t)

			var responses []fakehttp.Response

			for _, s := range tt.statuses {
				responses = append(responses, fakehttp.Response{Status: s})
			}

			api.Handle(http.MethodGet, "/healthz", responses...)
			api.ExpectAllCalled()

			w := h.mustStart(t, httpWatcher("health", api.URL()+"/healthz", tt.passes))
			tk := h.ticker(t)
			start := h.now

			var starts []time.Time

			for range tt.statuses {
				starts = append(starts, h.now)
				require.Equal(t, 1, h.tick(t, tk), "exactly one tick is ready")

				h.now = h.now.Add(testInterval)
			}

			got := h.watcher(t, w.ID)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Len(t, h.checks(t, w.ID), int(got.Checks))
			require.Len(t, tt.statuses, int(got.Checks))

			var results []string

			for _, c := range h.checks(t, w.ID) {
				results = append(results, c.Result)
			}

			require.Equal(t, tt.wantChecks, results)

			if tt.wantStatus == StatusRunning {
				pending := h.pendingTicks(t)
				require.Len(t, pending, 1)
				require.True(t, start.Add(time.Duration(len(tt.statuses))*testInterval).Equal(pending[0].RunAt), "the next tick is one interval after the last check")
				require.Equal(t, TickKey(w.ID), *pending[0].UniqueKey)
				require.Empty(t, h.closed())

				return
			}

			require.Empty(t, h.pendingTicks(t), "a recovered watcher has no next tick")
			require.NotNil(t, got.RecoveredAt)
			require.True(t, starts[tt.wantRecoveredCheck].Equal(*got.RecoveredAt), "recovery is the start of the passing run")
			require.Equal(t, []uuid.UUID{checkoutGroup}, h.closed())
		})
	}
}

func TestTickEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// prepare changes the harness after the watcher started.
		prepare        func(t *testing.T, h *harness)
		watcher        func(url string) NewWatcher
		wantStatus     string
		wantStopReason *string
		wantChecks     int
		wantClose      bool
	}{
		{
			name:       "the deadline passed",
			watcher:    func(url string) NewWatcher { return httpWatcher("health", url, 2) },
			prepare:    func(_ *testing.T, h *harness) { h.now = h.now.Add(testTimeout) },
			wantStatus: StatusTimedOut,
			wantClose:  true,
		},
		{
			name:    "the route no longer allows the model's host",
			watcher: func(url string) NewWatcher { return modelWatcher(httpWatcher("probe", url, 2)) },
			prepare: func(_ *testing.T, h *harness) {
				r := h.policy.Routes["default"]
				r.Hosts = []string{"status.example.com"}
				h.policy.Routes = map[string]RoutePolicy{"default": r}
			},
			wantStatus:     StatusStopped,
			wantStopReason: new(StopConfigChanged),
			wantClose:      true,
		},
		{
			name:    "the route no longer defines the watcher",
			watcher: func(url string) NewWatcher { return httpWatcher("health", url, 2) },
			prepare: func(_ *testing.T, h *harness) {
				r := h.policy.Routes["default"]
				r.Definitions = nil
				h.policy.Routes = map[string]RoutePolicy{"default": r}
			},
			wantStatus:     StatusStopped,
			wantStopReason: new(StopConfigChanged),
			wantClose:      true,
		},
		{
			name:    "the watcher was stopped meanwhile",
			watcher: func(url string) NewWatcher { return httpWatcher("health", url, 2) },
			prepare: func(t *testing.T, h *harness) {
				t.Helper()

				_, err := h.pool.Exec(t.Context(), "UPDATE watchers SET status = 'stopped', stop_reason = 'responder'")
				require.NoError(t, err)
			},
			wantStatus:     StatusStopped,
			wantStopReason: new(StopResponder),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			api := fakehttp.New(t)

			w := h.mustStart(t, tt.watcher(api.URL()+"/healthz"))
			tt.prepare(t, h)

			require.Equal(t, 1, h.tick(t, h.ticker(t)))

			got := h.watcher(t, w.ID)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantStopReason, got.StopReason)
			require.Len(t, h.checks(t, w.ID), tt.wantChecks, "no check runs")
			require.Empty(t, h.pendingTicks(t))
			require.Empty(t, api.Requests())

			if tt.wantClose {
				require.Equal(t, []uuid.UUID{checkoutGroup}, h.closed())
			} else {
				require.Empty(t, h.closed())
			}
		})
	}
}

func TestTickAfterAnOutage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// missed is how many intervals Sidekick was down for.
		missed int
	}{
		{name: "ten missed checks", missed: 10},
		{name: "one missed check", missed: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/healthz", fakehttp.Response{Status: http.StatusServiceUnavailable}, fakehttp.Response{Status: http.StatusServiceUnavailable})
			api.ExpectAllCalled()

			w := h.mustStart(t, httpWatcher("health", api.URL()+"/healthz", 2))
			require.Equal(t, 1, h.tick(t, h.ticker(t)))

			// Every replica was down; the next one starts with a new ticker,
			// long after the tick was due.
			h.now = h.now.Add(time.Duration(tt.missed) * testInterval)
			require.Equal(t, 1, h.tick(t, h.ticker(t)), "one tick runs, not one per missed check")

			require.Len(t, h.checks(t, w.ID), 2)

			pending := h.pendingTicks(t)
			require.Len(t, pending, 1)
			require.True(t, h.now.Add(testInterval).Equal(pending[0].RunAt), "the next check is one interval after the late one")
		})
	}
}

func TestTickAlertCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		resolve    bool
		wantStatus string
	}{
		{name: "the alert is still firing", wantStatus: StatusRunning},
		{name: "the alert recovered: one pass is enough", resolve: true, wantStatus: StatusRecovered},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			w := h.mustStart(t, NewWatcher{
				GroupID: checkoutGroup, InvestigationID: checkoutInvestigation, Name: "cleared", Origin: OriginModel,
				Check: AlertCheck{}, Settings: Settings{Interval: testInterval, Timeout: testTimeout, PassesToRecover: 3},
			})
			require.Equal(t, int32(1), w.PassesToRecover)

			if tt.resolve {
				_, err := h.pool.Exec(t.Context(), `INSERT INTO events (id, intake, key, kind, trusted, labels, fingerprint, status, group_id, received_at)
					VALUES ($1, 'alertmanager', 'checkout-1-resolved', 'resolved', true, '{}', 'alert-checkout', 'grouped', $2, '2026-10-26 09:20:00+00')`,
					ids.New(), checkoutGroup)
				require.NoError(t, err)
			}

			require.Equal(t, 1, h.tick(t, h.ticker(t)))
			require.Equal(t, tt.wantStatus, h.watcher(t, w.ID).Status)
		})
	}
}

// modelWatcher returns w as started by the model.
func modelWatcher(w NewWatcher) NewWatcher {
	w.Origin = OriginModel

	return w
}
