package bench

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/webhook"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/router"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/mock"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// RouteName is the name of the route every benchmark run goes through.
const RouteName = "bench"

// DefaultRepeat is how many times each scenario runs when Options.Repeat
// is zero.
const DefaultRepeat = 3

const (
	// endpointPrefix starts the webhook endpoint name of each run. The
	// rest is the run's own ID, so a payload delivered again in a later
	// run is a new event rather than a duplicate of the earlier one.
	endpointPrefix = "bench-"
	// pollInterval is how often a run looks at the database for the end
	// of its investigation, and how often the queue looks for jobs when
	// no notification arrives.
	pollInterval = 200 * time.Millisecond
	// routeTimeout bounds how long a run waits for its event to get an
	// investigation.
	routeTimeout = time.Minute
	// closedRunEnded is the closed_reason of the group of a run that
	// ended.
	closedRunEnded = "bench_run_ended"
	// workerPrefix starts the locked_by value of the run's jobs.
	workerPrefix = "sidekick-bench-"
)

var (
	// ErrInfrastructure is wrapped by failures of what a benchmark runs on
	// rather than of the scenarios: the model provider, Docker or the
	// database.
	ErrInfrastructure = errors.New("infrastructure failure")
	// ErrProviderUnreachable is returned when a model call failed after
	// the connector's retries. It wraps ErrInfrastructure.
	ErrProviderUnreachable = fmt.Errorf("the model provider is unreachable after retries: %w", ErrInfrastructure)

	// errNotOneEvent is a scenario payload that does not give exactly one
	// event.
	errNotOneEvent = errors.New("the payload must give exactly one event")
	// errNotRouted is a run whose event got no investigation in time.
	errNotRouted = fmt.Errorf("the event got no investigation within %s", routeTimeout)
	// unusedAuth is the endpoint auth of a run. Deliver skips
	// authentication, so it is never read.
	unusedAuth = registry.SecretRef{Name: "bench", Key: "unused"}
)

// Options set up a Runner.
type Options struct {
	// Config is the loaded sidekick.yaml. A run uses its model
	// connectors, roles and prices, its strategies, its report rules, its
	// redaction settings and its per-investigation limits. It does not use
	// its sources, outputs, webhooks, routes, rate limits or daily caps.
	Config *config.Config
	// Registries are where plugin types are looked up. The zero value
	// means the global ones.
	Registries config.Registries
	// Repeat is how many times each scenario runs. Zero means
	// DefaultRepeat.
	Repeat int
	// Logger receives a line as each run starts and ends. Nil logs
	// nothing.
	Logger *slog.Logger
	// ComponentLogger is the logger of the intake, router, queue,
	// investigator and tool guard of each run. Nil logs nothing.
	ComponentLogger *slog.Logger
}

// Runner runs benchmark scenarios. Make it with New, which checks every
// scenario against the config, then call Run.
type Runner struct {
	cfg        config.Config
	regs       config.Registries
	scenarios  []Scenario
	repeat     int
	deps       registry.Deps
	models     *config.Instances
	connectors map[string]investigator.Connector
	prices     pricing.Table
	redactor   *redact.Redactor
	log        *slog.Logger
	compLog    *slog.Logger
}

// Run runs every scenario Repeat times, one run at a time, in database, and
// returns the results. When a model call fails after its retries it stops
// and returns the results so far with an error that wraps
// ErrProviderUnreachable. When ctx ends it stops and returns the results
// so far with ctx's error.
func (r *Runner) Run(ctx context.Context, database *Database) (*Results, error) {
	results := &Results{StartedAt: time.Now(), Repeat: r.repeat}

	defer func() { results.FinishedAt = time.Now() }()

	for _, sc := range r.scenarios {
		sr := ScenarioResult{ID: sc.ID, Title: sc.Title, Path: sc.Path, Expected: sc.Expected}

		for n := 1; n <= r.repeat; n++ {
			r.log.InfoContext(ctx, "bench run started", "scenario", sc.ID, "run", n, "of", r.repeat)

			run, err := r.runOnce(ctx, database, sc, n)
			if err != nil {
				results.Scenarios = append(results.Scenarios, sr)

				return results, fmt.Errorf("scenario %s, run %d: %w", sc.ID, n, err)
			}

			r.log.InfoContext(ctx, "bench run ended", "scenario", sc.ID, "run", n,
				"verdict", string(run.Report.Verdict), "stop_reason", string(run.Report.StopReason))

			sr.Runs = append(sr.Runs, run)
		}

		results.Scenarios = append(results.Scenarios, sr)
	}

	return results, nil
}

