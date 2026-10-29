//go:build integration

package rollup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// wantStats are the rows fixture/records.sql counts to, worked out by hand.
// The histogram buckets are those of rollup_hist_bucket: 40 s is 10, 60 s
// is 11, 30 s is 9, 20 s is 8, 100 s is 12, 12 minutes is 16 and 16
// minutes is 17.
var wantStats = []statsRow{
	{
		Hour: hour10, Route: "r1", Service: "checkout", Model: "m1", Verdict: "false_positive",
		Investigations: 2, Finished: 2, DurationN: 2, DurationMsSum: 240_000, FirstFindingN: 2, FirstFindingMsSum: 100_000,
		FirstFindingHist: hist(10, 11), ToolCalls: 8, CostMicros: 3000, InputTokens: 160, OutputTokens: 80,
		NoisyFixed: 1, DowntimeHist: hist(), WatchTimeouts: 1,
	},
	{
		Hour: hour10, Route: "r1", Service: "checkout", Model: "m1", Verdict: "true_positive",
		Investigations: 1, Finished: 1, DurationN: 1, DurationMsSum: 300_000, FirstFindingN: 1, FirstFindingMsSum: 30_000,
		FirstFindingHist: hist(9), ToolCalls: 10, CostMicros: 5000, InputTokens: 200, OutputTokens: 100,
		Recoveries: 1, DowntimeMsSum: 720_000, DowntimeHist: hist(16),
	},
	{
		Hour: hour10, Route: "r1", Service: "payments", Model: "m2", Verdict: "inconclusive",
		Investigations: 1, Finished: 1, DurationN: 1, DurationMsSum: 60_000,
		FirstFindingHist: hist(), ToolCalls: 1, CostMicros: 500, InputTokens: 10, OutputTokens: 5, DowntimeHist: hist(),
	},
	{
		Hour: hour11, Route: "r1", Service: "checkout", Model: "m1", Verdict: "false_positive",
		Investigations: 1, Finished: 1, DurationN: 1, DurationMsSum: 120_000, FirstFindingN: 1, FirstFindingMsSum: 100_000,
		FirstFindingHist: hist(12), ToolCalls: 6, CostMicros: 2500, InputTokens: 90, OutputTokens: 45, DowntimeHist: hist(),
	},
	{
		Hour: hour11, Route: "r2", Service: "", Model: "", Verdict: "",
		Investigations: 1, FirstFindingHist: hist(), ToolCalls: 2, CostMicros: 700, InputTokens: 20, OutputTokens: 10, DowntimeHist: hist(),
	},
	{
		Hour: hour11, Route: "r2", Service: "checkout", Model: "m1", Verdict: "true_positive",
		Investigations: 1, Finished: 1, DurationN: 1, DurationMsSum: 300_000, FirstFindingN: 1, FirstFindingMsSum: 20_000,
		FirstFindingHist: hist(8), ToolCalls: 4, CostMicros: 3000, InputTokens: 80, OutputTokens: 40,
		Recoveries: 1, DowntimeMsSum: 960_000, DowntimeHist: hist(17),
	},
	{
		Hour: hourNext, Route: "r1", Service: "checkout", Model: "m1", Verdict: "false_positive",
		Investigations: 1, Finished: 1, DurationN: 1, DurationMsSum: 180_000, FirstFindingN: 1, FirstFindingMsSum: 30_000,
		FirstFindingHist: hist(9), ToolCalls: 2, CostMicros: 800, InputTokens: 40, OutputTokens: 20, DowntimeHist: hist(),
	},
}

// wantQuality are the rows of quality_daily for the fixture: the five
// investigations people rated. c4 and c6 have no feedback, and c8 has only
// a rating of a finding.
var wantQuality = []qualityRow{
	{Day: day20, Route: "r1", Service: "checkout", Model: "m1", Strategy: "default", Predicted: "false_positive", Confirmed: "false_positive", N: 1},
	{Day: day20, Route: "r1", Service: "checkout", Model: "m1", Strategy: "default", Predicted: "false_positive", Confirmed: "true_positive", N: 1},
	{Day: day20, Route: "r1", Service: "checkout", Model: "m1", Strategy: "default", Predicted: "false_positive", Confirmed: "unknown", N: 1},
	{Day: day20, Route: "r1", Service: "checkout", Model: "m1", Strategy: "default", Predicted: "true_positive", Confirmed: "true_positive", N: 1},
	{Day: day20, Route: "r2", Service: "checkout", Model: "m1", Strategy: "deep", Predicted: "true_positive", Confirmed: "false_positive", N: 1},
}

func TestHandleCountsTheDirtyBuckets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		batch int32
	}{
		{name: "OneBatch", batch: 1000},
		{name: "OneBucketAtATime", batch: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.batch)
			h.run(t)

			require.Equal(t, wantStats, readStats(t, h.pool))
			require.Equal(t, wantQuality, readQuality(t, h.pool))
			require.Empty(t, h.dirty(t))
		})
	}
}

func TestHandleTwiceGivesTheSameRows(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)
	h.run(t)

	stats, quality := readStats(t, h.pool), readQuality(t, h.pool)

	h.markAll(t)
	h.run(t)

	require.Equal(t, stats, readStats(t, h.pool))
	require.Equal(t, quality, readQuality(t, h.pool))

	h.run(t)

	require.Equal(t, stats, readStats(t, h.pool))
	require.Equal(t, quality, readQuality(t, h.pool))
}

