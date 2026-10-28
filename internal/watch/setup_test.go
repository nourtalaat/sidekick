//go:build integration

package watch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The rows of fixture/groups.sql.
var (
	checkoutGroup         = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000b1")
	checkoutInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	otherGroup            = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000b2")
)

// testInterval and testTimeout are the settings of the test watchers.
const (
	testInterval = 30 * time.Second
	testTimeout  = time.Hour
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Ticker on a database of its own, seeded with
// fixture/groups.sql, with a clock the test moves and a record of the
// groups whose close_group the watchers asked for.
type harness struct {
	pool     *pgxpool.Pool
	policy   Policy
	checker  *Checker
	reg      *prometheus.Registry
	recorder *Recorder
	now      time.Time

	mu     sync.Mutex
	closes []uuid.UUID
}

func setup(t *testing.T) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/groups.sql")

	reg := prometheus.NewRegistry()

	recorder, err := NewRecorder(slog.New(slog.DiscardHandler), reg)
	require.NoError(t, err)

	return &harness{
		pool:     pool,
		policy:   testPolicy(),
		checker:  newChecker(t, testDeps{allowHosts: []string{loopback}, allowPrivate: []string{loopback}, opts: Options{Alerts: StoreAlerts{DB: pool}}}),
		reg:      reg,
		recorder: recorder,
		now:      time.Now().UTC().Truncate(time.Second),
	}
}

// testPolicy allows every check type on the default route, HTTP checks
// to loopback, and defines the route watcher named health.
func testPolicy() Policy {
	return Policy{
		MaxPerGroup: DefaultMaxPerGroup,
		MaxTotal:    DefaultMaxTotal,
		Defaults:    Level{Path: "watch.defaults", Settings: Settings{Interval: testInterval, Timeout: testTimeout, PassesToRecover: 2, CheckTimeout: 5 * time.Second}},
		Bounds:      Bounds{Path: "watch", MinInterval: time.Second, MaxInterval: time.Hour, MaxTimeout: 4 * time.Hour},
		Routes: map[string]RoutePolicy{"default": {
			Path:        "routes[0]",
			AllowModel:  true,
			Types:       []Type{TypeHTTP, TypeTool, TypeAlert},
			Hosts:       []string{loopback},
			Definitions: []Definition{{Name: "health", Origin: OriginRoute, Check: HTTPCheck{URL: "http://127.0.0.1/healthz"}}},
		}},
	}
}

// ticker returns a Ticker on the harness's clock and policy.
func (h *harness) ticker(t *testing.T) *Ticker {
	t.Helper()

	tk, err := NewTicker(TickerOptions{
		Pool:       h.pool,
		Checker:    h.checker,
		Policy:     h.policy,
		CloseGroup: h.closeGroup,
		Recorder:   h.recorder,
		Logger:     slog.New(slog.DiscardHandler),
		Now:        func() time.Time { return h.now },
	})
	require.NoError(t, err)

	return tk
}

// closeGroup records that a watcher asked for the close_group of the
// group with groupID.
func (h *harness) closeGroup(_ context.Context, _ db.DBTX, groupID uuid.UUID) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closes = append(h.closes, groupID)

	return nil
}

// start starts w in a transaction that locks its group, as the router and
// start_watcher do.
func (h *harness) start(t *testing.T, w NewWatcher) (db.Watcher, error) {
	t.Helper()

	var started db.Watcher

	err := pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
		_, err := db.New(tx).LockEventGroup(t.Context(), w.GroupID)
		require.NoError(t, err)

		started, err = Start(t.Context(), tx, h.policy, w, h.now, h.recorder)

		return err
	})
	if err != nil {
		return db.Watcher{}, fmt.Errorf("start in a transaction: %w", err)
	}

	return started, nil
}

// mustStart starts w and returns it.
func (h *harness) mustStart(t *testing.T, w NewWatcher) db.Watcher {
	t.Helper()

	started, err := h.start(t, w)
	require.NoError(t, err)

	return started
}

// tick makes every pending watch_tick job ready, claims them and runs
// each with the ticker, as the workers do. It returns how many ran.
func (h *harness) tick(t *testing.T, tk *Ticker) int {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() WHERE kind = 'watch_tick' AND status = 'pending'")
	require.NoError(t, err)

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindWatchTick}, Batch: 100,
	})
	require.NoError(t, err)

	for _, job := range jobs {
		require.NoError(t, tk.Handle(t.Context(), job))

		_, err = db.New(h.pool).CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
		require.NoError(t, err)
	}

	return len(jobs)
}

// watcher returns the watcher with id.
func (h *harness) watcher(t *testing.T, id uuid.UUID) db.Watcher {
	t.Helper()

	w, err := db.New(h.pool).GetWatcher(t.Context(), id)
	require.NoError(t, err)

	return w
}

// checks returns the checks of the watcher with id, in order.
func (h *harness) checks(t *testing.T, id uuid.UUID) []db.WatcherCheck {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM watcher_checks WHERE watcher_id = $1 ORDER BY seq", id)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.WatcherCheck])
	require.NoError(t, err)

	return out
}

// pendingTicks returns the pending watch_tick jobs.
func (h *harness) pendingTicks(t *testing.T) []db.Job {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM jobs WHERE kind = 'watch_tick' AND status = 'pending' ORDER BY id")
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.Job])
	require.NoError(t, err)

	return out
}

// closed returns the groups whose close_group the watchers asked for.
func (h *harness) closed() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]uuid.UUID(nil), h.closes...)
}

// httpWatcher is a new HTTP watcher of the checkout group on url.
func httpWatcher(name, url string, passes int) NewWatcher {
	return NewWatcher{
		GroupID:         checkoutGroup,
		InvestigationID: checkoutInvestigation,
		Name:            name,
		Origin:          OriginRoute,
		Check:           HTTPCheck{URL: url},
		Settings:        Settings{Interval: testInterval, Timeout: testTimeout, PassesToRecover: passes, CheckTimeout: 5 * time.Second},
	}
}
