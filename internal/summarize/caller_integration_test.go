//go:build integration

package summarize

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
)

func TestCallToolSpend(t *testing.T) {
	t.Parallel()

	usage := model.Usage{InputTokens: 1000, OutputTokens: 200}
	answer := modeltest.ToolUse(usage, modeltest.Call("c1", testToolName, `{"text":"Check the deploy."}`))

	tests := []struct {
		name  string
		daily Daily
		route string
		// calls is whether the model is called, and wantSpend what the
		// route has spent in the end, in micro-dollars.
		calls     bool
		wantErr   error
		wantSpend int64
	}{
		{name: "a call adds its cost to the route", route: "payments", calls: true, wantSpend: 15_000},
		{name: "a route with no spend yet gets its first row", route: "billing", calls: true, wantSpend: 6000},
		{name: "a call under the route's cap goes through", route: "payments", daily: Daily{RouteMicros: map[string]int64{"payments": 9001}}, calls: true, wantSpend: 15_000},
		{name: "a route at its cap is refused", route: "payments", daily: Daily{RouteMicros: map[string]int64{"payments": 9000}}, wantErr: ErrOverCap, wantSpend: 9000},
		{name: "all routes at the global cap are refused", route: "search", daily: Daily{MaxCostMicros: 14_000}, wantErr: ErrOverCap, wantSpend: 5000},
		{name: "the global cap above the total lets a call through", route: "search", daily: Daily{MaxCostMicros: 14_001}, calls: true, wantSpend: 11_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			var turns []modeltest.Turn
			if tt.calls {
				turns = append(turns, answer)
			}

			conn := modeltest.New(t, model.Capabilities{ToolChoice: true}, turns...)
			conn.ExpectAllUsed()

			c := New(Options{
				Connector: conn, Name: testConnector, Model: testModel, Prices: testPrices, Daily: tt.daily,
				Now: func() time.Time { return testNow },
			})

			got, err := c.CallTool(t.Context(), pool, Request{
				Route: tt.route, System: "Draft a lesson.", Tool: testTool(),
				Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "The feedback."}}}},
			})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.JSONEq(t, `{"text":"Check the deploy."}`, string(got))
			}

			require.Equal(t, tt.wantSpend, routeSpend(t, pool, tt.route))
		})
	}
}

func TestCallToolInTransaction(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	conn := modeltest.New(t, model.Capabilities{ToolChoice: true},
		modeltest.ToolUse(model.Usage{InputTokens: 1000, OutputTokens: 200}, modeltest.Call("c1", testToolName, `{}`)))
	c := New(Options{Connector: conn, Name: testConnector, Model: testModel, Prices: testPrices, Now: func() time.Time { return testNow }})

	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)

	_, err = c.CallTool(t.Context(), tx, Request{Route: "payments", System: "Draft.", Tool: testTool()})
	require.NoError(t, err)

	// The spend is in the transaction, and goes with it.
	require.Equal(t, int64(9000), routeSpend(t, pool, "payments"))
	require.Equal(t, int64(15_000), txSpend(t, tx, "payments"))
	require.NoError(t, tx.Rollback(t.Context()))
	require.Equal(t, int64(9000), routeSpend(t, pool, "payments"))
}

// routeSpend returns what route has spent on the test day.
func routeSpend(t *testing.T, pool *pgxpool.Pool, route string) int64 {
	t.Helper()

	var micros int64

	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT coalesce(sum(cost_micros), 0)::bigint FROM spend_daily WHERE route = $1 AND day = '2026-11-01'", route).Scan(&micros))

	return micros
}

// txSpend is routeSpend inside tx.
func txSpend(t *testing.T, tx pgx.Tx, route string) int64 {
	t.Helper()

	var micros int64

	require.NoError(t, tx.QueryRow(t.Context(),
		"SELECT coalesce(sum(cost_micros), 0)::bigint FROM spend_daily WHERE route = $1 AND day = '2026-11-01'", route).Scan(&micros))

	return micros
}
