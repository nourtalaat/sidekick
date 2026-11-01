package gate

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// schemaInfix follows bench.SchemaPrefix in the name of a gate's schema, and
// the proposal's ID follows it, so a restarted gate drops what it left.
const schemaInfix = "gate_"

// closeTimeout bounds dropping the gate's schema, which runs even when the
// job's context has ended.
const closeTimeout = time.Minute

// microsPerUSD converts the dollars of the config to micro-dollars.
const microsPerUSD = 1_000_000

// idHalf is the number of bytes of a proposal ID that seed its resampling.
const idHalf = 8

// errDailyCap is returned, to retry the job later, when the day's spending
// cap is reached. The finished runs are kept, so the gate carries on from
// there.
var errDailyCap = errors.New("the daily spending cap is reached")

// Daily are the daily spending caps, as the investigator holds them.
type Daily struct {
	// Location is the time zone whose midnight starts a new day. Nil means
	// UTC.
	Location *time.Location
	// MaxCostMicros is the global cap, and zero means none.
	MaxCostMicros int64
}

// Options set up a Gate. Every field but the loggers and Now is required.
type Options struct {
	// Pool is the database of the server: the proposals, the benchmark
	// results and the daily spend are in its default schema.
	Pool *pgxpool.Pool
	// Config is the loaded config. The gate runs with its model connectors
	// and roles, its report rules and its per-investigation limits, as
	// sidekick bench run does.
	Config *config.Config
	// Registries are where plugin types are looked up. The zero value
	// means the global ones.
	Registries config.Registries
	// DatabaseURL returns the URL of the server's database, in which each
	// gate makes a schema of its own.
	DatabaseURL func(ctx context.Context) (string, error)
	// Strategies holds the strategies in effect. The gate runs two
	// versions of one of them on top of the others.
	Strategies strategy.Source
	// Scenarios are the scenarios the gate chooses from.
	Scenarios ScenarioSource
	// Daily are the caps the gate stays under.
	Daily Daily
	// Logger gets a line per step of a gate. Nil logs nothing.
	Logger *slog.Logger
	// ComponentLogger gets the warnings of the benchmark's own parts. Nil
	// logs nothing.
	ComponentLogger *slog.Logger
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Gate runs bench_gate jobs. It is safe for concurrent use.
type Gate struct {
	opts    Options
	results *bench.ResultStore
	audit   *audit.Writer
	log     *slog.Logger
	now     func() time.Time
}

var _ queue.Handler = (*Gate)(nil)

// Detail is what a gate stores with its proposal, in gate_detail: the
// decision with the numbers it rests on, and what the gate cost.
type Detail struct {
	bench.GateDecision
	// Repeats is how many times each scenario ran in each arm.
	Repeats int `json:"repeats"`
	// CostMicros is what the runs and their grading cost, and
	// MaxCostMicros the cap the gate had.
	CostMicros    int64 `json:"costMicros"`
	MaxCostMicros int64 `json:"maxCostMicros"`
	// Error says why a gate ended with GateError.
	Error string `json:"error,omitempty"`
}

// New returns a Gate.
func New(opts Options) (*Gate, error) {
	switch {
	case opts.Pool == nil:
		return nil, errors.New("new gate: Pool is required")
	case opts.Config == nil:
		return nil, errors.New("new gate: Config is required")
	case opts.DatabaseURL == nil:
		return nil, errors.New("new gate: DatabaseURL is required")
	case opts.Strategies == nil:
		return nil, errors.New("new gate: Strategies is required")
	case opts.Scenarios == nil:
		return nil, errors.New("new gate: Scenarios is required")
	}

	g := &Gate{opts: opts, results: bench.NewResultStore(opts.Pool), audit: audit.NewWriter(), now: opts.Now, log: opts.Logger}

	if g.now == nil {
		g.now = time.Now
	}

	if g.log == nil {
		g.log = slog.New(slog.DiscardHandler)
	}

	return g, nil
}

// Handle runs the gate of the proposal in the job. It returns nil once the
// gate has ended, with its result stored. A failure that may pass, such as a
// model provider that is down or a day's spending cap that is reached, is
// returned, so the queue tries again; the runs that finished are kept, and
// the next try carries on from there.
func (g *Gate) Handle(ctx context.Context, job db.Job) error {
	var payload proposals.BenchGatePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("%w: decode the payload of job %d: %w", queue.ErrGiveUp, job.ID, err)
	}

	prop, err := db.New(g.opts.Pool).GetStrategyProposal(ctx, payload.ProposalID)
	if errors.Is(err, pgx.ErrNoRows) {
		g.log.WarnContext(ctx, "the gate's proposal does not exist", "proposal", payload.ProposalID)

		return nil
	}

	if err != nil {
		return fmt.Errorf("read proposal %s: %w", payload.ProposalID, err)
	}

	if prop.Status != proposals.StatusPending && prop.Status != proposals.StatusBenchmarking {
		g.log.InfoContext(ctx, "the proposal is not waiting for its gate", "proposal", prop.ID, "status", prop.Status)

		return nil
	}

	if err := g.run(ctx, prop); err != nil {
		return fmt.Errorf("gate of proposal %s: %w", prop.ID, err)
	}

	return nil
}