// New builds the model connectors of opts.Config and checks every
// scenario without a database: its strategy is loaded, its adapter turns
// its payload into exactly one event, and its mock sources build. Problems
// in the scenarios give an error that wraps ErrInvalidScenario, and
// problems in the config one that wraps config.Errors.
func New(ctx context.Context, scenarios []Scenario, opts Options) (*Runner, error) {
	if opts.Config == nil {
		return nil, errors.New("new bench runner: Config is required")
	}

	r := &Runner{
		cfg:       benchConfig(opts.Config),
		regs:      opts.Registries,
		scenarios: scenarios,
		repeat:    cmp.Or(opts.Repeat, DefaultRepeat),
		log:       orDiscard(opts.Logger),
		compLog:   orDiscard(opts.ComponentLogger),
	}

	if r.regs.Sources == nil {
		r.regs.Sources = registry.Sources
	}

	if r.regs.Intakes == nil {
		r.regs.Intakes = registry.Intakes
	}

	r.deps = registry.Deps{Secrets: secrets.NewDir(r.cfg.Secrets.Dir), HTTP: httpclient.New(), Logger: r.compLog}

	if err := r.buildModels(ctx); err != nil {
		return nil, fmt.Errorf("new bench runner: %w", err)
	}

	if err := r.buildRedactor(ctx); err != nil {
		return nil, fmt.Errorf("new bench runner: %w", err)
	}

	if err := r.check(ctx); err != nil {
		return nil, fmt.Errorf("new bench runner: %w", err)
	}

	return r, nil
}

// buildModels builds the model connectors of the config, with their retry
// settings.
func (r *Runner) buildModels(ctx context.Context) error {
	models, err := config.Build(ctx, &r.cfg, r.regs, r.deps)
	if err != nil {
		return fmt.Errorf("build the model connectors: %w", err)
	}

	r.models = models
	r.prices = pricing.New(r.cfg.Model.PriceTable())
	r.connectors = make(map[string]investigator.Connector, len(models.Models))

	for name, c := range models.Models {
		r.connectors[name] = investigator.Connector{Connector: c, Retries: models.ModelSettings[name].Retries}
	}

	return nil
}

// buildRedactor builds the redactor from the config's redaction settings,
// as serve does.
func (r *Runner) buildRedactor(ctx context.Context) error {
	opts, err := r.cfg.Redaction.RedactOptions(ctx, r.deps.Secrets)
	if err != nil {
		return fmt.Errorf("read the redaction settings: %w", err)
	}

	opts.Logger = r.compLog

	redactor, err := redact.New(opts)
	if err != nil {
		return fmt.Errorf("compile the redaction rules: %w", err)
	}

	r.redactor = redactor

	return nil
}

