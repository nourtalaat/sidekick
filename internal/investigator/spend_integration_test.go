//go:build integration

package investigator

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// TestDailySpend checks that each model step is added to spend_daily in
// its own transaction, with the cost worked out by hand, and that the
// gauge of the day's spend reads it.
func TestDailySpend(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{
		turn(search("c1")),
		turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
	}})

	require.NoError(t, h.run(t))

	// Each turn: 1,000 input tokens at $4, 200 output tokens at $20, 5,000
	// cache reads at $0.20 and 300 cache writes at $5 per million tokens:
	// 4,000 + 4,000 + 1,000 + 1,500 micro-dollars.
	const handCost = 2 * (4_000 + 4_000 + 1_000 + 1_500)

	inv := h.investigation(t)
	require.EqualValues(t, handCost, inv.CostMicros)

	var (
		route, connector, modelID string
		cost, input, output       int64
	)

	require.NoError(t, h.pool.QueryRow(t.Context(),
		"SELECT route, connector, model, cost_micros, input_tokens, output_tokens FROM spend_daily WHERE day = $1",
		h.handler.today()).Scan(&route, &connector, &modelID, &cost, &input, &output))
	require.Equal(t, "payments", route)
	require.Equal(t, "claude", connector)
	require.Equal(t, testModel, modelID)
	require.EqualValues(t, handCost, cost)
	require.Equal(t, 2*testUsage.InputTokens, input)
	require.Equal(t, 2*testUsage.OutputTokens, output)

	require.NoError(t, testutil.GatherAndCompare(h.registry, strings.NewReader(`
# HELP sidekick_daily_spend_usd What each route's investigations have spent today on model calls, in US dollars, from the price table. The day starts at midnight in budgets.daily.timeZone.
# TYPE sidekick_daily_spend_usd gauge
sidekick_daily_spend_usd{route="payments"} 0.021
`), MetricDailySpend))
}