// OnDead ends the gate of a job that died, so its proposal does not wait for
// a gate that will not run again. An admin can send the proposal through the
// gate again by editing it.
func (g *Gate) OnDead(ctx context.Context, job db.Job) error {
	var payload proposals.BenchGatePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode the payload of job %d: %w", job.ID, err)
	}

	lastError := ""
	if job.LastError != nil {
		lastError = *job.LastError
	}

	detail := Detail{
		GateDecision: bench.GateDecision{Result: bench.GateError, Reason: "The gate could not finish. Send the proposal through the gate again by saving it."},
		Error:        lastError,
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode the detail of proposal %s: %w", payload.ProposalID, err)
	}

	err = pgx.BeginFunc(ctx, g.opts.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)

		rows, err := q.SetStrategyProposalGateError(ctx, db.SetStrategyProposalGateErrorParams{ID: payload.ProposalID, GateDetail: raw})
		if err != nil {
			return fmt.Errorf("end the gate: %w", err)
		}

		if rows == 0 {
			return nil
		}

		if err := q.DeleteStrategyGateRuns(ctx, payload.ProposalID); err != nil {
			return fmt.Errorf("delete the kept runs: %w", err)
		}

		return g.auditEnd(ctx, tx, payload.ProposalID, detail)
	})
	if err != nil {
		return fmt.Errorf("end the gate of proposal %s: %w", payload.ProposalID, err)
	}

	return nil
}

// run chooses the scenarios, runs both arms, decides and stores the result.
func (g *Gate) run(ctx context.Context, prop db.StrategyProposal) error {
	settings := g.opts.Config.Bench.Gate
	seed := seedOf(prop.ID)
	opts := bench.GateOptions{MaxDrop: settings.MaxDrop, Seed: seed}

	a, err := g.arms(prop)
	if err != nil {
		return fmt.Errorf("%w: %w", queue.ErrGiveUp, err)
	}

	selected, err := g.choose(ctx, a)
	if err != nil {
		return fmt.Errorf("choose the scenarios: %w", err)
	}

	if short, ok := bench.CoverageDecision(len(selected), opts); ok {
		g.log.InfoContext(ctx, "the gate ends without a run: too few scenarios cover the strategy", "proposal", prop.ID, "strategy", a.name, "scenarios", len(selected))

		return g.finish(ctx, prop, Detail{GateDecision: short, Repeats: settings.MinRepeats, MaxCostMicros: capMicros(settings)}, nil, nil)
	}

	r := &gateRun{gate: g, prop: prop, arms: a, scenarios: selected, settings: settings, opts: opts, budget: bench.NewBudget(capMicros(settings))}

	return r.execute(ctx)
}

