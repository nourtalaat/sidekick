// Package app wires Sidekick together and runs it: it loads the config,
// sets up telemetry, opens and migrates the database, builds the plugin
// instances, runs the job queue and serves HTTP, and stops all of it in the
// reverse order.
//
// Features add themselves between New and Run, with RegisterRoutes and
// RegisterJob, and take what they need from the App's accessors.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/telemetry"
)

// ShutdownGrace is how long Run takes at most to stop once its context
// ends: to finish open requests, hand back running jobs and send buffered
// spans. It fits in the default Kubernetes termination grace period.
const ShutdownGrace = 30 * time.Second

// jobShutdownGrace is how long running jobs may go on after shutdown
// starts before the queue hands them back. It leaves the rest of
// ShutdownGrace for the hand-back itself and for closing everything else.
const jobShutdownGrace = 20 * time.Second

// errAlreadyRunning is returned when something is registered after Run
// started, or Run is called twice.
var errAlreadyRunning = errors.New("the app is already running")

// Options set up an App. Only ConfigPath is required.
type Options struct {
	// ConfigPath is the sidekick.yaml file to load.
	ConfigPath string
	// Registries are where plugin types are looked up. The zero value
	// means the global registries.
	Registries config.Registries
	// Build is the version and commit of the binary, for
	// sidekick_build_info and traces.
	Build buildinfo.Info
	// LogOutput receives the JSON logs. Nil means standard output.
	LogOutput io.Writer
}

// App is a running Sidekick: its config, telemetry, database pool, plugin
// instances, job queue and HTTP listeners. Make it with New, register
// routes and job handlers, then call Run once.
type App struct {
	cfg       *config.Config
	telemetry *telemetry.Telemetry
	log       *slog.Logger
	pool      *pgxpool.Pool
	secrets   registry.Secrets
	redactor  *redact.Redactor
	instances *config.Instances
	runner    *queue.Runner
	mux       *http.ServeMux

	// mu guards running, jobKinds and tasks, which the Register methods
	// and Run touch.
	mu       sync.Mutex
	running  bool
	jobKinds int
	tasks    []task

	// started is closed once both listeners serve; listenAddr and
	// metricsAddr are set before that.
	started     chan struct{}
	listenAddr  net.Addr
	metricsAddr net.Addr

	closeOnce sync.Once
	closeErr  error
}

// jobQueue is the job queue as Run started it. done receives the queue's
// result once; it is nil when no handler is registered and the queue never
// ran.
type jobQueue struct {
	stop context.CancelFunc
	done chan error
}

// New runs the start-up steps before serving, in order: it loads the config
// with every secret checked, sets up telemetry, opens the database pool,
// migrates the database unless database.migrations is manual, builds the
// redactor and builds the plugin instances. On an error it closes what it
// had opened. A config with problems gives an error that wraps
// config.Errors.
func New(ctx context.Context, opts Options) (*App, error) {
	if opts.LogOutput == nil {
		opts.LogOutput = os.Stdout
	}

	// The log level is in the config, so loading it logs at info.
	loadLog := telemetry.NewLogger(opts.LogOutput, slog.LevelInfo).With("component", "config")

	cfg, err := config.Load(ctx, opts.ConfigPath, config.LoadOptions{Registries: opts.Registries, CheckSecrets: true, Logger: loadLog})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	tel, err := telemetry.New(ctx, cfg.Telemetry, telemetry.Options{Build: opts.Build, LogOutput: opts.LogOutput})
	if err != nil {
		return nil, fmt.Errorf("set up telemetry: %w", err)
	}

	a := &App{
		cfg:       cfg,
		telemetry: tel,
		log:       tel.Logger().With("component", "app"),
		mux:       http.NewServeMux(),
		started:   make(chan struct{}),
	}

	if err := a.start(ctx, opts.Registries); err != nil {
		return nil, errors.Join(err, a.Close())
	}

	return a, nil
}

// RegisterRoutes calls register with the mux of the main listener
// (server.listen), so it can add routes. Paths no route matches get 404.
// Call it before Run.
func (a *App) RegisterRoutes(register func(mux *http.ServeMux)) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return fmt.Errorf("register routes: %w", errAlreadyRunning)
	}

	register(a.mux)

	return nil
}

// RegisterJob sets the handler for jobs of kind. Run starts the job queue
// only when at least one handler is registered. Call it before Run. Claims
// of the investigation kinds (queue.KindInvestigate, queue.KindFollowUp)
// are held by budgets.concurrency and each route's concurrency.
func (a *App) RegisterJob(kind string, handler queue.Handler, opts queue.KindOptions) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return fmt.Errorf("register %s job handler: %w", kind, errAlreadyRunning)
	}

	if err := a.runner.Register(kind, handler, opts); err != nil {
		return fmt.Errorf("register %s job handler: %w", kind, err)
	}

	a.jobKinds++

	return nil
}

// Config returns the loaded config. It must not be changed.
func (a *App) Config() *config.Config {
	return a.cfg
}

