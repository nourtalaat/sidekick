//go:build integration

package gate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/gate"
	"github.com/nourtalaat/sidekick/internal/proposals"
)

func TestGateDecidesAProposal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus string
		wantResult bench.GateResult
	}{
		{name: "a proposal that scores like the current version passes", body: betterBody, wantStatus: proposals.StatusPassed, wantResult: bench.GatePassed},
		{name: "a proposal that scores much lower fails", body: worseBody, wantStatus: proposals.StatusFailed, wantResult: bench.GateFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, envOptions{})
			id := insertProposal(t, e.pool, tt.body)

			require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

			p := loadProposal(t, e.pool, id)
			require.Equal(t, tt.wantStatus, p.Status)
			require.Equal(t, new(string(tt.wantResult)), p.GateResult)
			require.NotNil(t, p.BenchRunID)
			require.NotNil(t, p.BaselineRunID)
			require.NotEqual(t, *p.BenchRunID, *p.BaselineRunID)

			d := detailOf(t, p)
			require.Equal(t, tt.wantResult, d.Result)
			require.Len(t, d.Scenarios, testScenarios)
			require.Equal(t, testRepeats, d.Repeats)
			require.Equal(t, bench.GateResamples, d.Resamples)
			require.Positive(t, d.CostMicros)
			require.LessOrEqual(t, d.Interval.Lower, d.Interval.Upper)

			// Each arm ran every scenario testRepeats times, and both are
			// stored as benchmark runs for the review page.
			runs := 2 * testScenarios * testRepeats
			require.Equal(t, runs, e.conn.investigations())
			require.Equal(t, 2, count(t, e.pool, "SELECT count(*) FROM bench_runs WHERE triggered_by = 'gate'"))
			require.Equal(t, runs, count(t, e.pool, "SELECT count(*) FROM bench_results"))

			require.Equal(t, 1, count(t, e.pool, "SELECT count(*) FROM audit_log WHERE action = $1 AND actor_kind = $2", proposals.ActionGate, audit.ActorSystem))
			require.Zero(t, count(t, e.pool, "SELECT count(*) FROM strategy_gate_runs"), "the kept runs are deleted when the gate ends")
			require.Empty(t, benchSchemas(t, e.pool), "the gate's schema is dropped")

			// The runs are paid for in the day's spend, under the route of
			// benchmark runs.
			require.Positive(t, count(t, e.pool, "SELECT coalesce(sum(cost_micros), 0)::int FROM spend_daily WHERE route = $1", bench.RouteName))
		})
	}
}

func TestGateRecordsTheStrategyVersionsOfBothArms(t *testing.T) {
	t.Parallel()

	e := newEnv(t, envOptions{})
	id := insertProposal(t, e.pool, betterBody)

	require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

	p := loadProposal(t, e.pool, id)

	var baseline, proposal string
	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT strategies ->> $2 FROM bench_runs WHERE id = $1", p.BaselineRunID, strategyName).Scan(&baseline))
	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT strategies ->> $2 FROM bench_runs WHERE id = $1", p.BenchRunID, strategyName).Scan(&proposal))

	require.Equal(t, "runtime:1", baseline)
	require.Regexp(t, `^proposal:[0-9a-f]{8}$`, proposal)
}

func TestGateWithoutCoverageSpendsNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		scenarios  func(t *testing.T) []bench.Scenario
		wantResult bench.GateResult
	}{
		{
			name:       "no scenario covers the strategy",
			scenarios:  func(*testing.T) []bench.Scenario { return []bench.Scenario{} },
			wantResult: bench.GateNoBenchmarkCoverage,
		},
		{
			name: "scenarios of other strategies do not cover it",
			scenarios: func(t *testing.T) []bench.Scenario {
				t.Helper()

				scs := coveringScenarios(t, 4)
				for i := range scs {
					scs[i].Strategy = "service-down"
				}

				return scs
			},
			wantResult: bench.GateNoBenchmarkCoverage,
		},
		{
			name: "two scenarios are too few",
			scenarios: func(t *testing.T) []bench.Scenario {
				t.Helper()

				return coveringScenarios(t, bench.MinGateScenarios-1)
			},
			wantResult: bench.GateNotEnoughEvidence,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, envOptions{scenarios: tt.scenarios(t)})
			id := insertProposal(t, e.pool, betterBody)

			require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

			p := loadProposal(t, e.pool, id)
			require.Equal(t, proposals.StatusFailed, p.Status)
			require.Equal(t, new(string(tt.wantResult)), p.GateResult)
			require.Nil(t, p.BenchRunID)
			require.Zero(t, e.conn.modelCalls(), "no model call is made")
			require.Zero(t, count(t, e.pool, "SELECT count(*) FROM bench_runs"))
			require.Zero(t, count(t, e.pool, "SELECT count(*) FROM spend_daily"))
			require.NotEmpty(t, detailOf(t, p).Reason)
		})
	}
}