// finish stores how the gate ended, with the benchmark runs of its arms when
// there are any, and deletes the runs it kept. A proposal that was rejected or
// edited while the gate ran is left as it is.
func (g *Gate) finish(ctx context.Context, prop db.StrategyProposal, detail Detail, proposalRun, baselineRun *uuid.UUID) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode the detail of proposal %s: %w", prop.ID, err)
	}

	status := proposals.StatusFailed
	if detail.Result.Passed() {
		status = proposals.StatusPassed
	}

	result := string(detail.Result)

	err = pgx.BeginFunc(ctx, g.opts.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)

		rows, err := q.SetStrategyProposalGate(ctx, db.SetStrategyProposalGateParams{
			ID: prop.ID, Status: status, GateResult: &result, GateDetail: raw, BenchRunID: proposalRun, BaselineRunID: baselineRun,
		})
		if err != nil {
			return fmt.Errorf("store the result: %w", err)
		}

		if err := q.DeleteStrategyGateRuns(ctx, prop.ID); err != nil {
			return fmt.Errorf("delete the kept runs: %w", err)
		}

		if rows == 0 {
			g.log.InfoContext(ctx, "the proposal changed while its gate ran, so the result is dropped", "proposal", prop.ID)

			return nil
		}

		return g.auditEnd(ctx, tx, prop.ID, detail)
	})
	if err != nil {
		return fmt.Errorf("end the gate: %w", err)
	}

	g.log.InfoContext(ctx, "the gate ended", "proposal", prop.ID, "result", result, "mean", detail.Mean, "lower", detail.Interval.Lower)

	return nil
}

// auditEnd writes the audit entry of a gate that ended.
func (g *Gate) auditEnd(ctx context.Context, tx pgx.Tx, id uuid.UUID, detail Detail) error {
	err := g.audit.Write(ctx, tx, audit.Entry{
		ActorKind: audit.ActorSystem, Action: proposals.ActionGate, TargetKind: proposals.KindProposal, TargetID: id.String(),
		New: map[string]any{
			"result": detail.Result, "mean": detail.Mean, "lower": detail.Interval.Lower, "upper": detail.Interval.Upper,
			"maxDrop": detail.MaxDrop, "scenarios": len(detail.Scenarios), "costMicros": detail.CostMicros,
		},
	})
	if err != nil {
		return fmt.Errorf("audit the end of the gate: %w", err)
	}

	return nil
}

// arms are the two versions of the strategy a gate compares, with the
// strategies each arm runs.
type arms struct {
	name               string
	baseline, proposal strategy.Strategy
	baselineSet        *strategy.Set
	proposalSet        *strategy.Set
}

// arms parses the proposal's two bodies and puts each in the strategies in
// effect, in place of the strategy of its name.
func (g *Gate) arms(prop db.StrategyProposal) (arms, error) {
	if prop.BaseBody == nil || prop.Body == nil || prop.BaseOrigin == nil {
		return arms{}, fmt.Errorf("proposal %s has no draft", prop.ID)
	}

	base, problems := strategy.Check([]byte(*prop.BaseBody))
	if len(problems) > 0 {
		return arms{}, fmt.Errorf("the base version of proposal %s is not valid: %s", prop.ID, problems[0])
	}

	base.Version, base.Origin, base.Body = prop.BaseVersion, strategy.Origin(*prop.BaseOrigin), *prop.BaseBody

	next, problems := strategy.Check([]byte(*prop.Body))
	if len(problems) > 0 {
		return arms{}, fmt.Errorf("the body of proposal %s is not valid: %s", prop.ID, problems[0])
	}

	next.Version, next.Origin, next.Body = proposalVersion(prop.ID), strategy.Runtime, *prop.Body

	current := g.opts.Strategies.Current()

	return arms{
		name: prop.Name, baseline: base, proposal: next,
		baselineSet: current.With(base), proposalSet: current.With(next),
	}, nil
}

// proposalVersion is the version the proposal's arm records, so its runs are
// told from those of a saved version.
func proposalVersion(id uuid.UUID) string {
	return "proposal:" + strings.ReplaceAll(id.String(), "-", "")[:proposalVersionLength]
}

// proposalVersionLength is how many characters of the proposal's ID its
// version keeps.
const proposalVersionLength = 8

// seedOf returns the seed of a proposal's resampling: part of its ID, so the
// interval can be worked out again from the stored numbers.
func seedOf(id uuid.UUID) uint64 {
	return binary.BigEndian.Uint64(id[idHalf:])
}

// capMicros returns the cost cap of a gate in micro-dollars.
func capMicros(s config.BenchGate) int64 {
	return int64(math.Round(s.MaxCostUSD * microsPerUSD))
}

// schemaName returns the name of the schema the gate of a proposal runs in.
func schemaName(id uuid.UUID) string {
	return bench.SchemaPrefix + schemaInfix + strings.ReplaceAll(id.String(), "-", "")
}
