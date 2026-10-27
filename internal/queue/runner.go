package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Defaults for the fields of Options and KindOptions left at zero.
const (
	DefaultPollInterval      = time.Second
	DefaultReapInterval      = 15 * time.Second
	DefaultShutdownTimeout   = 30 * time.Second
	DefaultMetricsInterval   = 15 * time.Second
	DefaultLongLease         = 60 * time.Second
	DefaultHeartbeatInterval = 20 * time.Second
	DefaultShortLease        = 5 * time.Minute
	DefaultConcurrency       = 1
)

const (
	// maxConcurrency is the most jobs of one kind that a Runner runs at once.
	maxConcurrency = 1024
	// dbTimeout bounds each queue query that the Runner makes.
	dbTimeout = 10 * time.Second
	// listenRetryInterval is how long the Runner waits before it listens
	// for jobs_ready again after the connection failed. Polling keeps jobs
	// moving in the meantime.
	listenRetryInterval = 5 * time.Second
	// handBackGrace is how long handlers have to return after their context
	// is cancelled at shutdown, before the Runner hands their jobs back
	// itself.
	handBackGrace = 5 * time.Second
	// onDeadTimeout bounds each OnDead hook.
	onDeadTimeout = 30 * time.Second
)

// errShuttingDown is the cause of a handler's cancelled context when the
// Runner shuts down.
var errShuttingDown = errors.New("the runner is shutting down")

// Handler runs claimed jobs of one kind. It returns nil to complete the job,
// or an error to fail it: the job is retried after a backoff, or becomes
// dead after its last attempt, or at once when the error wraps ErrGiveUp.
//
// job holds the job as claimed: its Kind, Payload, Route, Attempts (the
// failures before this run), Traceparent, and ClaimToken, which is always
// set. A handler that writes rows of its own fences those writes with the
// claim token, so that it stops when another worker has taken the job over.
//
// ctx ends when the job's lease is lost, when the lease of a short job runs
// out, or when the Runner shuts down. A handler must return soon after.
type Handler interface {
	Handle(ctx context.Context, job db.Job) error
}

// HandlerFunc is a function that is a Handler.
type HandlerFunc func(ctx context.Context, job db.Job) error

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, job db.Job) error {
	return f(ctx, job)
}

// KindOptions set how a Runner runs the jobs of one kind. Fields left at
// zero take their defaults.
type KindOptions struct {
	// Concurrency is the most jobs of this kind that this replica runs at
	// once. The default is DefaultConcurrency. Investigation kinds are also
	// held by Options.Limits, across all replicas.
	Concurrency int
	// Long marks a job that can run for minutes. Its lease is renewed every
	// HeartbeatInterval while the handler runs. A job that is not long keeps
	// one lease for its whole run, and its handler's context ends when that
	// lease runs out.
	Long bool
	// Lease is how long a claim lasts without renewal. The default is
	// DefaultLongLease for long jobs and DefaultShortLease for others.
	Lease time.Duration
	// HeartbeatInterval is how often a long job's lease is renewed. It must
	// be shorter than Lease. The default is DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration
	// OnDead, when set, runs after a job of this kind has become dead: when
	// its handler failed on its last attempt or gave up, and when the reaper
	// found its lease expired on its last attempt. It runs after the change
	// to dead is committed, on the replica that made it, and its error is
	// logged without changing the job. It must be safe to call more than
	// once for a job: a job that an admin retries can become dead again. If
	// the replica stops right after the change, the hook does not run.
	OnDead func(ctx context.Context, job db.Job) error
}

// Options set up a Runner. Fields left at zero take their defaults.
type Options struct {
	// WorkerID names this replica in jobs.locked_by. The default is the
	// host name and a random suffix.
	WorkerID string
	// Limits caps the investigate and follow_up jobs that run at once
	// across all replicas. It is required when the Runner handles either
	// kind.
	Limits Limits
	// PollInterval is how often each kind looks for ready jobs when no
	// jobs_ready notification arrives. The default is DefaultPollInterval.
	PollInterval time.Duration
	// ReapInterval is how often the Runner tries to reap expired leases.
	// The default is DefaultReapInterval.
	ReapInterval time.Duration
	// ShutdownTimeout is how long running handlers may go on after Run's
	// context ends, before their jobs are handed back. The default is
	// DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
	// Metrics, when set, receive the queue metrics. Make them with
	// NewMetrics.
	Metrics *Metrics
	// MetricsInterval is how often the Runner reads the job counts for
	// sidekick_queue_jobs. The default is DefaultMetricsInterval.
	MetricsInterval time.Duration
	// Logger receives the Runner's logs. The default is slog.Default().
	Logger *slog.Logger
	// Schema is the Postgres schema the jobs table is in, when the pool's
	// search_path names one of its own (see store.OpenSchema). The
	// Runner's advisory locks are keyed by it, so a Runner in one schema
	// never waits for, or holds up, Runners in another schema of the same
	// database. Empty means the default schema.
	Schema string
}

