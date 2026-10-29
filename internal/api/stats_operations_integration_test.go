//go:build integration

package api_test

import (
	"math"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// operationsBody is the response of GET /stats/operations.
type operationsBody struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Interval string `json:"interval"`
	Series   []struct {
		Bucket   string `json:"bucket"`
		Verdicts []struct {
			Verdict        string `json:"verdict"`
			Investigations int64  `json:"investigations"`
		} `json:"verdicts"`
		operationsFigures
	} `json:"series"`
	Totals   operationsTotals `json:"totals"`
	Previous operationsTotals `json:"previous"`
	Verdicts []struct {
		Verdict        string `json:"verdict"`
		Investigations int64  `json:"investigations"`
	} `json:"verdicts"`
	Downtime []struct {
		Route            string   `json:"route"`
		Service          string   `json:"service"`
		Recoveries       int64    `json:"recoveries"`
		DowntimeMsSum    int64    `json:"downtimeMsSum"`
		WatchTimeouts    int64    `json:"watchTimeouts"`
		MedianDowntimeMs *float64 `json:"medianDowntimeMs"`
	} `json:"downtime"`
}

type operationsFigures struct {
	Investigations    int64 `json:"investigations"`
	Finished          int64 `json:"finished"`
	DurationN         int64 `json:"durationN"`
	DurationMsSum     int64 `json:"durationMsSum"`
	FirstFindingN     int64 `json:"firstFindingN"`
	FirstFindingMsSum int64 `json:"firstFindingMsSum"`
	ToolCalls         int64 `json:"toolCalls"`
	NoisyFixed        int64 `json:"noisyFixed"`
	ActionsProposed   int64 `json:"actionsProposed"`
	ActionsApproved   int64 `json:"actionsApproved"`
	ActionsExecuted   int64 `json:"actionsExecuted"`
	Recoveries        int64 `json:"recoveries"`
	DowntimeMsSum     int64 `json:"downtimeMsSum"`
	WatchTimeouts     int64 `json:"watchTimeouts"`
}

type operationsTotals struct {
	operationsFigures
	MedianFirstFindingMs *float64 `json:"medianFirstFindingMs"`
	MedianDowntimeMs     *float64 `json:"medianDowntimeMs"`
}

// histMedian is the median that the API works out for values in bucket i of
// a histogram, counting from 0, when the middle one has the given rank
// among them as a fraction: the value placed in proportion inside the
// bucket, which spans from 500 * 1.6^(i-1) to 500 * 1.6^i milliseconds.
func histMedian(i int, fraction float64) float64 {
	lo, hi := 500*math.Pow(1.6, float64(i-1)), 500*math.Pow(1.6, float64(i))

	return lo + fraction*(hi-lo)
}

