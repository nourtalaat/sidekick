//go:build integration

package investigator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// routeCap is the payments route's daily cap in the tests: $40.
const routeCap = 40_000_000

// nextDayStart is the start of the day after stoppedAt, in UTC.
var nextDayStart = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

// capRoute is the payments route with its daily cap, and its notices sent
// to the Slack output.
func capRoute(skip bool) Route {
	return Route{Outputs: []string{"slack-thread"}, DailyMaxCostMicros: routeCap, DailySkip: skip}
}

// TestDailyCapAtStart checks what a reached daily cap does to an
// investigation that has not started: wait holds it until the next day,
// skip ends it unrun, and either way one notice goes to the route's
// outputs.
func TestDailyCapAtStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		opts  options
		spend []spendRow
		// skip means the investigation ends unrun instead of waiting.
		skip       bool
		wantReason string
		wantNotice string
	}{
		{
			name:       "the route's cap waits",
			opts:       options{route: capRoute(false)},
			spend:      []spendRow{{route: "payments", cost: routeCap}},
			wantNotice: "Route payments has used all of today's $40.00 cap. New investigations on this route wait until 00:00 UTC.",
		},
		{
			name:       "the route's cap skips",
			opts:       options{route: capRoute(true)},
			spend:      []spendRow{{route: "payments", cost: routeCap + 1}},
			skip:       true,
			wantReason: "Not investigated: route payments has used all of today's $40.00 cap.",
			wantNotice: "Route payments has used all of today's $40.00 cap. New investigations on this route are skipped until 00:00 UTC.",
		},
		{
			name: "the global cap waits, counting every route",
			opts: options{
				route: Route{Outputs: []string{"slack-thread"}},
				daily: DailyCaps{MaxCostMicros: 200_000_000},
			},
			spend:      []spendRow{{route: "payments", cost: 50_000_000}, {route: "checkout", cost: 150_000_000}},
			wantNotice: "All routes together have used today's global cap of $200.00. New investigations wait until 00:00 UTC.",
		},
		{
			name: "the global cap skips",
			opts: options{
				route: Route{Outputs: []string{"slack-thread"}},
				daily: DailyCaps{MaxCostMicros: 200_000_000, Skip: true},
			},
			spend:      []spendRow{{route: "checkout", cost: 200_000_000}},
			skip:       true,
			wantReason: "Not investigated: all routes together have used today's global cap of $200.00.",
			wantNotice: "All routes together have used today's global cap of $200.00. New investigations are skipped until 00:00 UTC.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.opts.stoppedTime = true
			h := setup(t, tt.opts)
			h.addSpend(t, tt.spend...)

			require.NoError(t, h.run(t))
			require.Empty(t, h.steps(t), "no model call is made")
			require.Empty(t, h.conn.Requests())
			require.Equal(t, []string{tt.wantNotice}, h.notices(t))

			if tt.skip {
				inv := h.investigation(t)
				require.Equal(t, StatusFinal, inv.Status)

				r := h.report(t)
				require.Equal(t, report.Inconclusive, r.Verdict)
				require.Equal(t, report.StopBudget, r.StopReason)
				require.Equal(t, tt.wantReason, r.Reason)
				require.Equal(t, dailyCapDetail, r.StopDetail)

				_, phase, body := h.publishState(t)
				require.Equal(t, "final", phase)
				require.Equal(t, tt.wantReason, body.Report.Reason)

				require.InDelta(t, 1, testutil.ToFloat64(h.handler.opts.Metrics.skippedEvents.WithLabelValues("payments", skipReasonDailyCap)), 0)
				require.Empty(t, h.waitingJobs(t))

				return
			}

			require.Equal(t, StatusQueued, h.investigation(t).Status, "the investigation has not started")
			require.Nil(t, h.investigation(t).StartedAt, "its wall time has not started either")
			require.Equal(t, []time.Time{nextDayStart}, h.waitingJobs(t))
		})
	}
}