// Runner claims ready jobs from the queue and runs the handler registered
// for each kind. Register every handler, then call Run once.
type Runner struct {
	pool *pgxpool.Pool
	opts Options
	// limits is nil when Options.Limits is not set.
	limits *limitParams
	log    *slog.Logger
	// backoff gives the wait before a failed job's next try, and
	// handBackGrace is the time handlers have to return at shutdown after
	// their context is cancelled. Tests shorten both.
	backoff       func(attempts int32) time.Duration
	handBackGrace time.Duration

	mu      sync.Mutex
	workers map[string]*worker
	started bool

	active activeJobs
}

// worker runs the jobs of one kind.
type worker struct {
	kind    string
	handler Handler
	opts    KindOptions
	limited bool
	// slots holds one value for each job of this kind that is running.
	slots chan struct{}
	// wake asks the kind's dispatcher to look for ready jobs.
	wake chan struct{}
}

// NewRunner returns a Runner that runs jobs from the database behind pool.
// The Runner holds one of the pool's connections for as long as it runs, to
// listen for jobs_ready, and briefly takes more for claims and updates.
func NewRunner(pool *pgxpool.Pool, opts Options) (*Runner, error) {
	if pool == nil {
		return nil, errors.New("new runner: pool is nil")
	}

	opts.PollInterval = orDefault(opts.PollInterval, DefaultPollInterval)
	opts.ReapInterval = orDefault(opts.ReapInterval, DefaultReapInterval)
	opts.ShutdownTimeout = orDefault(opts.ShutdownTimeout, DefaultShutdownTimeout)
	opts.MetricsInterval = orDefault(opts.MetricsInterval, DefaultMetricsInterval)

	if opts.PollInterval < 0 || opts.ReapInterval < 0 || opts.ShutdownTimeout < 0 || opts.MetricsInterval < 0 {
		return nil, errors.New("new runner: intervals and timeouts must not be negative")
	}

	if opts.WorkerID == "" {
		opts.WorkerID = defaultWorkerID()
	}

	var limits *limitParams

	if opts.Limits.Global != 0 || len(opts.Limits.Routes) > 0 {
		params, err := opts.Limits.params()
		if err != nil {
			return nil, fmt.Errorf("new runner: Options.Limits: %w", err)
		}

		params.lockKey = store.SchemaLockKey(limitedClaimLockKey, opts.Schema)
		limits = &params
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Runner{
		pool:          pool,
		opts:          opts,
		limits:        limits,
		log:           logger.With("component", "queue", "worker", opts.WorkerID),
		backoff:       Backoff,
		handBackGrace: handBackGrace,
		workers:       map[string]*worker{},
	}, nil
}

// Register sets the handler for jobs of kind. Each kind has one handler,
// and handlers must be registered before Run.
func (r *Runner) Register(kind string, handler Handler, opts KindOptions) error {
	if kind == "" || handler == nil {
		return errors.New("register handler: kind and handler are required")
	}

	opts, err := kindDefaults(opts)
	if err != nil {
		return fmt.Errorf("register %s handler: %w", kind, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return fmt.Errorf("register %s handler: the runner has already started", kind)
	}

	if _, ok := r.workers[kind]; ok {
		return fmt.Errorf("register %s handler: kind already has a handler", kind)
	}

	limited := slices.Contains(limitedKinds, kind)
	if limited && r.limits == nil {
		return fmt.Errorf("register %s handler: Options.Limits must be set for investigation kinds", kind)
	}

	r.workers[kind] = &worker{
		kind:    kind,
		handler: handler,
		opts:    opts,
		limited: limited,
		slots:   make(chan struct{}, opts.Concurrency),
		wake:    make(chan struct{}, 1),
	}

	return nil
}

// Run claims and runs jobs until ctx ends, then shuts down gracefully: it
// stops claiming, lets running handlers finish for up to
// Options.ShutdownTimeout, then cancels their contexts and hands their jobs
// back as pending with attempts unchanged, so another replica picks them up
// at once. Run returns nil after a graceful shutdown.
func (r *Runner) Run(ctx context.Context) error {
	workers, err := r.start()
	if err != nil {
		return fmt.Errorf("run queue: %w", err)
	}

	// Handlers run on a context of their own, so that they can finish after
	// ctx ends.
	workCtx, stopWork := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stopWork(nil)

	var loops, jobs sync.WaitGroup

	loops.Go(func() { r.listen(ctx, workers) })
	loops.Go(func() { r.reapEvery(ctx) })

	if r.opts.Metrics != nil {
		loops.Go(func() { r.countEvery(ctx) })
	}

	for _, w := range workers {
		loops.Go(func() { r.dispatch(ctx, workCtx, w, &jobs) })
	}

	loops.Wait()
	r.shutdown(&jobs, stopWork)

	return nil
}

// start marks the Runner as started and returns its workers.
func (r *Runner) start() (map[string]*worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return nil, errors.New("the runner has already run")
	}

	if len(r.workers) == 0 {
		return nil, errors.New("no handlers are registered")
	}

	r.started = true

	return r.workers, nil
}

// listen wakes the dispatcher of a kind whenever a job of that kind is
// enqueued. When the connection fails it listens again after a pause.
func (r *Runner) listen(ctx context.Context, workers map[string]*worker) {
	for {
		err := r.listenOnce(ctx, workers)
		if ctx.Err() != nil {
			return
		}

		r.log.Warn("listening for new jobs failed, polling until it works again", "error", err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(listenRetryInterval):
		}
	}
}

func (r *Runner) listenOnce(ctx context.Context, workers map[string]*worker) error {
	pooled, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}

	// The connection leaves the pool, so it never goes back still
	// listening.
	conn := pooled.Hijack()

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
		defer cancel()

		if err := conn.Close(closeCtx); err != nil {
			r.log.Warn("closing the listen connection failed", "error", err)
		}
	}()

	if err := db.New(conn).ListenJobsReady(ctx); err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// Jobs may have been enqueued while no connection was listening.
	for _, w := range workers {
		signal(w.wake)
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}

		if w, ok := workers[notification.Payload]; ok {
			signal(w.wake)
		}
	}
}

