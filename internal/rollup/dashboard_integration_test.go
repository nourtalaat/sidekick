//go:build integration

package rollup

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// dashboardNow is the time of the dashboard tests: a few days after the
// fixture's investigations, so they are inside the 30 day window.
var dashboardNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// newDashboard returns a Dashboard on the harness's database that thinks it
// is now, and the registry it reports to.
func (h *harness) newDashboard(t *testing.T, now *time.Time) (*Dashboard, *prometheus.Registry) {
	t.Helper()

	reg := prometheus.NewRegistry()

	d, err := NewDashboard(reg, DashboardOptions{
		Pool:            h.pool,
		RouteCapsMicros: map[string]int64{"r1": 5_000_000},
		GlobalCapMicros: 20_000_000,
		Now:             func() time.Time { return *now },
	})
	require.NoError(t, err)

	return d, reg
}

// seedSpend adds the spend of the day before dashboardNow and of its day.
func (h *harness) seedSpend(t *testing.T) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), `INSERT INTO spend_daily (day, route, connector, model, cost_micros, input_tokens, output_tokens) VALUES
		('2026-10-29', 'r1', 'claude', 'm1', 1500000, 10, 5),
		('2026-10-29', 'r1', 'openai', 'm1', 500000, 10, 5),
		('2026-10-29', 'r1', 'claude', 'm2', 250000, 10, 5),
		('2026-10-29', 'r2', 'claude', 'm1', 3000000, 10, 5),
		('2026-10-29', 'r2', 'claude', '', 10000, 10, 5),
		('2026-10-28', 'r1', 'claude', 'm1', 9000000, 10, 5)`)
	require.NoError(t, err)
}

// bucketMid is the middle of bucket i of the histograms, in seconds.
func bucketMid(i int) float64 {
	return (histBase*math.Pow(histGrowth, float64(i-1)) + histBase*math.Pow(histGrowth, float64(i))) / 2 / 1000
}

// wantDashboard are the dashboard series for fixture/records.sql after a
// rollup run and seedSpend, worked out by hand from the rows in
// wantStats and wantQuality. The first findings of r1 are in buckets 9, 9,
// 10, 11 and 12, so the median is the middle of bucket 10; r2 has one in
// bucket 8.
func wantDashboard() map[string]float64 {
	return map[string]float64{
		`sidekick_dashboard_rated_verdicts_30d{model="m1",route="r1",verdict="false_positive"}`:   3,
		`sidekick_dashboard_rated_verdicts_30d{model="m1",route="r1",verdict="true_positive"}`:    1,
		`sidekick_dashboard_rated_verdicts_30d{model="m1",route="r2",verdict="true_positive"}`:    1,
		`sidekick_dashboard_correct_verdicts_30d{model="m1",route="r1",verdict="false_positive"}`: 1,
		`sidekick_dashboard_correct_verdicts_30d{model="m1",route="r1",verdict="true_positive"}`:  1,
		`sidekick_dashboard_correct_verdicts_30d{model="m1",route="r2",verdict="true_positive"}`:  0,
		`sidekick_dashboard_verdict_accuracy_ratio_30d{model="m1",route="r1"}`:                    0.5,
		`sidekick_dashboard_verdict_accuracy_ratio_30d{model="m1",route="r2"}`:                    0,
		`sidekick_dashboard_investigations_30d{route="r1",verdict="false_positive"}`:              4,
		`sidekick_dashboard_investigations_30d{route="r1",verdict="true_positive"}`:               1,
		`sidekick_dashboard_investigations_30d{route="r1",verdict="inconclusive"}`:                1,
		`sidekick_dashboard_investigations_30d{route="r2",verdict="none"}`:                        1,
		`sidekick_dashboard_investigations_30d{route="r2",verdict="true_positive"}`:               1,
		`sidekick_dashboard_recoveries_30d{route="r1"}`:                                           1,
		`sidekick_dashboard_recoveries_30d{route="r2"}`:                                           1,
		`sidekick_dashboard_watch_timeouts_30d{route="r1"}`:                                       1,
		`sidekick_dashboard_watch_timeouts_30d{route="r2"}`:                                       0,
		`sidekick_dashboard_time_to_first_finding_median_seconds_30d{route="r1"}`:                 bucketMid(10),
		`sidekick_dashboard_time_to_first_finding_median_seconds_30d{route="r2"}`:                 bucketMid(8),
		`sidekick_dashboard_spend_today_usd{model="m1",route="r1"}`:                               2,
		`sidekick_dashboard_spend_today_usd{model="m2",route="r1"}`:                               0.25,
		`sidekick_dashboard_spend_today_usd{model="m1",route="r2"}`:                               3,
		`sidekick_dashboard_spend_today_usd{model="none",route="r2"}`:                             0.01,
		`sidekick_dashboard_route_spend_cap_usd{route="r1"}`:                                      5,
		`sidekick_dashboard_spend_cap_usd`:                                                        20,
		`sidekick_dashboard_refreshed_timestamp_seconds`:                                          float64(dashboardNow.Unix()),
	}
}