// check checks every scenario against the config and the registries. It
// returns an error that wraps ErrInvalidScenario and lists every problem.
func (r *Runner) check(ctx context.Context) error {
	var problems []string

	for _, sc := range r.scenarios {
		for _, p := range r.checkScenario(ctx, sc) {
			problems = append(problems, sc.Path+": "+p)
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w:\n%s", ErrInvalidScenario, strings.Join(problems, "\n"))
	}

	return nil
}

// checkScenario returns the problems of one scenario, as "<field>:
// <problem>".
func (r *Runner) checkScenario(ctx context.Context, sc Scenario) []string {
	var problems []string

	if _, ok := r.cfg.StrategySet().Get(r.strategy(sc)); !ok {
		problems = append(problems, fmt.Sprintf("strategy: %q is not loaded", r.strategy(sc)))
	}

	if err := r.checkEvent(ctx, sc); err != nil {
		problems = append(problems, err.Error())
	}

	for _, name := range sc.SourceNames() {
		src, err := r.buildSource(ctx, sc, name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("sources.%s: %v", name, err))

			continue
		}

		if err := src.Close(); err != nil {
			problems = append(problems, fmt.Sprintf("sources.%s: close: %v", name, err))
		}
	}

	return problems
}

// checkEvent checks that the scenario's adapter turns its payload into
// exactly one event that opens an investigation.
func (r *Runner) checkEvent(ctx context.Context, sc Scenario) error {
	adapter, err := r.buildAdapter(ctx, sc, endpointPrefix+"check")
	if err != nil {
		return fmt.Errorf("event.adapter: %w", err)
	}

	body, err := sc.Body()
	if err != nil {
		return fmt.Errorf("event.payload: %w", err)
	}

	events, err := adapter.Parse(ctx, intake.Request{Endpoint: endpointPrefix + "check", Body: body, ReceivedAt: sc.Now})
	if err != nil {
		return fmt.Errorf("event.payload: the %s adapter cannot parse it: %w", sc.Event.Adapter, err)
	}

	if len(events) != 1 {
		return fmt.Errorf("event.payload: %w; the %s adapter gives %d", errNotOneEvent, sc.Event.Adapter, len(events))
	}

	if events[0].Kind == event.KindResolved {
		return errors.New("event.payload: the event is a recovery, which opens no investigation")
	}

	return nil
}

// runOnce runs a scenario once, the way serve handles an alert: the
// payload is delivered to a webhook endpoint of the run's own, and a job
// queue runs the router and the investigator until the investigation ends.
func (r *Runner) runOnce(ctx context.Context, database *Database, sc Scenario, n int) (RunResult, error) {
	runID := ids.New()
	endpoint := endpointPrefix + runID.String()

	guard, closeSources, err := r.guard(ctx, sc)
	if err != nil {
		return RunResult{}, fmt.Errorf("set up the sources: %w", err)
	}

	defer closeSources()

	hooks, err := r.webhooks(ctx, database.Pool, sc, endpoint)
	if err != nil {
		return RunResult{}, fmt.Errorf("set up the webhook endpoint: %w", err)
	}

	routeJobs, investigateJobs, jobs, err := r.jobQueue(database, sc, guard, runID)
	if err != nil {
		return RunResult{}, fmt.Errorf("set up the job queue: %w", err)
	}

	jobsCtx, stopJobs := context.WithCancel(ctx)
	jobsDone := make(chan error, 1)

	go func() { jobsDone <- jobs.Run(jobsCtx) }()

	defer func() {
		stopJobs()

		if err := <-jobsDone; err != nil {
			r.log.ErrorContext(ctx, "the bench job queue failed", "error", err)
		}
	}()

	body, err := sc.Body()
	if err != nil {
		return RunResult{}, fmt.Errorf("encode the payload: %w", err)
	}

	started := time.Now()

	events, err := hooks.Deliver(ctx, intake.Request{Endpoint: endpoint, Body: body, ReceivedAt: sc.Now})
	if err != nil {
		return RunResult{}, fmt.Errorf("deliver the payload: %w", err)
	}

	if len(events) != 1 {
		return RunResult{}, fmt.Errorf("deliver the payload: %w; %d were stored", errNotOneEvent, len(events))
	}

	inv, err := r.wait(ctx, database.Pool, events[0], routeJobs, investigateJobs)
	if err != nil {
		return RunResult{}, fmt.Errorf("investigation: %w", err)
	}

	took := time.Since(started)

	// The next run's event has the same fingerprint, and would join this
	// run's group rather than open an investigation of its own.
	if _, err := db.New(database.Pool).CloseEventGroup(ctx, db.CloseEventGroupParams{ClosedReason: closedRunEnded, ID: inv.GroupID}); err != nil {
		return RunResult{}, fmt.Errorf("close the run's group: %w", err)
	}

	return r.result(ctx, database.Pool, inv, n, took)
}

