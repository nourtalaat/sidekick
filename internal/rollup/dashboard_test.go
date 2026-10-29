package rollup

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// scrapeDashboard serves reg the way /metrics does and returns the
// dashboard's series as the scrape shows them, such as
// `sidekick_dashboard_spend_today_usd{model="m1",route="r1"}`, with their
// values. Labels are in alphabetical order.
func scrapeDashboard(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()

	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	got := map[string]float64{}

	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "sidekick_dashboard_") {
			continue
		}

		cut := strings.LastIndex(line, " ")
		require.Positive(t, cut, line)

		v, err := strconv.ParseFloat(line[cut+1:], 64)
		require.NoError(t, err, line)

		got[line[:cut]] = v
	}

	return got
}

func TestDashboardMetricsOfTheRows(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	d, err := NewDashboard(reg, DashboardOptions{
		RouteCapsMicros: map[string]int64{"r1": 5_000_000},
		GlobalCapMicros: 12_500_000,
	})
	require.NoError(t, err)

	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	metrics := d.metricsOf(dashboardData{
		at: at,
		quality: []db.DashboardQualityRow{
			{Route: "r1", Model: "m1", Predicted: "false_positive", Rated: 3, Correct: 1},
			{Route: "r1", Model: "m1", Predicted: "true_positive", Rated: 1, Correct: 1},
			// An empty model and a model named none add up.
			{Route: "r2", Model: "", Predicted: "true_positive", Rated: 2, Correct: 0},
			{Route: "r2", Model: "none", Predicted: "true_positive", Rated: 2, Correct: 2},
			// Nothing rated gives no accuracy, not NaN.
			{Route: "r3", Model: "m1", Predicted: "inconclusive", Rated: 0, Correct: 0},
		},
		stats: []db.DashboardStatsByRouteRow{
			{Route: "r1", Verdict: "false_positive", Investigations: 4, Recoveries: 1, WatchTimeouts: 2},
			{Route: "r1", Verdict: "", Investigations: 1},
		},
		hists: []db.DashboardFirstFindingHistogramsRow{
			{Route: "r1", Bucket: 1, N: 2},
			// A bucket outside the histogram is ignored, so r2 has no median.
			{Route: "r2", Bucket: 25, N: 2},
		},
		spend: []db.DashboardSpendByDayRow{
			{Route: "r1", Model: "m1", CostMicros: 1_500_000},
			{Route: "r1", Model: "", CostMicros: 250_000},
		},
	})
	d.snapshot.Store(&metrics)

	want := map[string]float64{
		`sidekick_dashboard_rated_verdicts_30d{model="m1",route="r1",verdict="false_positive"}`:    3,
		`sidekick_dashboard_rated_verdicts_30d{model="m1",route="r1",verdict="true_positive"}`:     1,
		`sidekick_dashboard_rated_verdicts_30d{model="none",route="r2",verdict="true_positive"}`:   4,
		`sidekick_dashboard_rated_verdicts_30d{model="m1",route="r3",verdict="inconclusive"}`:      0,
		`sidekick_dashboard_correct_verdicts_30d{model="m1",route="r1",verdict="false_positive"}`:  1,
		`sidekick_dashboard_correct_verdicts_30d{model="m1",route="r1",verdict="true_positive"}`:   1,
		`sidekick_dashboard_correct_verdicts_30d{model="none",route="r2",verdict="true_positive"}`: 2,
		`sidekick_dashboard_correct_verdicts_30d{model="m1",route="r3",verdict="inconclusive"}`:    0,
		`sidekick_dashboard_verdict_accuracy_ratio_30d{model="m1",route="r1"}`:                     0.5,
		`sidekick_dashboard_verdict_accuracy_ratio_30d{model="none",route="r2"}`:                   0.5,
		`sidekick_dashboard_investigations_30d{route="r1",verdict="false_positive"}`:               4,
		`sidekick_dashboard_investigations_30d{route="r1",verdict="none"}`:                         1,
		`sidekick_dashboard_recoveries_30d{route="r1"}`:                                            1,
		`sidekick_dashboard_watch_timeouts_30d{route="r1"}`:                                        2,
		`sidekick_dashboard_time_to_first_finding_median_seconds_30d{route="r1"}`:                  0.25,
		`sidekick_dashboard_spend_today_usd{model="m1",route="r1"}`:                                1.5,
		`sidekick_dashboard_spend_today_usd{model="none",route="r1"}`:                              0.25,
		`sidekick_dashboard_route_spend_cap_usd{route="r1"}`:                                       5,
		`sidekick_dashboard_spend_cap_usd`:                                                         12.5,
		`sidekick_dashboard_refreshed_timestamp_seconds`:                                           float64(at.Unix()),
	}

	got := scrapeDashboard(t, reg)
	require.Len(t, got, len(want))

	for series, v := range want {
		require.Contains(t, got, series)
		require.InDelta(t, v, got[series], 1e-9, series)
	}
}

func TestDashboardReportsOnlyTheCapsBeforeTheFirstRefresh(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	_, err := NewDashboard(reg, DashboardOptions{RouteCapsMicros: map[string]int64{"r1": 1_000_000}})
	require.NoError(t, err)

	require.Equal(t, map[string]float64{`sidekick_dashboard_route_spend_cap_usd{route="r1"}`: 1}, scrapeDashboard(t, reg))
}

func TestWindowAt(t *testing.T) {
	t.Parallel()

	// 23:30 in New York is already the next day in UTC.
	now := time.Date(2026, 10, 29, 23, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
	w := windowAt(now)

	require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), w.fromDay)
	require.Equal(t, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), w.toDay)
	require.Equal(t, w.fromDay, w.fromHour)
	require.Equal(t, time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), w.toHour)
	require.Equal(t, DashboardWindowDays, int(w.toHour.Sub(w.fromHour)/oneDay))
}

func TestSpendDayIsTheDayInTheCapsTimeZone(t *testing.T) {
	t.Parallel()

	newYork := time.FixedZone("EDT", -4*60*60)
	at := time.Date(2026, 10, 29, 2, 0, 0, 0, time.UTC)

	require.Equal(t, time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), spendDay(at, time.UTC).Time)
	require.Equal(t, time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), spendDay(at, newYork).Time)
}
