//go:build integration

package rollup

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The IDs of fixture/records.sql.
const (
	invC1 = "0192d3a4-0000-7000-8000-0000000000c1"
	invC4 = "0192d3a4-0000-7000-8000-0000000000c4"
	invC6 = "0192d3a4-0000-7000-8000-0000000000c6"
	invC8 = "0192d3a4-0000-7000-8000-0000000000c8"
)

// The hours and days of fixture/records.sql.
var (
	hour10   = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	hour11   = time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC)
	hourNext = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	day20    = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	day21    = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Handler on a database of its own seeded with
// fixture/records.sql, with every hour and day of it marked dirty.
type harness struct {
	pool    *pgxpool.Pool
	handler *Handler
}

// setup returns a harness whose batches take at most batch buckets.
func setup(t *testing.T, batch int32) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/records.sql")

	metrics, err := NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	h := &harness{pool: pool, handler: New(Options{Pool: pool, BatchSize: batch, Metrics: metrics})}
	h.markAll(t)

	return h
}

// markAll replaces the marks with one for each hour and day of the
// fixture, as the migration does for the history.
func (h *harness) markAll(t *testing.T) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "DELETE FROM rollup_dirty")
	require.NoError(t, err)

	for _, hour := range []time.Time{hour10, hour11, hourNext} {
		_, err := h.pool.Exec(t.Context(), "INSERT INTO rollup_dirty (table_name, bucket) VALUES ('stats_hourly', $1)", hour)
		require.NoError(t, err)
	}

	for _, day := range []time.Time{day20, day21} {
		_, err := h.pool.Exec(t.Context(), "INSERT INTO rollup_dirty (table_name, bucket) VALUES ('quality_daily', $1)", day)
		require.NoError(t, err)
	}
}

// run runs one rollup job.
func (h *harness) run(t *testing.T) {
	t.Helper()

	require.NoError(t, h.handler.Handle(t.Context(), db.Job{ID: 1}))
}

// dirty returns the marks that wait, as "table bucket".
func (h *harness) dirty(t *testing.T) []string {
	t.Helper()

	return dirtyOf(t, h.pool)
}

func dirtyOf(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), `SELECT table_name || ' ' || to_char(bucket AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI')
		FROM rollup_dirty ORDER BY 1`)
	require.NoError(t, err)

	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return got
}

// statsRow is a row of stats_hourly.
type statsRow struct {
	Hour              time.Time `db:"hour"`
	Route             string    `db:"route"`
	Service           string    `db:"service"`
	Model             string    `db:"model"`
	Verdict           string    `db:"verdict"`
	Investigations    int32     `db:"investigations"`
	Finished          int32     `db:"finished"`
	DurationN         int32     `db:"duration_n"`
	DurationMsSum     int64     `db:"duration_ms_sum"`
	FirstFindingN     int32     `db:"first_finding_n"`
	FirstFindingMsSum int64     `db:"first_finding_ms_sum"`
	FirstFindingHist  []int32   `db:"first_finding_hist"`
	ToolCalls         int32     `db:"tool_calls"`
	CostMicros        int64     `db:"cost_micros"`
	InputTokens       int64     `db:"input_tokens"`
	OutputTokens      int64     `db:"output_tokens"`
	NoisyFixed        int32     `db:"noisy_fixed"`
	Recoveries        int32     `db:"recoveries"`
	DowntimeMsSum     int64     `db:"downtime_ms_sum"`
	DowntimeHist      []int32   `db:"downtime_hist"`
	WatchTimeouts     int32     `db:"watch_timeouts"`
}

// qualityRow is a row of quality_daily.
type qualityRow struct {
	Day       time.Time `db:"day"`
	Route     string    `db:"route"`
	Service   string    `db:"service"`
	Model     string    `db:"model"`
	Strategy  string    `db:"strategy"`
	Predicted string    `db:"predicted"`
	Confirmed string    `db:"confirmed"`
	N         int32     `db:"n"`
}

func readStats(t *testing.T, pool *pgxpool.Pool) []statsRow {
	t.Helper()

	rows, err := pool.Query(t.Context(), `SELECT hour, route, service, model, verdict, investigations, finished,
		duration_n, duration_ms_sum, first_finding_n, first_finding_ms_sum, first_finding_hist,
		tool_calls, cost_micros, input_tokens, output_tokens, noisy_fixed,
		recoveries, downtime_ms_sum, downtime_hist, watch_timeouts
		FROM stats_hourly ORDER BY hour, route, service, model, verdict`)
	require.NoError(t, err)

	got, err := pgx.CollectRows(rows, pgx.RowToStructByName[statsRow])
	require.NoError(t, err)

	return got
}

func readQuality(t *testing.T, pool *pgxpool.Pool) []qualityRow {
	t.Helper()

	rows, err := pool.Query(t.Context(), `SELECT day, route, service, model, strategy, predicted, confirmed, n
		FROM quality_daily ORDER BY day, route, service, model, strategy, predicted, confirmed`)
	require.NoError(t, err)

	got, err := pgx.CollectRows(rows, pgx.RowToStructByName[qualityRow])
	require.NoError(t, err)

	return got
}

// hist returns a histogram of 24 counts with 1 in each of buckets.
func hist(buckets ...int) []int32 {
	h := make([]int32, 24)
	for _, b := range buckets {
		h[b]++
	}

	return h
}
