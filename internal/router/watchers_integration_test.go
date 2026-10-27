//go:build integration

package router

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// checkoutDown is a "service down" alert that the service-down route of
// fixture/watchers.yaml takes.
const checkoutDown = "fixture/alertmanager/checkout-down.json"

func TestServiceDownAlertStartsItsWatcher(t *testing.T) {
	t.Parallel()

	afterTheWindow := time.Now().Add(config.DefaultGroupingWindow + time.Minute)

	tests := []struct {
		name string
		// closeBetween runs the close_group jobs after the window between
		// the first and the second check.
		closeBetween bool
	}{
		{name: "two passing checks recover the watcher"},
		{name: "the window ends while the watcher runs", closeBetween: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/healthz", fakehttp.Response{Body: []byte("ok")}, fakehttp.Response{Body: []byte("ok")})
			api.ExpectAllCalled()

			cfg, instances := loadWatcherConfig(t, api.URL()+"/healthz")
			h := setupWith(t, cfg, instances)

			h.deliver(t, checkoutDown)
			h.routeAll(t)

			watchers := collect[db.Watcher](t, h.pool, "SELECT * FROM watchers")
			require.Len(t, watchers, 1, "only the watcher whose match fits starts")
			require.Equal(t, "checkout-health", watchers[0].Name)
			require.Equal(t, string(watch.OriginRoute), watchers[0].Origin)
			require.Equal(t, int32(10), watchers[0].IntervalS)
			require.Equal(t, int32(2), watchers[0].PassesToRecover)

			groupID := watchers[0].GroupID
			ticker := newTicker(t, h, cfg)

			require.Equal(t, 1, runTicks(t, h, ticker), "the first check runs at once")
			require.Equal(t, watch.StatusRunning, watcherByID(t, h, watchers[0]).Status)

			if tt.closeBetween {
				h.closeGroups(t, afterTheWindow)
				require.Equal(t, "open", groupStatus(t, h, groupID), "the watcher keeps the group open")
			}

			_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() WHERE kind = 'watch_tick' AND status = 'pending'")
			require.NoError(t, err)
			require.Equal(t, 1, runTicks(t, h, ticker))

			got := watcherByID(t, h, watchers[0])
			require.Equal(t, watch.StatusRecovered, got.Status)
			require.Equal(t, int32(2), got.Checks)

			h.closeGroups(t, afterTheWindow)
			require.Equal(t, "closed", groupStatus(t, h, groupID), "the group closes once its last watcher ended")
		})
	}
}

// loadWatcherConfig loads fixture/watchers.yaml with healthURL as the URL
// of its HTTP check, and builds its plugins.
func loadWatcherConfig(t *testing.T, healthURL string) (*config.Config, *config.Instances) {
	t.Helper()

	data, err := os.ReadFile("fixture/watchers.yaml")
	require.NoError(t, err)

	regs := config.Registries{Models: fakeModels()}

	cfg, err := config.Parse(t.Context(), []byte(strings.ReplaceAll(string(data), "HEALTH_URL", healthURL)), config.LoadOptions{Registries: regs})
	require.NoError(t, err)

	in, err := config.Build(t.Context(), cfg, regs, registry.Deps{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, in.Close()) })

	return cfg, in
}

// newTicker returns a watch Ticker for the config, as sidekick serve
// builds it.
func newTicker(t *testing.T, h *harness, cfg *config.Config) *watch.Ticker {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	guard, err := tools.New(nil, tools.Options{Redactor: redactor, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	client, err := safehttp.New(safehttp.Options{AllowHosts: cfg.Watch.HTTP.AllowHosts, AllowPrivate: cfg.Watch.HTTP.AllowPrivate})
	require.NoError(t, err)

	checker, err := watch.New(watch.Options{Guard: guard, HTTP: client, Redactor: redactor, Alerts: watch.StoreAlerts{DB: h.pool}})
	require.NoError(t, err)

	ticker, err := watch.NewTicker(watch.TickerOptions{
		Pool:       h.pool,
		Checker:    checker,
		Policy:     cfg.WatchPolicy(),
		CloseGroup: grouper.EnqueueClose,
		Logger:     slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	return ticker
}

// runTicks claims the ready watch_tick jobs and runs each, as the workers
// do. It returns how many ran.
func runTicks(t *testing.T, h *harness, ticker *watch.Ticker) int {
	t.Helper()

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindWatchTick}, Batch: 100,
	})
	require.NoError(t, err)

	for _, job := range jobs {
		require.NoError(t, ticker.Handle(t.Context(), job))

		_, err = db.New(h.pool).CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
		require.NoError(t, err)
	}

	return len(jobs)
}

// watcherByID returns w as it is stored now.
func watcherByID(t *testing.T, h *harness, w db.Watcher) db.Watcher {
	t.Helper()

	got, err := db.New(h.pool).GetWatcher(t.Context(), w.ID)
	require.NoError(t, err)

	return got
}

// groupStatus returns the status of the group with id.
func groupStatus(t *testing.T, h *harness, id uuid.UUID) string {
	t.Helper()

	for _, g := range h.groups(t) {
		if g.ID == id {
			return g.Status
		}
	}

	require.Failf(t, "no such group", "group %s", id)

	return ""
}
