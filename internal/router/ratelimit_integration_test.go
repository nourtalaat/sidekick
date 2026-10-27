//go:build integration

package router

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// rateEvents is how many events the rate limit tests send: one over the
// limit of rateLimitEvents an hour.
const (
	rateEvents      = 61
	rateLimitEvents = 60
)

// rateNow is when the rate limit tests route their events.
var rateNow = time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)

// TestRateLimit sends 61 events at once to a route limited to 60 an hour
// and checks that exactly one is held, with one notice, and that a delayed
// event is routed once the window has room.
func TestRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// route is the billing route's limit, and global budgets.rateLimit.
		route, global *config.RateLimit
		status        string
		wantNotice    string
	}{
		{
			name:       "the route's limit delays",
			route:      &config.RateLimit{Events: rateLimitEvents, Per: time.Hour, OverLimit: config.OverLimitDelay},
			status:     statusDelayed,
			wantNotice: "Route billing received 60 events in the last 1 hour, the most its rate limit allows. Events over the limit are delayed and investigated once the window has room.",
		},
		{
			name:       "the route's limit skips",
			route:      &config.RateLimit{Events: rateLimitEvents, Per: time.Hour, OverLimit: config.OverLimitSkip},
			status:     statusSkipped,
			wantNotice: "Route billing received 60 events in the last 1 hour, the most its rate limit allows. Events over the limit are skipped and not investigated.",
		},
		{
			name:       "the global limit delays",
			route:      &config.RateLimit{Events: 100, Per: time.Hour, OverLimit: config.OverLimitSkip},
			global:     &config.RateLimit{Events: rateLimitEvents, Per: time.Hour, OverLimit: config.OverLimitDelay},
			status:     statusDelayed,
			wantNotice: "All routes together received 60 events in the last 1 hour, the most its rate limit allows. Events over the limit are delayed and investigated once the window has room.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setupRateLimit(t, tt.route, tt.global)
			ids := h.billingEvents(t, rateEvents)

			jobs := h.claimRouteJobs(t, ids)
			require.Len(t, jobs, rateEvents)

			var wg sync.WaitGroup
			for _, job := range jobs {
				wg.Go(func() {
					require.NoError(t, h.router.Handle(t.Context(), job))
				})
			}

			wg.Wait()

			statuses := h.statuses(t, ids)
			require.Equal(t, rateLimitEvents, statuses["grouped"])
			require.Equal(t, 1, statuses[tt.status], "one event is held")
			require.Len(t, h.investigations(t), rateLimitEvents)

			held := h.heldEvent(t, tt.status)
			require.Equal(t, "billing", deref(held.Route))
			require.Contains(t, deref(held.SkipReason), "over the rate limit of ")

			require.Equal(t, []string{tt.wantNotice}, h.rateNotices(t))

			skipped := testutil.ToFloat64(h.router.skippedEvents.WithLabelValues("billing", skipReasonRateLimit))
			if tt.status == statusSkipped {
				require.InDelta(t, 1, skipped, 0)
				require.Empty(t, h.pendingRouteJobs(t))

				return
			}

			require.InDelta(t, 0, skipped, 0)
			require.Equal(t, []time.Time{rateNow.Add(time.Hour)}, h.pendingRouteJobs(t), "the event waits until the oldest leaves the window")

			h.router.now = func() time.Time { return rateNow.Add(time.Hour) }
			_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() WHERE kind = 'route_event' AND status = 'pending'")
			require.NoError(t, err)

			later := h.claimRouteJobs(t, []uuid.UUID{held.ID})
			require.Len(t, later, 1)
			require.NoError(t, h.router.Handle(t.Context(), later[0]))

			ev := h.event(t, held.ID)
			require.Equal(t, "grouped", ev.Status)
			require.Nil(t, ev.SkipReason)
			require.Len(t, h.investigations(t), rateEvents)
			require.Len(t, h.rateNotices(t), 1, "one notice per window")
		})
	}
}

// TestRateLimitRetry checks that a route_event job that runs again after
// its event was admitted does not count the event twice.
func TestRateLimitRetry(t *testing.T) {
	t.Parallel()

	h := setupRateLimit(t, &config.RateLimit{Events: 1, Per: time.Hour, OverLimit: config.OverLimitSkip}, nil)
	ids := h.billingEvents(t, 1)
	job := h.claimRouteJobs(t, ids)[0]

	err := db.New(h.pool).AdmitEvent(t.Context(), db.AdmitEventParams{Scope: "route:billing", EventID: ids[0], AdmittedAt: rateNow})
	require.NoError(t, err)

	require.NoError(t, h.router.Handle(t.Context(), job))
	require.Equal(t, "grouped", h.event(t, ids[0]).Status)
	require.Empty(t, h.rateNotices(t))
}