func TestGateStopsAtItsCostCap(t *testing.T) {
	t.Parallel()

	// A run costs far more than a thousandth of a cent, so the gate does one
	// run and then stops before the next.
	e := newEnv(t, envOptions{maxCostUSD: 0.00001})
	id := insertProposal(t, e.pool, betterBody)

	require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

	p := loadProposal(t, e.pool, id)
	require.Equal(t, proposals.StatusFailed, p.Status)
	require.Equal(t, new(string(bench.GateOverBudget)), p.GateResult)
	require.Nil(t, p.BenchRunID, "nothing is stored as a benchmark run")
	require.Equal(t, 1, e.conn.investigations(), "one run, then the next would pass the cap")
	require.Zero(t, count(t, e.pool, "SELECT count(*) FROM bench_runs"))
	require.Positive(t, detailOf(t, p).CostMicros)
	require.Empty(t, benchSchemas(t, e.pool))
}

func TestGateCarriesOnAfterAnInterruption(t *testing.T) {
	t.Parallel()

	const total = 2 * testScenarios * testRepeats

	// The provider is down from the fifth investigation on.
	e := newEnv(t, envOptions{failFrom: 5})
	id := insertProposal(t, e.pool, betterBody)

	err := e.gate.Handle(t.Context(), jobFor(t, id))
	require.ErrorIs(t, err, bench.ErrProviderUnreachable, "the job fails, so the queue tries again")

	p := loadProposal(t, e.pool, id)
	require.Equal(t, proposals.StatusBenchmarking, p.Status)
	require.Equal(t, 4, count(t, e.pool, "SELECT count(*) FROM strategy_gate_runs"), "the four runs that finished are kept")
	require.Empty(t, benchSchemas(t, e.pool), "the schema is dropped even though the gate failed")

	spentBefore := count(t, e.pool, "SELECT coalesce(sum(cost_micros), 0)::int FROM spend_daily")
	require.Positive(t, spentBefore)

	// The provider is back, and a new try finishes the other runs only.
	e.conn.mu.Lock()
	e.conn.failFrom = 0
	e.conn.started = 0
	e.conn.mu.Unlock()

	require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

	p = loadProposal(t, e.pool, id)
	require.Equal(t, proposals.StatusPassed, p.Status)
	require.Equal(t, total-4, e.conn.investigations(), "the kept runs are not run again")
	require.Zero(t, count(t, e.pool, "SELECT count(*) FROM strategy_gate_runs"))
	require.Equal(t, total, count(t, e.pool, "SELECT count(*) FROM bench_results"), "the stored runs hold the kept runs too")

	// The kept runs were paid for once: the spend grew by the new runs only,
	// and the cost in the detail counts all of them.
	spentAfter := count(t, e.pool, "SELECT coalesce(sum(cost_micros), 0)::int FROM spend_daily")
	require.Greater(t, spentAfter, spentBefore)
	require.EqualValues(t, spentAfter, detailOf(t, p).CostMicros)
}