// TestDailyCapWaitsUntilTheNextDay holds an investigation at the route's
// cap twice on the same day, with one notice, then runs it on the next
// day.
func TestDailyCapWaitsUntilTheNextDay(t *testing.T) {
	t.Parallel()

	h := setup(t, options{
		route:       capRoute(false),
		stoppedTime: true,
		turns:       []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, uncitedReport))},
	})
	h.addSpend(t, spendRow{route: "payments", cost: routeCap})

	require.NoError(t, h.run(t))
	h.wakeWaitingJob(t)
	require.NoError(t, h.run(t), "the job runs again on the same day")
	require.Equal(t, []time.Time{nextDayStart}, h.waitingJobs(t))
	require.Len(t, h.notices(t), 1, "one notice a day")

	h.handler.now = func() time.Time { return nextDayStart.Add(time.Second) }
	h.wakeWaitingJob(t)
	require.NoError(t, h.run(t))

	inv := h.investigation(t)
	require.Equal(t, StatusFinal, inv.Status)
	require.Equal(t, report.StopSubmitted, h.report(t).StopReason, "the new day has spent nothing yet")
	require.Len(t, h.notices(t), 1)
}

// TestDailyCapWhileRunning checks that a running investigation that
// crosses its route's cap gets its final turn.
func TestDailyCapWhileRunning(t *testing.T) {
	t.Parallel()

	h := setup(t, options{
		route:       Route{Outputs: []string{"slack-thread"}, DailyMaxCostMicros: turnCost},
		stoppedTime: true,
		turns:       []modeltest.Turn{turn(search("c1")), submit()},
	})

	require.NoError(t, h.run(t))
	require.Equal(t, []string{
		"prompt ok ", "model_turn ok ", "tool_result ok search_logs", finalNote, "model_turn ok ", "tool_result ok submit_verdict",
	}, summary(h.steps(t)))
	require.Equal(t, budgetDailyCap, deref(h.steps(t)[3].Budget))

	r := h.report(t)
	require.Equal(t, report.Inconclusive, r.Verdict)
	require.Equal(t, report.StopBudget, r.StopReason)
	require.Equal(t, "daily cap nearly reached; the verdict submitted in the final turn was true_positive", r.StopDetail)
	require.InDelta(t, 1, exhausted(h, budgetDailyCap), 0)
}

// spendRow is a row of spend_daily on the day of stoppedAt.
type spendRow struct {
	route string
	cost  int64
}

// addSpend adds rows to spend_daily on the day of stoppedAt.
func (h *harness) addSpend(t *testing.T, rows ...spendRow) {
	t.Helper()

	for _, row := range rows {
		_, err := h.pool.Exec(t.Context(),
			"INSERT INTO spend_daily (day, route, connector, model, cost_micros) VALUES ($1, $2, 'claude', $3, $4)",
			stoppedAt.Format(time.DateOnly), row.route, testModel, row.cost)
		require.NoError(t, err)
	}
}

// notices returns the text of each notice job, oldest first.
func (h *harness) notices(t *testing.T) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT payload FROM jobs WHERE kind = 'notice' ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []string

	for rows.Next() {
		var p struct {
			Sink   string `json:"sink"`
			Notice struct {
				Kind  string `json:"kind"`
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"notice"`
		}

		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		require.NoError(t, json.Unmarshal(raw, &p))
		require.Equal(t, "slack-thread", p.Sink)
		require.Equal(t, "daily_cap", p.Notice.Kind)
		require.Equal(t, noticeTitleDailyCap, p.Notice.Title)

		out = append(out, p.Notice.Text)
	}

	require.NoError(t, rows.Err())

	return out
}

// waitingJobs returns the run_at of each pending investigate job that
// holds the group's unique key.
func (h *harness) waitingJobs(t *testing.T) []time.Time {
	t.Helper()

	rows, err := h.pool.Query(t.Context(),
		"SELECT run_at FROM jobs WHERE kind = 'investigate' AND status = 'pending' AND unique_key IS NOT NULL ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []time.Time

	for rows.Next() {
		var at time.Time
		require.NoError(t, rows.Scan(&at))

		out = append(out, at.UTC())
	}

	require.NoError(t, rows.Err())

	return out
}

// wakeWaitingJob makes the waiting investigate job ready now, as the real
// clock reaching its run_at would, and completes the job that moved it.
func (h *harness) wakeWaitingJob(t *testing.T) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET status = 'done' WHERE kind = 'investigate' AND status = 'running'")
	require.NoError(t, err)

	tag, err := h.pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() - interval '1 second' WHERE kind = 'investigate' AND status = 'pending'")
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}