// setupRateLimit is setup with route as the billing route's rate limit,
// global as budgets.rateLimit, the Slack output on the billing route and
// the clock stopped at rateNow.
func setupRateLimit(t *testing.T, route, global *config.RateLimit) *harness {
	t.Helper()

	cfg, instances := loadConfig(t)
	cfg.Budgets.RateLimit = global

	for i := range cfg.Routes {
		if cfg.Routes[i].Name == "billing" {
			cfg.Routes[i].RateLimit = route
			cfg.Routes[i].Outputs = []string{"slack-thread"}
		}
	}

	pool := storetest.New(t)

	r, err := New(Options{
		Config:        cfg,
		ModelSettings: instances.ModelSettings,
		Pool:          pool,
		Logger:        slog.New(slog.DiscardHandler),
		Metrics:       prometheus.NewRegistry(),
	})
	require.NoError(t, err)

	r.now = func() time.Time { return rateNow }

	return &harness{pool: pool, router: r}
}

// billingEvents stores n received events that fit the billing route, a
// second apart, each with its own fingerprint so that each opens a group,
// and returns their IDs in order.
func (h *harness) billingEvents(t *testing.T, n int) []uuid.UUID {
	t.Helper()

	ids := make([]uuid.UUID, 0, n)

	for i := range n {
		id := uuid.New()
		ids = append(ids, id)

		_, err := h.pool.Exec(t.Context(), `
INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, severity, labels, fingerprint, status, received_at)
VALUES ($1, 'alertmanager', $2, 'alert', true, 'InvoiceLag', 'Invoices are late', 'warning', '{"team": "billing"}',
        $3, 'received', $4)`, id, id.String(), fmt.Sprintf("fp-billing-%d", i), rateNow.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
	}

	return ids
}

// claimRouteJobs enqueues the route_event job of each event, with the
// unique key intake gives it, and claims them as a worker does.
func (h *harness) claimRouteJobs(t *testing.T, ids []uuid.UUID) []db.Job {
	t.Helper()

	for _, id := range ids {
		payload, err := json.Marshal(routeEventPayload{EventID: id})
		require.NoError(t, err)

		_, err = queue.Enqueue(t.Context(), h.pool, queue.NewJob{Kind: queue.KindRouteEvent, Payload: payload, UniqueKey: "route_event:" + id.String()})
		if err != nil {
			require.ErrorIs(t, err, queue.ErrDuplicate, "the delayed job is pending already")
		}
	}

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: time.Minute.Seconds(), Kinds: []string{queue.KindRouteEvent}, Batch: rateEvents,
	})
	require.NoError(t, err)

	return jobs
}

// statuses counts the events with ids by status.
func (h *harness) statuses(t *testing.T, ids []uuid.UUID) map[string]int {
	t.Helper()

	out := map[string]int{}

	for _, id := range ids {
		out[h.event(t, id).Status]++
	}

	return out
}

// heldEvent returns the one event with status.
func (h *harness) heldEvent(t *testing.T, status string) db.Event {
	t.Helper()

	held := collect[db.Event](t, h.pool, "SELECT * FROM events WHERE status = '"+status+"'")
	require.Len(t, held, 1)

	return held[0]
}

// rateNotices returns the text of each rate limit notice job.
func (h *harness) rateNotices(t *testing.T) []string {
	t.Helper()

	var out []string

	for _, j := range collect[struct{ Payload json.RawMessage }](t, h.pool, "SELECT payload FROM jobs WHERE kind = 'notice' ORDER BY id") {
		var p struct {
			Sink   string `json:"sink"`
			Notice struct {
				Kind, Title, Text string
			} `json:"notice"`
		}

		require.NoError(t, json.Unmarshal(j.Payload, &p))
		require.Equal(t, "slack-thread", p.Sink)
		require.Equal(t, "rate_limit", p.Notice.Kind)
		require.Equal(t, noticeTitleRateLimit, p.Notice.Title)

		out = append(out, p.Notice.Text)
	}

	return out
}

// pendingRouteJobs returns the run_at of each pending route_event job.
func (h *harness) pendingRouteJobs(t *testing.T) []time.Time {
	t.Helper()

	var out []time.Time

	for _, j := range collect[struct{ RunAt time.Time }](t, h.pool, "SELECT run_at FROM jobs WHERE kind = 'route_event' AND status = 'pending' ORDER BY id") {
		out = append(out, j.RunAt.UTC())
	}

	return out
}
