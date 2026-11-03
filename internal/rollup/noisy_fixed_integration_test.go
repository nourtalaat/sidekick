//go:build integration

package rollup

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// noisyRow is the part of a row of stats_hourly that the noisy alerts fixed
// tests read. The service of each investigation of fixture/noisy_fixed.sql
// is the name of its case.
type noisyRow struct {
	Hour           time.Time `db:"hour"`
	Service        string    `db:"service"`
	Investigations int32     `db:"investigations"`
	NoisyFixed     int32     `db:"noisy_fixed"`
}

func TestRecountCountsANoisyAlertFixedOnce(t *testing.T) {
	t.Parallel()

	h := setupNoisyFixed(t)
	h.run(t)

	got := readNoisyFixed(t, h.pool)

	tests := []struct {
		name string
		hour time.Time
		want int32
	}{
		{name: "applied_only", hour: hour10, want: 1},
		{name: "merged_only", hour: hour10, want: 1},
		{name: "api_only", hour: hour10, want: 1},
		{name: "all_three", hour: hour10, want: 1},
		{name: "two_api_and_merged", hour: hour10, want: 1},
		{name: "late_merge", hour: hourNext, want: 1},
		{name: "merged_undone", hour: hour10, want: 0},
		{name: "api_undone", hour: hour10, want: 0},
		{name: "pr_open", hour: hour10, want: 0},
		{name: "pr_closed", hour: hour10, want: 0},
		{name: "pr_no_row", hour: hour10, want: 0},
		{name: "pr_failed", hour: hour10, want: 0},
		{name: "api_failed", hour: hour10, want: 0},
		{name: "not_false_positive", hour: hour10, want: 0},
		{name: "no_verdict", hour: hour10, want: 0},
		{name: "mute", hour: hour10, want: 0},
		{name: "code_change", hour: hour10, want: 0},
	}

	require.Len(t, got, len(tests))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row, ok := got[tt.name]
			require.True(t, ok)
			require.Equal(t, noisyRow{Hour: tt.hour, Service: tt.name, Investigations: 1, NoisyFixed: tt.want}, row)
		})
	}
}

func TestRecountOfNoisyAlertsFixedTwiceGivesTheSameRows(t *testing.T) {
	t.Parallel()

	h := setupNoisyFixed(t)
	h.run(t)

	first := readNoisyFixed(t, h.pool)
	stats := readStats(t, h.pool)

	h.markHours(t, hour10, hourNext)
	h.run(t)

	require.Equal(t, first, readNoisyFixed(t, h.pool))
	require.Equal(t, stats, readStats(t, h.pool))

	h.markHours(t, hour10, hourNext)
	h.run(t)

	require.Equal(t, first, readNoisyFixed(t, h.pool))
	require.Equal(t, stats, readStats(t, h.pool))
}

func TestAMergeIsCountedByTheNextRun(t *testing.T) {
	t.Parallel()

	h := setupNoisyFixed(t)
	h.run(t)

	require.Zero(t, readNoisyFixed(t, h.pool)["pr_open"].NoisyFixed)
	require.Empty(t, h.dirty(t))

	// The daily check finds the pull request merged.
	_, err := h.pool.Exec(t.Context(), `UPDATE pull_request_states SET state = 'merged', merged_at = now(), checked_at = now()
		WHERE proposal_id = md5('proposal-pr_open1')::uuid AND state = 'open'`)
	require.NoError(t, err)

	require.Equal(t, []string{"stats_hourly 2026-10-20 10:00"}, h.dirty(t))

	h.run(t)

	require.EqualValues(t, 1, readNoisyFixed(t, h.pool)["pr_open"].NoisyFixed)
	require.Empty(t, h.dirty(t))
}

// setupNoisyFixed returns a harness on a database seeded with
// fixture/noisy_fixed.sql. Loading the fixture marks every hour of it, as
// the triggers do for any change.
func setupNoisyFixed(t *testing.T) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/noisy_fixed.sql")

	metrics, err := NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	return &harness{pool: pool, handler: New(Options{Pool: pool, BatchSize: DefaultBatchSize, Metrics: metrics})}
}

// markHours marks the hours for a recount, as a change would.
func (h *harness) markHours(t *testing.T, hours ...time.Time) {
	t.Helper()

	for _, hour := range hours {
		_, err := h.pool.Exec(t.Context(), "INSERT INTO rollup_dirty (table_name, bucket) VALUES ('stats_hourly', $1) ON CONFLICT DO NOTHING", hour)
		require.NoError(t, err)
	}
}

// readNoisyFixed returns the rows of stats_hourly by service.
func readNoisyFixed(t *testing.T, pool *pgxpool.Pool) map[string]noisyRow {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT hour, service, investigations, noisy_fixed FROM stats_hourly")
	require.NoError(t, err)

	all, err := pgx.CollectRows(rows, pgx.RowToStructByName[noisyRow])
	require.NoError(t, err)

	byService := make(map[string]noisyRow, len(all))

	for _, row := range all {
		row.Hour = row.Hour.UTC()
		byService[row.Service] = row
	}

	return byService
}