func requireSeries(t *testing.T, want, got map[string]float64) {
	t.Helper()

	for series := range got {
		require.Contains(t, want, series, "unexpected series")
	}

	for series, v := range want {
		require.Contains(t, got, series)
		require.InDelta(t, v, got[series], 1e-9, series)
	}
}

func TestDashboardMetricsAfterARollupRun(t *testing.T) {
	t.Parallel()

	h := setup(t, 0)
	h.run(t)
	h.seedSpend(t)

	now := dashboardNow
	d, reg := h.newDashboard(t, &now)

	require.NoError(t, d.Refresh(t.Context()))

	requireSeries(t, wantDashboard(), scrapeDashboard(t, reg))
}

func TestDashboardReplicasReportTheSameNumbers(t *testing.T) {
	t.Parallel()

	h := setup(t, 0)
	h.run(t)
	h.seedSpend(t)

	now := dashboardNow
	first, firstReg := h.newDashboard(t, &now)
	second, secondReg := h.newDashboard(t, &now)

	// The replica that did not run the job reads the same tables.
	require.NoError(t, first.Refresh(t.Context()))
	require.NoError(t, second.Refresh(t.Context()))

	requireSeries(t, scrapeDashboard(t, firstReg), scrapeDashboard(t, secondReg))
}

func TestDashboardDropsSeriesThatLeftTheWindow(t *testing.T) {
	t.Parallel()

	h := setup(t, 0)
	h.run(t)
	h.seedSpend(t)

	now := dashboardNow
	d, reg := h.newDashboard(t, &now)

	require.NoError(t, d.Refresh(t.Context()))
	require.Contains(t, scrapeDashboard(t, reg), `sidekick_dashboard_investigations_30d{route="r1",verdict="false_positive"}`)

	// A month later the fixture's days are out of the window and the
	// spend of that day is not today's.
	now = dashboardNow.AddDate(0, 0, 40)

	require.NoError(t, d.Refresh(t.Context()))

	requireSeries(t, map[string]float64{
		`sidekick_dashboard_route_spend_cap_usd{route="r1"}`: 5,
		`sidekick_dashboard_spend_cap_usd`:                   20,
		`sidekick_dashboard_refreshed_timestamp_seconds`:     float64(now.Unix()),
	}, scrapeDashboard(t, reg))
}

func TestDashboardKeepsTheLastNumbersWhenARefreshFails(t *testing.T) {
	t.Parallel()

	h := setup(t, 0)
	h.run(t)
	h.seedSpend(t)

	now := dashboardNow
	d, reg := h.newDashboard(t, &now)

	require.NoError(t, d.Refresh(t.Context()))

	now = now.Add(time.Hour)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	require.Error(t, d.Refresh(cancelled))

	requireSeries(t, wantDashboard(), scrapeDashboard(t, reg))
}
