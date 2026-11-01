package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The two arms of a gate, as strategy_gate_runs.arm spells them.
const (
	armBaseline = "baseline"
	armProposal = "proposal"
)

// triggeredByGate is bench_runs.triggered_by of a gate's runs.
const triggeredByGate = "gate"

// runKey identifies one run of a gate.
type runKey struct {
	arm      string
	scenario string
	repeat   int
}

// gateRun is one execution of a gate: the proposal, the scenarios it runs,
// what the earlier tries finished, and what it has spent.
type gateRun struct {
	gate      *Gate
	prop      db.StrategyProposal
	arms      arms
	scenarios []bench.Scenario
	settings  config.BenchGate
	opts      bench.GateOptions
	budget    *bench.Budget

	// kept are the runs earlier tries finished.
	kept map[runKey]bench.RunResult
	// investigate is the connector and model the runs are priced to in the
	// daily spend, and judge those of grading.
	investigate, judge bench.ConnectorInfo
}

// execute runs both arms in a schema of their own, scores them, decides,
// stores the benchmark runs and ends the gate.
func (r *gateRun) execute(ctx context.Context) error {
	g := r.gate

	hash, err := setHash(r.scenarios)
	if err != nil {
		return fmt.Errorf("hash the scenarios: %w", err)
	}

	err = r.resume(ctx, hash)
	if errors.Is(err, errSuperseded) {
		g.log.InfoContext(ctx, "the proposal changed before its gate began", "proposal", r.prop.ID)

		return nil
	}

	if err != nil {
		return fmt.Errorf("resume the gate: %w", err)
	}

	database, err := r.openSchema(ctx)
	if errors.Is(err, bench.ErrCannotCreateSchema) {
		g.log.ErrorContext(ctx, "the gate cannot run: the database role needs CREATE on the database", "proposal", r.prop.ID, "error", err.Error())

		return g.finish(ctx, r.prop, r.errorDetail("The database role of Sidekick may not create the schema the gate runs in. Give it CREATE on the database, then save the proposal again."), nil, nil)
	}

	if err != nil {
		return fmt.Errorf("open the gate's schema: %w", err)
	}

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()

		if cerr := database.Close(closeCtx); cerr != nil {
			g.log.ErrorContext(ctx, "could not drop the gate's schema", "proposal", r.prop.ID, "error", cerr.Error())
		}
	}()

	baseline, err := g.runner(ctx, r.scenarios, r.arms.baselineSet, r.hooks(armBaseline), r.budget)
	if err != nil {
		return fmt.Errorf("build the baseline runner: %w", err)
	}

	proposal, err := g.runner(ctx, r.scenarios, r.arms.proposalSet, r.hooks(armProposal), r.budget)
	if err != nil {
		return fmt.Errorf("build the proposal runner: %w", err)
	}

	info := baseline.Info()
	r.investigate, r.judge = info.Connectors[model.RoleInvestigate], info.Connectors[model.RoleJudge]

	baseResults, err := baseline.Run(ctx, database)
	if err != nil {
		return r.stopped(ctx, fmt.Errorf("run the baseline: %w", err))
	}

	propResults, err := proposal.Run(ctx, database)
	if err != nil {
		return r.stopped(ctx, fmt.Errorf("run the proposal: %w", err))
	}

	if err := r.score(ctx, baseline, baseResults, propResults); err != nil {
		return r.stopped(ctx, err)
	}

	decision := bench.DecideGate(baseResults.Scenarios, propResults.Scenarios, r.opts)

	baselineRun, err := r.save(ctx, baseline, baseResults, armBaseline)
	if err != nil {
		return fmt.Errorf("store the baseline run: %w", err)
	}

	proposalRun, err := r.save(ctx, proposal, propResults, armProposal)
	if err != nil {
		return fmt.Errorf("store the proposal run: %w", err)
	}

	return g.finish(ctx, r.prop, r.detail(decision, ""), &proposalRun, &baselineRun)
}