// reapEvery reaps expired leases every Options.ReapInterval until ctx ends.
func (r *Runner) reapEvery(ctx context.Context) {
	ticker := time.NewTicker(r.opts.ReapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		r.reapOnce(ctx)
	}
}

// reapOnce reaps expired leases, if no other replica is reaping, and runs
// the OnDead hooks of jobs that became dead.
func (r *Runner) reapOnce(ctx context.Context) {
	reapCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()

	reaped, _, err := reap(reapCtx, r.pool, store.SchemaLockKey(reaperLockKey, r.opts.Schema))
	if err != nil {
		r.log.Error("reaping expired leases failed", "error", err)
	}

	for _, job := range reaped {
		r.log.Warn("a job's lease expired", jobAttrs(job)...)

		if job.Status == StatusDead {
			r.jobDied(r.workerFor(job.Kind), job)
		}
	}
}

// countEvery sets sidekick_queue_jobs from the database every
// Options.MetricsInterval until ctx ends.
func (r *Runner) countEvery(ctx context.Context) {
	ticker := time.NewTicker(r.opts.MetricsInterval)
	defer ticker.Stop()

	for {
		r.countJobs(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) countJobs(ctx context.Context) {
	countCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	counts, err := db.New(r.pool).CountJobs(countCtx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warn("counting jobs for the metrics failed", "error", err)
		}

		return
	}

	r.opts.Metrics.setCounts(counts)
}

// workerFor returns the worker of kind, or nil when this Runner has no
// handler for it.
func (r *Runner) workerFor(kind string) *worker {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.workers[kind]
}

// dispatch claims ready jobs of one kind whenever it has free slots and is
// woken by a notification, a finished job or the poll timer, and starts a
// goroutine for each job. It returns when ctx ends.
func (r *Runner) dispatch(ctx, workCtx context.Context, w *worker, jobs *sync.WaitGroup) {
	ticker := time.NewTicker(r.opts.PollInterval)
	defer ticker.Stop()

	for {
		r.claimReady(ctx, workCtx, w, jobs)

		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

// claimReady claims as many ready jobs of w's kind as it has free slots,
// and starts them.
func (r *Runner) claimReady(ctx, workCtx context.Context, w *worker, jobs *sync.WaitGroup) {
	for ctx.Err() == nil {
		// Only this goroutine fills slots, so the free count can only grow
		// while it claims.
		free := cap(w.slots) - len(w.slots)
		if free == 0 {
			return
		}

		claimed, err := r.claim(ctx, w, free)
		if err != nil {
			r.log.Error("claiming jobs failed", "job_kind", w.kind, "error", err)

			return
		}

		for _, job := range claimed {
			w.slots <- struct{}{}

			r.active.add(job)
			r.opts.Metrics.observeWait(job, w.opts.Lease)
			jobs.Go(func() { r.runJob(workCtx, w, job) })
		}

		if len(claimed) < free {
			return
		}
	}
}

// claim claims up to limit ready jobs of w's kind. The query runs on a
// context that ctx ending does not cancel, so a claim that the database
// made is never lost on the way back.
func (r *Runner) claim(ctx context.Context, w *worker, limit int) ([]db.Job, error) {
	claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()

	if !w.limited {
		jobs, err := claim(claimCtx, r.pool, r.opts.WorkerID, w.kind, w.opts.Lease, int32(limit)) //nolint:gosec // limit is at most maxConcurrency.
		if err != nil {
			return nil, fmt.Errorf("claim: %w", err)
		}

		return jobs, nil
	}

	var jobs []db.Job

	for range limit {
		job, claimed, err := claimLimited(claimCtx, r.pool, r.opts.WorkerID, w.kind, w.opts.Lease, *r.limits)
		if err != nil {
			return jobs, fmt.Errorf("claim: %w", err)
		}

		if !claimed {
			break
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

// shutdown waits for running jobs after the loops have stopped, first
// letting handlers finish, then cancelling them so they hand their jobs
// back, and at last handing back the jobs of handlers that did not return.
// A handler that did return writes its job's result itself, even when that
// write is still going on after the grace, and shutdown waits for it.
func (r *Runner) shutdown(jobs *sync.WaitGroup, stopWork context.CancelCauseFunc) {
	if waitFor(jobs, r.opts.ShutdownTimeout) {
		return
	}

	r.log.Info("handlers did not finish in time, handing their jobs back", "timeout", r.opts.ShutdownTimeout)
	stopWork(errShuttingDown)

	if waitFor(jobs, r.handBackGrace) {
		return
	}

	for _, job := range r.active.takeAll() {
		r.log.Warn("a handler did not return after shutdown, handing its job back", jobAttrs(job)...)
		r.handBack(job)
	}

	r.active.waitResults()
}

// activeJobs holds the jobs whose handlers are running, by ID. The job
// goroutines and the shutdown share it, and it decides which of them writes
// a job's result: the job's goroutine when the handler returns first, or the
// shutdown when it takes the job while the handler still runs.
type activeJobs struct {
	mu   sync.Mutex
	jobs map[int64]db.Job
	// results counts the job goroutines whose handler has returned and
	// that have not yet written the job's result.
	results sync.WaitGroup
}

func (a *activeJobs) add(job db.Job) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.jobs == nil {
		a.jobs = map[int64]db.Job{}
	}

	a.jobs[job.ID] = job
}

// handlerReturned removes the job of a handler that has returned, and
// reports whether the caller is to write the job's result. It is false when
// the shutdown has already taken the job to hand it back. After true, the
// caller calls resultWritten once the write is done.
func (a *activeJobs) handlerReturned(id int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.jobs[id]; !ok {
		return false
	}

	delete(a.jobs, id)
	a.results.Add(1)

	return true
}

func (a *activeJobs) resultWritten() {
	a.results.Done()
}

// takeAll removes and returns the jobs whose handlers are still running, so
// that their goroutines leave the jobs alone when the handlers return.
func (a *activeJobs) takeAll() []db.Job {
	a.mu.Lock()
	defer a.mu.Unlock()

	jobs := make([]db.Job, 0, len(a.jobs))
	for _, job := range a.jobs {
		jobs = append(jobs, job)
	}

	clear(a.jobs)

	return jobs
}

// waitResults waits for the results that job goroutines are still writing.
// Call it only after takeAll: from then on no goroutine can start a write,
// so none is missed.
func (a *activeJobs) waitResults() {
	a.results.Wait()
}

// kindDefaults checks opts and fills in the defaults.
func kindDefaults(opts KindOptions) (KindOptions, error) {
	opts.Concurrency = orDefault(opts.Concurrency, DefaultConcurrency)
	if opts.Concurrency < 1 || opts.Concurrency > maxConcurrency {
		return opts, fmt.Errorf("concurrency is %d, want 1 to %d", opts.Concurrency, maxConcurrency)
	}

	if opts.Lease < 0 || opts.HeartbeatInterval < 0 {
		return opts, errors.New("lease and heartbeat interval must not be negative")
	}

	if !opts.Long {
		opts.Lease = orDefault(opts.Lease, DefaultShortLease)

		return opts, nil
	}

	opts.Lease = orDefault(opts.Lease, DefaultLongLease)
	opts.HeartbeatInterval = orDefault(opts.HeartbeatInterval, DefaultHeartbeatInterval)

	if opts.HeartbeatInterval >= opts.Lease {
		return opts, fmt.Errorf("heartbeat interval %s must be shorter than the lease %s", opts.HeartbeatInterval, opts.Lease)
	}

	return opts, nil
}

// defaultWorkerID returns the host name with a random suffix, so that two
// Runners in one process have different IDs.
func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "sidekick"
	}

	return host + "-" + ids.New().String()
}

// waitFor waits for wg for up to timeout, and reports whether it finished.
func waitFor(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// signal sends on a channel with a buffer of one without blocking. A signal
// already waiting is enough.
func signal(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// jobAttrs returns the standard log fields of a job.
func jobAttrs(job db.Job) []any {
	return []any{"job_id", job.ID, "job_kind", job.Kind, "attempt", job.Attempts}
}