func TestGateStartsOverWhenTheScenariosChanged(t *testing.T) {
	t.Parallel()

	e := newEnv(t, envOptions{})
	id := insertProposal(t, e.pool, betterBody)

	// An earlier try ran other scenarios and left one run behind.
	_, err := e.pool.Exec(t.Context(), "UPDATE strategy_proposals SET status = 'benchmarking', gate_set_hash = 'another set' WHERE id = $1", id)
	require.NoError(t, err)

	_, err = e.pool.Exec(t.Context(), `INSERT INTO strategy_gate_runs (proposal_id, arm, scenario_id, repeat, result, cost_micros, created_at)
		VALUES ($1, 'baseline', 'checkout-pool-size-a', 1, '{"Run": 1}', 5, now())`, id)
	require.NoError(t, err)

	require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

	require.Equal(t, 2*testScenarios*testRepeats, e.conn.investigations(), "the run left behind is not used")
	require.Equal(t, proposals.StatusPassed, loadProposal(t, e.pool, id).Status)
}

func TestGatePausesWhenTheDailyCapIsReached(t *testing.T) {
	t.Parallel()

	// The cap is one micro-dollar: the first run is allowed, since nothing is
	// spent, and the second is not.
	e := newEnv(t, envOptions{daily: gate.Daily{MaxCostMicros: 1}})
	id := insertProposal(t, e.pool, betterBody)

	err := e.gate.Handle(t.Context(), jobFor(t, id))
	require.Error(t, err, "the job fails, so the queue tries again later")
	require.Equal(t, proposals.StatusBenchmarking, loadProposal(t, e.pool, id).Status)
	require.Equal(t, 1, count(t, e.pool, "SELECT count(*) FROM strategy_gate_runs"))
	require.Equal(t, 1, e.conn.investigations())
}

func TestGateLeavesProposalsThatAreNotWaiting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
	}{
		{name: "rejected", status: proposals.StatusRejected},
		{name: "already failed", status: proposals.StatusFailed},
		{name: "still drafting", status: proposals.StatusDrafting},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, envOptions{})
			id := insertProposal(t, e.pool, betterBody)

			_, err := e.pool.Exec(t.Context(), "UPDATE strategy_proposals SET status = $2, gate_result = CASE WHEN $2 = 'failed' THEN 'failed' END WHERE id = $1", id, tt.status)
			require.NoError(t, err)

			require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))
			require.Zero(t, e.conn.modelCalls())
			require.Equal(t, tt.status, loadProposal(t, e.pool, id).Status)
		})
	}
}

func TestGateOnDeadEndsTheProposal(t *testing.T) {
	t.Parallel()

	e := newEnv(t, envOptions{})
	id := insertProposal(t, e.pool, betterBody)

	_, err := e.pool.Exec(t.Context(), "UPDATE strategy_proposals SET status = 'benchmarking' WHERE id = $1", id)
	require.NoError(t, err)

	job := jobFor(t, id)
	job.LastError = new("the model provider is unreachable")

	require.NoError(t, e.gate.OnDead(t.Context(), job))

	p := loadProposal(t, e.pool, id)
	require.Equal(t, proposals.StatusFailed, p.Status)
	require.Equal(t, new(string(bench.GateError)), p.GateResult)
	require.Equal(t, "the model provider is unreachable", detailOf(t, p).Error)

	// A second call, as the queue may make, changes nothing.
	require.NoError(t, e.gate.OnDead(t.Context(), job))
	require.Equal(t, 1, count(t, e.pool, "SELECT count(*) FROM audit_log WHERE action = $1", proposals.ActionGate))
}

func TestGateStopsSpendingOnAProposalThatWasRejected(t *testing.T) {
	t.Parallel()

	e := newEnv(t, envOptions{})
	id := insertProposal(t, e.pool, betterBody)

	// An admin rejects the proposal while the second run is going.
	e.conn.onStart = func(n int) {
		if n != 2 {
			return
		}

		_, err := e.pool.Exec(t.Context(), "UPDATE strategy_proposals SET status = 'rejected' WHERE id = $1", id)
		require.NoError(t, err)
	}

	require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

	require.Equal(t, 2, e.conn.investigations(), "the gate stops before the third run")
	require.Equal(t, proposals.StatusRejected, loadProposal(t, e.pool, id).Status)
	require.Zero(t, count(t, e.pool, "SELECT count(*) FROM strategy_gate_runs"), "the kept runs are deleted")
	require.Zero(t, count(t, e.pool, "SELECT count(*) FROM bench_runs"))
	require.Empty(t, benchSchemas(t, e.pool))
}