// resume loads the runs earlier tries finished, after dropping them when the
// scenarios are not the same any more, and marks the proposal as running its
// gate.
func (r *gateRun) resume(ctx context.Context, hash string) error {
	q := db.New(r.gate.opts.Pool)

	if r.prop.GateSetHash != nil && *r.prop.GateSetHash != hash {
		r.gate.log.InfoContext(ctx, "the scenarios changed since the gate began, so it starts over", "proposal", r.prop.ID)

		if err := q.DeleteStrategyGateRuns(ctx, r.prop.ID); err != nil {
			return fmt.Errorf("delete the kept runs: %w", err)
		}
	}

	rows, err := q.SetStrategyProposalBenchmarking(ctx, db.SetStrategyProposalBenchmarkingParams{ID: r.prop.ID, GateSetHash: &hash})
	if err != nil {
		return fmt.Errorf("mark the gate as running: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("proposal %s changed before its gate began: %w", r.prop.ID, errSuperseded)
	}

	kept, err := q.ListStrategyGateRuns(ctx, r.prop.ID)
	if err != nil {
		return fmt.Errorf("list the kept runs: %w", err)
	}

	r.kept = make(map[runKey]bench.RunResult, len(kept))

	for _, k := range kept {
		var run bench.RunResult
		if err := json.Unmarshal(k.Result, &run); err != nil {
			return fmt.Errorf("decode the kept %s run %d of %s: %w", k.Arm, k.Repeat, k.ScenarioID, err)
		}

		r.kept[runKey{arm: k.Arm, scenario: k.ScenarioID, repeat: int(k.Repeat)}] = run
	}

	return nil
}

// errSuperseded means the proposal was rejected or edited, so the gate has
// nothing left to do.
var errSuperseded = errors.New("the proposal is no longer waiting for its gate")

// openSchema opens the schema the gate's investigations run in, and drops one
// that an earlier, killed try left.
func (r *gateRun) openSchema(ctx context.Context) (*bench.Database, error) {
	url, err := r.gate.opts.DatabaseURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the database URL: %w", err)
	}

	database, err := bench.OpenSchemaNamed(ctx, url, schemaName(r.prop.ID), r.gate.log)
	if err != nil {
		return nil, fmt.Errorf("open the schema: %w", err)
	}

	return database, nil
}

// hooks returns the hooks of one arm: reuse the runs earlier tries finished,
// stop while the day's cap is reached, and keep each run that ends.
func (r *gateRun) hooks(arm string) bench.Hooks {
	return bench.Hooks{
		Done: func(scenarioID string, run int) (bench.RunResult, bool) {
			kept, ok := r.kept[runKey{arm: arm, scenario: scenarioID, repeat: run}]

			return kept, ok
		},
		Before: func(ctx context.Context, _ string, _ int) error {
			if err := r.checkStillWaiting(ctx); err != nil {
				return err
			}

			return r.checkDailyCap(ctx)
		},
		After: func(ctx context.Context, scenarioID string, run bench.RunResult) error {
			return r.keep(ctx, arm, scenarioID, run)
		},
	}
}

// checkStillWaiting returns errSuperseded when the proposal is no longer
// waiting for its gate, for example because an admin rejected it, so the gate
// stops spending on it.
func (r *gateRun) checkStillWaiting(ctx context.Context) error {
	prop, err := db.New(r.gate.opts.Pool).GetStrategyProposal(ctx, r.prop.ID)
	if err != nil {
		return fmt.Errorf("read proposal %s: %w", r.prop.ID, err)
	}

	if prop.Status != proposals.StatusBenchmarking {
		return errSuperseded
	}

	return nil
}

// checkDailyCap returns errDailyCap when the day's spending cap is reached.
func (r *gateRun) checkDailyCap(ctx context.Context) error {
	daily := r.gate.opts.Daily
	if daily.MaxCostMicros <= 0 {
		return nil
	}

	spent, err := db.New(r.gate.opts.Pool).GetDailySpend(ctx, db.GetDailySpendParams{Route: bench.RouteName, Day: r.day()})
	if err != nil {
		return fmt.Errorf("read the day's spend: %w", err)
	}

	if spent.TotalMicros >= daily.MaxCostMicros {
		return errDailyCap
	}

	return nil
}

// keep stores a finished run and adds its cost to the day's spend, in one
// transaction, so a run that is kept is paid for once.
func (r *gateRun) keep(ctx context.Context, arm, scenarioID string, run bench.RunResult) error {
	raw, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode run %d of %s: %w", run.Run, scenarioID, err)
	}

	err = pgx.BeginFunc(ctx, r.gate.opts.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)

		err := q.InsertStrategyGateRun(ctx, db.InsertStrategyGateRunParams{
			ProposalID: r.prop.ID, Arm: arm, ScenarioID: scenarioID, Repeat: int32(run.Run), Result: raw, CostMicros: run.CostMicros, //nolint:gosec // A run number is far below the limit.
		})
		if err != nil {
			return fmt.Errorf("keep the run: %w", err)
		}

		return r.addSpend(ctx, tx, r.investigate, run.CostMicros, run.InputTokens, run.OutputTokens)
	})
	if err != nil {
		return fmt.Errorf("keep run %d of %s: %w", run.Run, scenarioID, err)
	}

	return nil
}

// addSpend adds a cost to the day's spend, under the route of benchmark runs
// and the given connector and model.
func (r *gateRun) addSpend(ctx context.Context, q db.DBTX, c bench.ConnectorInfo, cost, in, out int64) error {
	err := db.New(q).AddSpend(ctx, db.AddSpendParams{
		Day: r.day(), Route: bench.RouteName, Connector: c.Connector, Model: c.Model,
		CostMicros: cost, InputTokens: in, OutputTokens: out,
	})
	if err != nil {
		return fmt.Errorf("add to the daily spend: %w", err)
	}

	return nil
}