// guard builds the scenario's mock sources behind a tool guard of the
// run's own. The returned function closes the sources.
func (r *Runner) guard(ctx context.Context, sc Scenario) (*tools.Guard, func(), error) {
	sources := make([]tools.Source, 0, len(sc.Sources))

	closeAll := func() {
		for _, s := range sources {
			if err := s.Source.Close(); err != nil {
				r.log.WarnContext(ctx, "closing a mock source failed", "source", s.Name, "error", err)
			}
		}
	}

	for _, name := range sc.SourceNames() {
		src, err := r.buildSource(ctx, sc, name)
		if err != nil {
			closeAll()

			return nil, nil, fmt.Errorf("source %s: %w", name, err)
		}

		sources = append(sources, tools.Source{Name: name, Source: src})
	}

	guard, err := tools.New(sources, tools.Options{Redactor: r.redactor, Logger: r.compLog})
	if err != nil {
		closeAll()

		return nil, nil, fmt.Errorf("build the tool guard: %w", err)
	}

	return guard, closeAll, nil
}

// webhooks builds a webhook handler with one endpoint, endpoint, for the
// scenario's adapter.
func (r *Runner) webhooks(ctx context.Context, pool *pgxpool.Pool, sc Scenario, endpoint string) (*webhook.Handler, error) {
	adapter, err := r.buildAdapter(ctx, sc, endpoint)
	if err != nil {
		return nil, fmt.Errorf("build the %s adapter: %w", sc.Event.Adapter, err)
	}

	auth := unusedAuth

	hooks, err := webhook.New(webhook.Options{
		Webhooks: map[string]config.Webhook{endpoint: {Section: config.Section{Type: sc.Event.Adapter}, Auth: config.WebhookAuth{Bearer: &auth}}},
		Adapters: map[string]intake.Adapter{endpoint: adapter},
		Redactor: r.redactor,
		Pool:     pool,
		Logger:   r.compLog,
	})
	if err != nil {
		return nil, fmt.Errorf("build the webhook handler: %w", err)
	}

	return hooks, nil
}