func TestHandleCountsAChangeAfterwards(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)
	h.run(t)

	_, err := h.pool.Exec(t.Context(), "UPDATE investigations SET confirmed_verdict = 'inconclusive' WHERE id = $1", invC4)
	require.NoError(t, err)
	_, err = h.pool.Exec(t.Context(), "UPDATE investigations SET status = 'final', finished_at = '2026-10-20 11:35:00+00' WHERE id = $1", invC6)
	require.NoError(t, err)

	require.Equal(t, []string{"quality_daily 2026-10-20 00:00", "stats_hourly 2026-10-20 11:00"}, h.dirty(t))

	h.run(t)

	quality := readQuality(t, h.pool)
	require.Len(t, quality, 6)
	require.Equal(t, qualityRow{
		Day: day20, Route: "r1", Service: "payments", Model: "m2", Strategy: "", Predicted: "inconclusive", Confirmed: "inconclusive", N: 1,
	}, quality[4])

	stats := readStats(t, h.pool)
	require.Equal(t, int32(1), stats[4].Finished)
	require.Equal(t, int64(300_000), stats[4].DurationMsSum)
	require.Empty(t, h.dirty(t))
}

func TestHandleKeepsTheHistoryOfDeletedInvestigations(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)
	h.run(t)

	_, err := h.pool.Exec(t.Context(), "DELETE FROM investigations WHERE id = $1", invC8)
	require.NoError(t, err)

	h.run(t)

	require.Equal(t, wantStats, readStats(t, h.pool))
	require.Empty(t, h.dirty(t))
}

func TestHandleDropsRowsOfBucketsThatAreEmptyNow(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)
	h.run(t)

	_, err := h.pool.Exec(t.Context(), "DELETE FROM investigations WHERE id = $1", invC8)
	require.NoError(t, err)

	_, err = h.pool.Exec(t.Context(), "SELECT rollup_mark('stats_hourly', $1)", hourNext)
	require.NoError(t, err)

	h.run(t)

	require.Equal(t, wantStats[:6], readStats(t, h.pool))
}

func TestHandlePutsTheMarksBackWhenARecountFails(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)

	_, err := h.pool.Exec(t.Context(), "ALTER TABLE quality_daily ADD CONSTRAINT quality_never CHECK (n < 0) NOT VALID")
	require.NoError(t, err)

	require.Error(t, h.handler.Handle(t.Context(), db.Job{ID: 1}))

	require.Empty(t, readQuality(t, h.pool))
	require.Equal(t, []string{"quality_daily 2026-10-20 00:00", "quality_daily 2026-10-21 00:00"}, h.dirty(t))
	require.Equal(t, wantStats, readStats(t, h.pool), "the tables before the failing one are counted")
}

// TestHandleLeavesAMarkThatAWriterHoldsALockOn: a writer whose transaction
// has not ended holds the lock of the mark it made, so the run skips the
// bucket, and the next run counts the writer's change.
func TestHandleLeavesAMarkThatAWriterHoldsALockOn(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)

	writer, err := h.pool.Begin(t.Context())
	require.NoError(t, err)

	defer func() { _ = writer.Rollback(t.Context()) }()

	_, err = writer.Exec(t.Context(), "UPDATE investigations SET service = 'billing' WHERE id = $1", invC1)
	require.NoError(t, err)

	h.run(t)

	require.Equal(t, []string{"quality_daily 2026-10-20 00:00", "stats_hourly 2026-10-20 10:00"}, h.dirty(t))

	for _, row := range readStats(t, h.pool) {
		require.NotEqual(t, hour10, row.Hour, "the hour the writer holds is not counted yet")
	}

	require.NoError(t, writer.Commit(t.Context()))

	h.run(t)

	require.Empty(t, h.dirty(t))

	var found bool

	for _, row := range readStats(t, h.pool) {
		if row.Service == "billing" {
			found = true

			require.Equal(t, int32(1), row.Investigations)
		}
	}

	require.True(t, found, "the writer's change is counted by the next run")
}

// TestAMarkAfterTheTakeOutlivesTheRun: a writer that marks a bucket the run
// has taken waits for the run's transaction, then marks it again, so the
// change the run could not see is counted by the next run.
func TestAMarkAfterTheTakeOutlivesTheRun(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)

	run, err := h.pool.Begin(t.Context())
	require.NoError(t, err)

	defer func() { _ = run.Rollback(t.Context()) }()

	taken, err := db.New(run).TakeDirtyBuckets(t.Context(), db.TakeDirtyBucketsParams{TableName: TableStatsHourly, Batch: DefaultBatchSize})
	require.NoError(t, err)
	require.Len(t, taken, 3)

	written := make(chan error, 1)

	go func() {
		_, err := h.pool.Exec(t.Context(), "UPDATE investigations SET status = 'final', finished_at = '2026-10-20 11:35:00+00' WHERE id = $1", invC6)
		written <- err
	}()

	select {
	case err := <-written:
		t.Fatalf("the writer finished while the run held the bucket: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, run.Commit(t.Context()))
	require.NoError(t, <-written)

	require.Equal(t,
		[]string{"quality_daily 2026-10-20 00:00", "quality_daily 2026-10-21 00:00", "stats_hourly 2026-10-20 11:00"},
		h.dirty(t), "the writer's mark is there for the next run")
}

func TestScheduleEnqueuesOneJobAtATime(t *testing.T) {
	t.Parallel()

	h := setup(t, DefaultBatchSize)
	s := NewScheduler(SchedulerOptions{Pool: h.pool})

	enqueued, err := s.Schedule(t.Context())
	require.NoError(t, err)
	require.True(t, enqueued)

	enqueued, err = s.Schedule(t.Context())
	require.NoError(t, err)
	require.False(t, enqueued, "a job is pending already")

	var n int

	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'rollup'").Scan(&n))
	require.Equal(t, 1, n)
}