// day returns today in the time zone of the daily caps, as spend_daily
// stores it.
func (r *gateRun) day() pgtype.Date {
	loc := r.gate.opts.Daily.Location
	if loc == nil {
		loc = time.UTC
	}

	y, m, d := r.gate.now().In(loc).Date()

	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

// score grades both arms with the judge, within the budget, and adds the
// cost of grading to the day's spend.
func (r *gateRun) score(ctx context.Context, runner *bench.Runner, baseline, proposal *bench.Results) error {
	judge, err := runner.Judge()
	if err != nil {
		return fmt.Errorf("get the judge: %w", err)
	}

	grader := bench.NewBudgetGrader(judge, r.budget)

	for _, arm := range []struct {
		name    string
		results *bench.Results
	}{{armBaseline, baseline}, {armProposal, proposal}} {
		scoreErr := bench.Score(ctx, arm.results, bench.ScoreOptions{Grader: grader, Seed: r.opts.Seed})

		if cost := judgeCost(arm.results); cost > 0 {
			if err := r.addSpend(ctx, r.gate.opts.Pool, r.judge, cost, 0, 0); err != nil {
				return fmt.Errorf("count the grading of the %s arm: %w", arm.name, err)
			}
		}

		if scoreErr != nil {
			return fmt.Errorf("score the %s arm: %w", arm.name, scoreErr)
		}
	}

	return nil
}

// judgeCost returns what grading cost, once Score has run, or zero when it
// stopped before the totals were made.
func judgeCost(results *bench.Results) int64 {
	var total int64

	for _, sc := range results.Scenarios {
		for _, run := range sc.Runs {
			if run.RootCause != nil {
				total += run.RootCause.CostMicros
			}
		}
	}

	return total
}

// save stores the scored results of one arm as a benchmark run, and returns
// its ID.
func (r *gateRun) save(ctx context.Context, runner *bench.Runner, results *bench.Results, arm string) (uuid.UUID, error) {
	build := buildinfo.Current()

	info := runner.Info()
	info.Label = fmt.Sprintf("gate %s %s", r.prop.ID, arm)
	info.SidekickVersion = build.Version
	info.GitSHA = build.Commit
	info.ScenarioSet = "gate:" + r.prop.Name
	info.TriggeredBy = triggeredByGate

	rep, err := results.Report(info, nil)
	if err != nil {
		return uuid.Nil, fmt.Errorf("make the report: %w", err)
	}

	id, err := r.gate.results.Save(ctx, rep)
	if err != nil {
		return uuid.Nil, fmt.Errorf("save the report: %w", err)
	}

	return id, nil
}

// stopped decides what to do with the error that stopped a gate: a gate that
// reached its cost cap ends as over budget, a proposal that changed is left
// alone, and anything else is returned, so the queue tries the gate again.
func (r *gateRun) stopped(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, bench.ErrOverBudget):
		r.gate.log.InfoContext(ctx, "the gate stops: it would cost more than bench.gate.maxCostUSD", "proposal", r.prop.ID, "error", err.Error())

		decision := bench.GateDecision{
			Result: bench.GateOverBudget, MaxDrop: r.opts.MaxDrop, Seed: r.opts.Seed, Resamples: bench.GateResamples,
			Reason: "The gate stopped because it would have cost more than bench.gate.maxCostUSD. Raise the cap, or cover the strategy with fewer scenarios.",
		}

		return r.gate.finish(ctx, r.prop, r.detail(decision, ""), nil, nil)
	case errors.Is(err, errSuperseded):
		r.gate.log.InfoContext(ctx, "the gate stops: the proposal is no longer waiting for it", "proposal", r.prop.ID)

		if err := db.New(r.gate.opts.Pool).DeleteStrategyGateRuns(ctx, r.prop.ID); err != nil {
			return fmt.Errorf("delete the kept runs: %w", err)
		}

		return nil
	default:
		return err
	}
}

// detail returns the stored detail of a gate that ended with decision.
func (r *gateRun) detail(decision bench.GateDecision, message string) Detail {
	return Detail{
		GateDecision: decision, Repeats: r.settings.MinRepeats, CostMicros: r.budget.Spent(),
		MaxCostMicros: capMicros(r.settings), Error: message,
	}
}

// errorDetail returns the detail of a gate that cannot run.
func (r *gateRun) errorDetail(message string) Detail {
	return r.detail(bench.GateDecision{Result: bench.GateError, MaxDrop: r.opts.MaxDrop, Seed: r.opts.Seed, Resamples: bench.GateResamples, Reason: message}, message)
}