func TestGetOperationsStats(t *testing.T) {
	t.Parallel()

	s := setupStats(t)

	t.Run("the default range", func(t *testing.T) {
		t.Parallel()

		var got operationsBody

		decodeStats(t, getStats(t, s, "/stats/operations"), &got)

		require.Equal(t, "2026-09-29", got.From)
		require.Equal(t, "2026-10-28", got.To)
		require.Equal(t, "day", got.Interval)

		require.Len(t, got.Series, 3)

		oct20, oct21, oct22 := got.Series[0], got.Series[1], got.Series[2]

		require.Equal(t, "2026-10-20T00:00:00Z", oct20.Bucket)
		require.Equal(t, operationsFigures{
			Investigations: 14, Finished: 14, DurationN: 14, DurationMsSum: 2_400_000, FirstFindingN: 12, FirstFindingMsSum: 520_000,
			ToolCalls: 90, NoisyFixed: 2, Recoveries: 2, DowntimeMsSum: 1_440_000, WatchTimeouts: 1,
		}, oct20.operationsFigures)
		require.Len(t, oct20.Verdicts, 2)
		require.Equal(t, "false_positive", oct20.Verdicts[0].Verdict)
		require.Equal(t, int64(10), oct20.Verdicts[0].Investigations)
		require.Equal(t, "true_positive", oct20.Verdicts[1].Verdict)
		require.Equal(t, int64(4), oct20.Verdicts[1].Investigations)

		require.Equal(t, "2026-10-21T00:00:00Z", oct21.Bucket)
		require.Equal(t, operationsFigures{
			Investigations: 8, Finished: 8, DurationN: 8, DurationMsSum: 460_000, FirstFindingN: 6, FirstFindingMsSum: 360_000,
			ToolCalls: 14, NoisyFixed: 1, Recoveries: 1, DowntimeMsSum: 600_000,
		}, oct21.operationsFigures)

		require.Equal(t, "2026-10-22T00:00:00Z", oct22.Bucket)
		require.Equal(t, operationsFigures{Investigations: 1, ToolCalls: 1}, oct22.operationsFigures)
		require.Len(t, oct22.Verdicts, 1)
		require.Equal(t, "none", oct22.Verdicts[0].Verdict)

		require.Equal(t, operationsFigures{
			Investigations: 23, Finished: 22, DurationN: 22, DurationMsSum: 2_860_000, FirstFindingN: 18, FirstFindingMsSum: 880_000,
			ToolCalls: 105, NoisyFixed: 3, Recoveries: 3, DowntimeMsSum: 2_040_000, WatchTimeouts: 1,
		}, got.Totals.operationsFigures)

		// 18 first findings: 4 in bucket 9 of the histogram, 8 in bucket 10
		// and 6 in bucket 11. The middle one is the 9th, the 5th of the 8
		// in bucket 10.
		require.InDelta(t, histMedian(10, 5.0/8.0), *got.Totals.MedianFirstFindingMs, 1e-6)
		// 3 recoveries in bucket 16: the middle one is the 1.5th of them.
		require.InDelta(t, histMedian(16, 0.5), *got.Totals.MedianDowntimeMs, 1e-6)

		require.Equal(t, operationsFigures{
			Investigations: 5, Finished: 5, DurationN: 5, DurationMsSum: 500_000, FirstFindingN: 5, FirstFindingMsSum: 150_000, ToolCalls: 20,
		}, got.Previous.operationsFigures)
		require.InDelta(t, histMedian(9, 0.5), *got.Previous.MedianFirstFindingMs, 1e-6)
		require.Nil(t, got.Previous.MedianDowntimeMs)

		type count struct {
			verdict string
			n       int64
		}

		var verdicts []count
		for _, v := range got.Verdicts {
			verdicts = append(verdicts, count{v.Verdict, v.Investigations})
		}

		require.Equal(t, []count{{"true_positive", 4}, {"false_positive", 16}, {"inconclusive", 2}, {"none", 1}}, verdicts)

		require.Len(t, got.Downtime, 2)
		require.Equal(t, "r1", got.Downtime[0].Route)
		require.Equal(t, "checkout", got.Downtime[0].Service)
		require.Equal(t, int64(2), got.Downtime[0].Recoveries)
		require.Equal(t, int64(1_440_000), got.Downtime[0].DowntimeMsSum)
		require.Equal(t, int64(1), got.Downtime[0].WatchTimeouts)
		require.InDelta(t, histMedian(16, 0.5), *got.Downtime[0].MedianDowntimeMs, 1e-6)
		require.Equal(t, "r2", got.Downtime[1].Route)
		require.Equal(t, "payments", got.Downtime[1].Service)
		require.Equal(t, int64(1), got.Downtime[1].Recoveries)
		require.InDelta(t, histMedian(16, 0.5), *got.Downtime[1].MedianDowntimeMs, 1e-6)
	})

	t.Run("by the hour", func(t *testing.T) {
		t.Parallel()

		var got operationsBody

		decodeStats(t, getStats(t, s, "/stats/operations?interval=hour&from=2026-10-20&to=2026-10-20"), &got)

		require.Equal(t, "hour", got.Interval)
		require.Len(t, got.Series, 1)
		require.Equal(t, "2026-10-20T10:00:00Z", got.Series[0].Bucket)
		require.Equal(t, int64(14), got.Series[0].Investigations)
	})

	t.Run("an hour over a range of more than 7 days", func(t *testing.T) {
		t.Parallel()

		rec := getStats(t, s, "/stats/operations?interval=hour")
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.JSONEq(t,
			problem(http.StatusBadRequest, "An interval of an hour needs a range of at most 7 days.", "/api/v1/stats/operations"),
			rec.Body.String())
	})

	filters := []struct {
		name  string
		query string
		want  int64
		prev  int64
	}{
		{name: "a route", query: "?route=r2", want: 8},
		{name: "a service", query: "?service=checkout", want: 14, prev: 5},
		{name: "a model", query: "?model=m2", want: 8},
		{name: "a verdict", query: "?verdict=false_positive", want: 16, prev: 5},
		{name: "filters together", query: "?route=r1&service=checkout&verdict=true_positive", want: 4},
		{name: "one day", query: "?from=2026-10-21&to=2026-10-21", want: 8, prev: 14},
		{name: "nothing matches", query: "?route=nowhere"},
	}

	for _, tt := range filters {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got operationsBody

			decodeStats(t, getStats(t, s, "/stats/operations"+tt.query), &got)

			require.Equal(t, tt.want, got.Totals.Investigations)
			require.Equal(t, tt.prev, got.Previous.Investigations)

			if tt.want == 0 {
				require.Empty(t, got.Series)
				require.Nil(t, got.Totals.MedianFirstFindingMs)
			}
		})
	}

	t.Run("an empty range has empty lists, not null", func(t *testing.T) {
		t.Parallel()

		rec := getStats(t, s, "/stats/operations?route=nowhere")
		require.Equal(t, http.StatusOK, rec.Code)

		for _, empty := range []string{"series", "verdicts", "downtime"} {
			require.Contains(t, rec.Body.String(), `"`+empty+`":[]`)
		}
	})

	t.Run("no session", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/stats/operations", "", "", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	requireRefusals(t, s, "/stats/operations")
}