// Logger returns the process's logger. Add a component name with
// Logger().With("component", name).
func (a *App) Logger() *slog.Logger {
	return a.telemetry.Logger()
}

// Telemetry returns the metrics registry and the tracer provider, besides
// the logger.
func (a *App) Telemetry() *telemetry.Telemetry {
	return a.telemetry
}

// Pool returns the database pool.
func (a *App) Pool() *pgxpool.Pool {
	return a.pool
}

// Secrets returns the resolver of secret references, the same one plugins
// get in registry.Deps. It reads the secret files on every call, so a
// rotated secret is used without a restart.
func (a *App) Secrets() registry.Secrets {
	return a.secrets
}

// Redactor returns the redactor built from the redaction section of the
// config. It is safe for concurrent use.
func (a *App) Redactor() *redact.Redactor {
	return a.redactor
}

// Instances returns the plugin instances built from the config.
func (a *App) Instances() *config.Instances {
	return a.instances
}

// Started is closed once both listeners serve and the start is logged.
func (a *App) Started() <-chan struct{} {
	return a.started
}

// Addrs returns the addresses the main and metrics listeners serve on,
// which differ from the config when it names port 0. They are set once
// Started is closed.
func (a *App) Addrs() (listen, metrics net.Addr) {
	return a.listenAddr, a.metricsAddr
}

// Run starts the job queue, if any job handler is registered, then serves
// the main and metrics listeners and runs the registered tasks until ctx
// ends or one of them fails. Then it stops everything in the reverse order
// of start-up, taking at most ShutdownGrace, and closes the App. It returns nil after a shutdown that
// ctx started and finished in time.
func (a *App) Run(ctx context.Context) error {
	if err := a.markRunning(); err != nil {
		return fmt.Errorf("run: %w", err)
	}

	stopCtx, cancelStop := context.WithCancel(ctx)
	defer cancelStop()

	jobs := a.startJobs(stopCtx)

	servers, failed, err := a.serve(stopCtx)
	if err != nil {
		cancelStop()

		return errors.Join(fmt.Errorf("start listeners: %w", err), a.shutdown(ctx, taskGroup{}, nil, jobs))
	}

	tasks := a.startTasks(stopCtx)

	a.log.InfoContext(ctx, "sidekick started",
		"listen", a.listenAddr.String(), "metrics_listen", a.metricsAddr.String(),
		"migrations", string(a.cfg.Database.Migrations), "job_kinds", a.jobKinds)

	// Closed after the start line is logged, so whoever waits on Started
	// also sees that line.
	close(a.started)

	var runErr error

	select {
	case <-ctx.Done():
		a.log.InfoContext(ctx, "shutting down")
	case err := <-failed:
		runErr = fmt.Errorf("serve: %w", err)
	case err := <-jobs.done:
		runErr = fmt.Errorf("run job queue: %w", err)
	case err := <-tasks.failed:
		runErr = fmt.Errorf("run: %w", err)
	}

	cancelStop()

	return errors.Join(runErr, a.shutdown(ctx, tasks, servers, jobs))
}

// Close releases what New opened: the plugin instances, the database pool
// and telemetry. Run calls it when it returns; call it yourself only when
// Run is not called. Calling it more than once is safe.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
		defer cancel()

		a.closeErr = a.closeResources(ctx)
	})

	return a.closeErr
}

// start opens the database, migrates it, and builds the redactor and the
// plugin instances.
func (a *App) start(ctx context.Context, regs config.Registries) error {
	a.secrets = secrets.NewDir(a.cfg.Secrets.Dir)

	if err := a.openStore(ctx); err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	if err := a.migrate(ctx); err != nil {
		return fmt.Errorf("migrate at start-up: %w", err)
	}

	if err := a.buildRedactor(ctx); err != nil {
		return fmt.Errorf("build the redactor: %w", err)
	}

	deps := registry.Deps{Secrets: a.secrets, HTTP: httpclient.New(), Logger: a.telemetry.Logger(), Metrics: a.telemetry.Registry(), DB: a.pool}

	instances, err := config.Build(ctx, a.cfg, regs, deps)
	if err != nil {
		return fmt.Errorf("build plugin instances: %w", err)
	}

	a.instances = instances

	metrics, err := queue.NewMetrics(a.telemetry.Registry())
	if err != nil {
		return fmt.Errorf("set up queue metrics: %w", err)
	}

	runner, err := queue.NewRunner(a.pool, queue.Options{
		Limits:          queue.Limits{Global: a.cfg.Budgets.Concurrency, Routes: a.cfg.RouteConcurrency()},
		ShutdownTimeout: jobShutdownGrace,
		Metrics:         metrics,
		Logger:          a.telemetry.Logger(),
	})
	if err != nil {
		return fmt.Errorf("set up job queue: %w", err)
	}

	a.runner = runner

	return nil
}