// jobQueue builds a job queue that runs the route_event and investigate jobs
// with a router and an investigator for the scenario's route. It returns
// the two handlers, which remember the last error of their jobs.
func (r *Runner) jobQueue(database *Database, sc Scenario, guard *tools.Guard, runID uuid.UUID) (routeJobs, investigateJobs *watched, jobs *queue.Runner, err error) {
	cfg := r.cfg
	cfg.Routes = []config.Route{r.route(sc)}

	rt, err := router.New(router.Options{Config: &cfg, ModelSettings: r.models.ModelSettings, Pool: database.Pool, Logger: r.compLog})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build the router: %w", err)
	}

	inv, err := investigator.New(investigator.Options{
		Pool:       database.Pool,
		Guard:      guard,
		Connectors: r.connectors,
		Prices:     r.prices,
		Strategies: cfg.StrategySet(),
		Routes:     map[string]investigator.Route{RouteName: {Sources: sc.SourceNames()}},
		Rules:      cfg.Report.Rules(),
		Daily:      investigator.DailyCaps{Location: time.UTC},
		Logger:     r.compLog,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build the investigator: %w", err)
	}

	jobs, err = queue.NewRunner(database.Pool, queue.Options{
		WorkerID:     workerPrefix + runID.String(),
		Limits:       queue.Limits{Global: 1},
		PollInterval: pollInterval,
		Logger:       r.compLog,
		Schema:       database.Schema,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build the job queue: %w", err)
	}

	routeJobs, investigateJobs = &watched{next: rt}, &watched{next: inv}

	if err := jobs.Register(queue.KindRouteEvent, routeJobs, queue.KindOptions{}); err != nil {
		return nil, nil, nil, fmt.Errorf("register the router: %w", err)
	}

	if err := jobs.Register(queue.KindInvestigate, investigateJobs, queue.KindOptions{Concurrency: 1, Long: true, OnDead: inv.OnDead}); err != nil {
		return nil, nil, nil, fmt.Errorf("register the investigator: %w", err)
	}

	return routeJobs, investigateJobs, jobs, nil
}

// wait waits until the investigation of the event with eventID ends, and
// returns it. A router or investigator job that failed ends the wait with
// its error; a model call that failed after its retries gives an error that
// wraps ErrProviderUnreachable.
func (r *Runner) wait(ctx context.Context, pool *pgxpool.Pool, eventID uuid.UUID, routeJobs, investigateJobs *watched) (db.Investigation, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	routeDeadline := time.Now().Add(routeTimeout)
	queries := db.New(pool)

	for {
		select {
		case <-ctx.Done():
			return db.Investigation{}, fmt.Errorf("wait for the investigation: %w", context.Cause(ctx))
		case <-ticker.C:
		}

		if err := routeJobs.failure(); err != nil {
			return db.Investigation{}, fmt.Errorf("route the event: %w", err)
		}

		row, err := queries.GetEventInvestigation(ctx, eventID)
		if errors.Is(err, pgx.ErrNoRows) {
			if time.Now().After(routeDeadline) {
				return db.Investigation{}, errNotRouted
			}

			continue
		}

		if err != nil {
			return db.Investigation{}, fmt.Errorf("load the investigation: %w", err)
		}

		inv := row.Investigation
		if inv.Status != investigator.StatusFinal && inv.Status != investigator.StatusFailed {
			continue
		}

		if err := investigateJobs.failure(); err != nil {
			if _, ok := errors.AsType[*model.Error](err); ok {
				return db.Investigation{}, fmt.Errorf("%w: %w", ErrProviderUnreachable, err)
			}

			return db.Investigation{}, fmt.Errorf("run the investigation: %w", err)
		}

		return inv, nil
	}
}

// result reads the outcome of a run from its investigation and steps.
func (r *Runner) result(ctx context.Context, pool *pgxpool.Pool, inv db.Investigation, n int, took time.Duration) (RunResult, error) {
	var rep report.Report
	if err := json.Unmarshal(inv.Report, &rep); err != nil {
		return RunResult{}, fmt.Errorf("decode the report of investigation %s: %w", inv.ID, err)
	}

	steps, err := db.New(pool).ListSteps(ctx, inv.ID)
	if err != nil {
		return RunResult{}, fmt.Errorf("load the steps of investigation %s: %w", inv.ID, err)
	}

	saved, err := investigator.ToolResults(steps)
	if err != nil {
		return RunResult{}, fmt.Errorf("read the tool results of investigation %s: %w", inv.ID, err)
	}

	var results []ToolResult

	for _, s := range saved {
		if s.Source == "" {
			continue
		}

		name, err := source.QualifiedName(s.Source, s.Tool)
		if err != nil {
			return RunResult{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
		}

		results = append(results, ToolResult{Tool: name, Status: s.Status, Content: s.Content})
	}

	return RunResult{
		Run:             n,
		InvestigationID: inv.ID,
		Status:          inv.Status,
		Report:          rep,
		ToolCalls:       int(inv.ToolCalls),
		InputTokens:     inv.InputTokens,
		OutputTokens:    inv.OutputTokens,
		CostMicros:      inv.CostMicros,
		Duration:        took,
		ToolResults:     results,
	}, nil
}

// route returns the route of the scenario's runs: its strategy, its mock
// sources, read-only mode, and its limits, with the config's
// per-investigation limits where it sets none.
func (r *Runner) route(sc Scenario) config.Route {
	global := r.cfg.Budgets.PerInvestigation

	return config.Route{
		Name:     RouteName,
		Priority: config.DefaultRoutePriority,
		Strategy: r.strategy(sc),
		Sources:  sc.SourceNames(),
		Mode:     action.ReadOnly,
		Budgets: config.RouteBudgets{Limits: config.Limits{
			MaxTokens:    cmp.Or(sc.Limits.MaxTokens, global.MaxTokens),
			MaxToolCalls: cmp.Or(sc.Limits.MaxToolCalls, global.MaxToolCalls),
			MaxWallTime:  cmp.Or(sc.Limits.MaxWallTime, global.MaxWallTime),
			MaxCostUSD:   cmp.Or(sc.Limits.MaxCostUSD, global.MaxCostUSD),
		}},
		Grouping: config.Grouping{Window: config.DefaultGroupingWindow},
	}
}

// strategy returns the name of the scenario's strategy.
func (r *Runner) strategy(sc Scenario) string {
	return cmp.Or(sc.Strategy, config.DefaultStrategy)
}

// buildSource builds the scenario's mock source called name through the
// source registry, as serve builds a configured source.
func (r *Runner) buildSource(ctx context.Context, sc Scenario, name string) (source.Source, error) {
	src, err := r.regs.Sources.Build(ctx, mock.Type, name, configDecoder[mock.Config]{cfg: sc.SourceConfig(name)}, r.deps)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	return src, nil
}

// buildAdapter builds the scenario's intake adapter for the endpoint
// called endpoint, from the scenario's adapter config.
func (r *Runner) buildAdapter(ctx context.Context, sc Scenario, endpoint string) (intake.Adapter, error) {
	adapter, err := r.regs.Intakes.Build(ctx, sc.Event.Adapter, endpoint, config.Section{Type: sc.Event.Adapter, Node: &sc.Event.Config}, r.deps)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	return adapter, nil
}

// watched is a job handler that remembers the last error of its jobs and
// gives the job up on it: a benchmark run does not retry a job, since the
// model connector has already retried its calls.
type watched struct {
	next queue.Handler

	mu  sync.Mutex
	err error
}

// Handle runs the job and gives it up when it fails.
func (w *watched) Handle(ctx context.Context, job db.Job) error {
	err := w.next.Handle(ctx, job)
	if err == nil {
		return nil
	}

	w.mu.Lock()
	w.err = err
	w.mu.Unlock()

	return fmt.Errorf("%w: %w", queue.ErrGiveUp, err)
}

// failure returns the last error of the handler's jobs, or nil.
func (w *watched) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.err
}

// configDecoder is a registry.Decoder that hands over a config that is
// already decoded and checked, such as a scenario's mock source.
type configDecoder[C any] struct {
	cfg C
}

// Decode sets target, a *C, to the config.
func (d configDecoder[C]) Decode(target any) error {
	p, ok := target.(*C)
	if !ok {
		return fmt.Errorf("decode into %T: want %T", target, &d.cfg)
	}

	*p = d.cfg

	return nil
}

// benchConfig returns a copy of cfg for benchmark runs: without its
// sources, outputs, webhooks and routes, which the runs replace with their
// own, and without rate limits and daily caps, which would hold runs back.
func benchConfig(cfg *config.Config) config.Config {
	out := *cfg
	out.Sources = nil
	out.Outputs = nil
	out.Intake = config.Intake{}
	out.Routes = nil
	out.Budgets.RateLimit = nil
	out.Budgets.Daily.MaxCostUSD = 0

	return out
}

// orDiscard returns log, or a logger that discards everything when log is
// nil.
func orDiscard(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.New(slog.DiscardHandler)
	}

	return log
}