func (a *App) openStore(ctx context.Context) error {
	maxConns := a.cfg.Database.MaxConns
	if maxConns > math.MaxInt32 {
		return fmt.Errorf("database.maxConns is %d, at most %d is allowed", maxConns, math.MaxInt32)
	}

	url, err := a.cfg.DatabaseURL(ctx, nil)
	if err != nil {
		return fmt.Errorf("find the database URL: %w", err)
	}

	pool, err := store.Open(ctx, url, int32(maxConns)) //nolint:gosec // Checked against math.MaxInt32 above.
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	a.pool = pool

	return nil
}

func (a *App) migrate(ctx context.Context) error {
	if a.cfg.Database.Migrations == config.MigrationsManual {
		a.log.InfoContext(ctx, "not migrating the database: database.migrations is manual")

		return a.checkVectorDimensions(ctx)
	}

	a.log.InfoContext(ctx, "migrating the database")

	if err := store.Migrate(ctx, a.pool); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	if !a.cfg.Memory.Vector.Enabled {
		return nil
	}

	a.log.InfoContext(ctx, "migrating the vector memory tables")

	if err := store.MigrateVector(ctx, a.pool, a.cfg.Memory.Vector.Dimensions); err != nil {
		return fmt.Errorf("apply vector migrations: %w", err)
	}

	return nil
}

// checkVectorDimensions makes the start fail when vector memory is on and
// the embeddings table holds vectors of another length than the config
// says. It only reads, so it serves a replica that does not migrate. A
// database whose vector migrations are not applied yet is not checked;
// /readyz reports it.
func (a *App) checkVectorDimensions(ctx context.Context) error {
	if !a.cfg.Memory.Vector.Enabled {
		return nil
	}

	current, target, err := store.VectorSchemaVersion(ctx, a.pool)
	if err != nil {
		return fmt.Errorf("read the vector schema version: %w", err)
	}

	if current < target {
		return nil
	}

	if err := store.CheckVectorDimensions(ctx, a.pool, a.cfg.Memory.Vector.Dimensions); err != nil {
		return fmt.Errorf("check the embedding length: %w", err)
	}

	return nil
}

func (a *App) buildRedactor(ctx context.Context) error {
	opts, err := a.cfg.Redaction.RedactOptions(ctx, a.secrets)
	if err != nil {
		return fmt.Errorf("read the redaction settings: %w", err)
	}

	opts.Logger = a.telemetry.Logger().With("component", "redact")

	r, err := redact.New(opts)
	if err != nil {
		return fmt.Errorf("compile the redaction rules: %w", err)
	}

	a.redactor = r

	return nil
}

func (a *App) markRunning() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return errAlreadyRunning
	}

	a.running = true

	return nil
}

// startJobs runs the job queue, if any handler is registered, until the
// returned jobQueue's stop is called.
func (a *App) startJobs(ctx context.Context) jobQueue {
	if a.jobKinds == 0 {
		a.log.InfoContext(ctx, "not starting the job queue: no job handlers are registered")

		return jobQueue{stop: func() {}}
	}

	// The queue stops when shutdown says so, after the listeners, not when
	// ctx ends.
	jobsCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)

	go func() { done <- a.runner.Run(jobsCtx) }()

	return jobQueue{stop: stop, done: done}
}

// shutdown stops the tasks and the listeners, then the job queue, then
// closes the rest, all within ShutdownGrace of now.
func (a *App) shutdown(ctx context.Context, tasks taskGroup, servers []*http.Server, jobs jobQueue) error {
	graceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownGrace)
	defer cancel()

	var errs []error

	if tasks.stop != nil {
		if err := tasks.stopAndWait(graceCtx); err != nil {
			errs = append(errs, fmt.Errorf("stop tasks: %w", err))
		}
	}

	for _, server := range servers {
		if err := server.Shutdown(graceCtx); err != nil {
			errs = append(errs, fmt.Errorf("stop listener %s: %w", server.Addr, err))
		}
	}

	jobs.stop()

	if jobs.done != nil {
		select {
		case err := <-jobs.done:
			if err != nil {
				errs = append(errs, fmt.Errorf("stop job queue: %w", err))
			}
		case <-graceCtx.Done():
			errs = append(errs, fmt.Errorf("stop job queue: %w", graceCtx.Err()))
		}
	}

	a.closeOnce.Do(func() {
		a.closeErr = a.closeResources(graceCtx)
	})

	errs = append(errs, a.closeErr)

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}

	a.log.InfoContext(ctx, "sidekick stopped")

	return nil
}

// closeResources closes the plugin instances, the pool and telemetry, in
// that order, skipping those that were never opened.
func (a *App) closeResources(ctx context.Context) error {
	var errs []error

	if a.instances != nil {
		if err := a.instances.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close plugin instances: %w", err))
		}
	}

	if a.pool != nil {
		a.pool.Close()
	}

	if err := a.telemetry.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("stop telemetry: %w", err))
	}

	return errors.Join(errs...)
}
