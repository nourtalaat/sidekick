//go:build integration

package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	// testPoll is the poll interval of Runners in tests.
	testPoll = 50 * time.Millisecond
	// testReap is the reap interval of Runners in tests.
	testReap = 50 * time.Millisecond
	// waitTimeout is how long a test waits for the queue to reach a state.
	waitTimeout = 30 * time.Second
	// waitTick is how often a test checks the queue while it waits.
	waitTick = 20 * time.Millisecond
)

// TestTwoRunnersRunEachJobOnce runs two Runners, as two replicas, on one
// database and checks that each of 1,000 jobs runs exactly once.
func TestTwoRunnersRunEachJobOnce(t *testing.T) {
	t.Parallel()

	const (
		jobsPerKind = 500
		concurrency = 8
	)

	kinds := []string{KindPublish, KindRouteEvent}
	url := storetest.NewURL(t)
	runs := newRunCounter()

	var runners []*Runner

	for i := range 2 {
		runner := newTestRunner(t, openReplica(t, url), Options{WorkerID: fmt.Sprintf("replica-%d", i)})

		for _, kind := range kinds {
			require.NoError(t, runner.Register(kind, HandlerFunc(func(_ context.Context, job db.Job) error {
				runs.add(job.ID, runner.opts.WorkerID)

				return nil
			}), KindOptions{Concurrency: concurrency}))
		}

		runners = append(runners, runner)
	}

	pool := openReplica(t, url)
	stop := startRunners(t, runners...)

	ids := make([]int64, 0, jobsPerKind*len(kinds))

	for i := range jobsPerKind {
		for _, kind := range kinds {
			ids = append(ids, mustEnqueue(t, pool, NewJob{Kind: kind, Payload: fmt.Appendf(nil, `{"n":%d}`, i)}))
		}
	}

	waitForCount(t, pool, StatusDone, len(ids))
	stop()

	counts, workers := runs.snapshot()
	for _, id := range ids {
		require.Equal(t, 1, counts[id], "job %d", id)
	}

	require.Len(t, counts, len(ids))
	require.Len(t, workers, len(runners), "both replicas ran jobs")
}

func TestRunnerRetriesFailedJobs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		maxAttempts int32
		handler     func(job db.Job) error
		// wantRuns is how many times the handler runs before the job
		// settles.
		wantRuns     int
		wantStatus   string
		wantAttempts int32
		wantError    string
	}{
		{
			name:        "succeeds on a retry",
			maxAttempts: 3,
			handler: func(job db.Job) error {
				if job.Attempts < 2 {
					return errors.New("not yet")
				}

				return nil
			},
			wantRuns: 3, wantStatus: StatusDone, wantAttempts: 2, wantError: "publish handler: not yet",
		},
		{
			name:        "dead after its last attempt",
			maxAttempts: 3,
			handler:     func(db.Job) error { return errors.New("slack is down") },
			wantRuns:    3, wantStatus: StatusDead, wantAttempts: 3, wantError: "publish handler: slack is down",
		},
		{
			name:        "dead at once when the handler gives up",
			maxAttempts: 3,
			handler: func(db.Job) error {
				return fmt.Errorf("channel archived: %w", ErrGiveUp)
			},
			wantRuns: 1, wantStatus: StatusDead, wantAttempts: 1, wantError: "publish handler: channel archived: give up on the job",
		},
		{
			name:        "a panic is a failure",
			maxAttempts: 1,
			handler:     func(db.Job) error { panic("bad payload") },
			wantRuns:    1, wantStatus: StatusDead, wantAttempts: 1, wantError: "publish handler panicked: bad payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			runs := newRunCounter()
			dead := newDeadRecorder()

			runner := newTestRunner(t, pool, Options{})
			require.NoError(t, runner.Register(KindPublish, HandlerFunc(func(_ context.Context, job db.Job) error {
				runs.add(job.ID, "")

				return tt.handler(job)
			}), KindOptions{OnDead: dead.record}))

			id := mustEnqueue(t, pool, NewJob{Kind: KindPublish, MaxAttempts: tt.maxAttempts})
			stop := startRunners(t, runner)

			job := waitForStatus(t, pool, id, tt.wantStatus)
			stop()

			require.Equal(t, tt.wantAttempts, job.Attempts)
			require.Equal(t, new(tt.wantError), job.LastError)

			counts, _ := runs.snapshot()
			require.Equal(t, tt.wantRuns, counts[id])

			if tt.wantStatus == StatusDead {
				require.Equal(t, []db.Job{job}, dead.jobs(), "OnDead sees the dead job once")
			} else {
				require.Empty(t, dead.jobs())
			}
		})
	}
}

// TestOnDeadAfterLeaseExpiry checks that the OnDead hook also runs when the
// reaper marks a job dead because its lease expired on its last attempt.
func TestOnDeadAfterLeaseExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		onDeadErr  error
		wantStatus string
	}{
		{name: "hook succeeds", wantStatus: StatusDead},
		{name: "hook error leaves the job dead", onDeadErr: errors.New("slack is down"), wantStatus: StatusDead},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			dead := newDeadRecorder()

			// A worker of another replica claims the job and then crashes.
			id := mustEnqueue(t, pool, NewJob{Kind: KindInvestigate, MaxAttempts: 1})
			_, err := claim(t.Context(), pool, "crashed-replica", KindInvestigate, shortLease, 1)
			require.NoError(t, err)

			runner := newTestRunner(t, pool, Options{Limits: Limits{Global: 1}})
			require.NoError(t, runner.Register(KindInvestigate, HandlerFunc(func(context.Context, db.Job) error {
				return errors.New("the handler must not run")
			}), KindOptions{Long: true, OnDead: func(ctx context.Context, job db.Job) error {
				_ = dead.record(ctx, job)

				return tt.onDeadErr
			}}))

			stop := startRunners(t, runner)
			job := waitForStatus(t, pool, id, StatusDead)

			eventually(t, func() bool { return len(dead.jobs()) > 0 })
			stop()

			require.Equal(t, []db.Job{job}, dead.jobs())
			require.Equal(t, new("lease expired"), job.LastError)
			require.Equal(t, tt.wantStatus, getJob(t, pool, id).Status)
		})
	}
}

func TestHeartbeatKeepsALongJob(t *testing.T) {
	t.Parallel()

	const (
		lease     = 300 * time.Millisecond
		heartbeat = 50 * time.Millisecond
		jobTime   = 4 * lease
	)

	pool := setup(t)
	runner := newTestRunner(t, pool, Options{})
	require.NoError(t, runner.Register(KindBenchGate, HandlerFunc(func(ctx context.Context, _ db.Job) error {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(jobTime):
			return nil
		}
	}), KindOptions{Long: true, Lease: lease, HeartbeatInterval: heartbeat}))

	id := mustEnqueue(t, pool, NewJob{Kind: KindBenchGate})
	stop := startRunners(t, runner)

	job := waitForStatus(t, pool, id, StatusDone)
	stop()

	require.Zero(t, job.Attempts, "the reaper never took the job")
}

// TestLostLeaseStopsTheHandler takes a running job away from its worker, as
// the reaper and another replica would, and checks that the next heartbeat
// cancels the handler and that the worker leaves the job alone.
func TestLostLeaseStopsTheHandler(t *testing.T) {
	t.Parallel()

	const (
		lease     = time.Minute
		heartbeat = 50 * time.Millisecond
	)

	pool := setup(t)
	started := make(chan db.Job, 1)
	causes := make(chan error, 1)

	runner := newTestRunner(t, pool, Options{})
	require.NoError(t, runner.Register(KindBenchGate, HandlerFunc(func(ctx context.Context, job db.Job) error {
		started <- job
		<-ctx.Done()
		causes <- context.Cause(ctx)

		return ctx.Err()
	}), KindOptions{Long: true, Lease: lease, HeartbeatInterval: heartbeat}))

	id := mustEnqueue(t, pool, NewJob{Kind: KindBenchGate})
	stop := startRunners(t, runner)
	defer stop()

	job := <-started

	_, err := pool.Exec(t.Context(), "UPDATE jobs SET claim_token = nextval('job_claim_seq'), locked_by = 'other-replica' WHERE id = $1", id)
	require.NoError(t, err)
	require.ErrorIs(t, <-causes, ErrLeaseLost)

	stop()

	taken := getJob(t, pool, id)
	require.Equal(t, StatusRunning, taken.Status, "the old worker did not fail or hand back the job")
	require.Equal(t, new("other-replica"), taken.LockedBy)
	require.Greater(t, *taken.ClaimToken, *job.ClaimToken)
}

func TestShortJobStopsAtTheEndOfItsLease(t *testing.T) {
	t.Parallel()

	const lease = 200 * time.Millisecond

	pool := setup(t)
	// The reaper would race the handler for the expired lease.
	runner := newTestRunner(t, pool, Options{ReapInterval: time.Hour})
	require.NoError(t, runner.Register(KindPublish, HandlerFunc(func(ctx context.Context, _ db.Job) error {
		<-ctx.Done()

		return context.Cause(ctx)
	}), KindOptions{Lease: lease}))

	id := mustEnqueue(t, pool, NewJob{Kind: KindPublish, MaxAttempts: 1})
	stop := startRunners(t, runner)

	job := waitForStatus(t, pool, id, StatusDead)
	stop()

	require.Equal(t, new("publish handler: "+errLeaseEnded.Error()), job.LastError)
}

func TestShutdown(t *testing.T) {
	t.Parallel()

	const (
		// runsOut is the shutdown timeout or grace of a case whose handlers
		// outlast it. holds is one that every handler of the case returns
		// within, so that the case does not depend on how fast the machine
		// is: shutdown goes on as soon as the handlers return.
		runsOut = 100 * time.Millisecond
		holds   = waitTimeout
		// handlerTime is how long a handler that finishes in time runs.
		handlerTime = 50 * time.Millisecond
		// startAttempts is the attempts each job has used before the test,
		// to show that the hand-back leaves them alone.
		startAttempts = 2
		jobs          = 3
	)

	tests := []struct {
		name string
		// handler runs until ctx ends or release is closed, whichever it
		// heeds.
		handler         func(ctx context.Context, release <-chan struct{}) error
		shutdownTimeout time.Duration
		grace           time.Duration
		wantStatus      string
		wantAttempts    int32
	}{
		{
			name: "a handler that finishes in time completes",
			handler: func(context.Context, <-chan struct{}) error {
				time.Sleep(handlerTime)

				return nil
			},
			shutdownTimeout: holds,
			grace:           runsOut,
			wantStatus:      StatusDone,
			wantAttempts:    startAttempts,
		},
		{
			name: "a handler that stops when cancelled is handed back",
			handler: func(ctx context.Context, _ <-chan struct{}) error {
				<-ctx.Done()

				return ctx.Err()
			},
			shutdownTimeout: runsOut,
			grace:           holds,
			wantStatus:      StatusPending,
			wantAttempts:    startAttempts,
		},
		{
			name: "a handler that finishes on cancel still completes",
			handler: func(ctx context.Context, _ <-chan struct{}) error {
				<-ctx.Done()

				return nil
			},
			shutdownTimeout: runsOut,
			grace:           holds,
			wantStatus:      StatusDone,
			wantAttempts:    startAttempts,
		},
		{
			name: "a handler that ignores the cancel is handed back by the runner",
			handler: func(_ context.Context, release <-chan struct{}) error {
				<-release

				return nil
			},
			shutdownTimeout: runsOut,
			grace:           runsOut,
			wantStatus:      StatusPending,
			wantAttempts:    startAttempts,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			release := make(chan struct{})
			started := make(chan struct{}, jobs)

			runner := newTestRunner(t, pool, Options{ShutdownTimeout: tt.shutdownTimeout})
			runner.handBackGrace = tt.grace
			require.NoError(t, runner.Register(KindBenchGate, HandlerFunc(func(ctx context.Context, _ db.Job) error {
				started <- struct{}{}

				return tt.handler(ctx, release)
			}), KindOptions{Long: true, Concurrency: jobs}))

			ids := make([]int64, jobs)
			for i := range ids {
				ids[i] = mustEnqueue(t, pool, NewJob{Kind: KindBenchGate})
			}

			_, err := pool.Exec(t.Context(), "UPDATE jobs SET attempts = $1", startAttempts)
			require.NoError(t, err)

			stop := startRunners(t, runner)
			for range jobs {
				<-started
			}

			stop()
			close(release)

			for _, id := range ids {
				job := getJob(t, pool, id)
				require.Equal(t, tt.wantStatus, job.Status, "job %d", id)
				require.Equal(t, tt.wantAttempts, job.Attempts, "job %d", id)

				if tt.wantStatus == StatusPending {
					require.Nil(t, job.LockedBy)
					require.Nil(t, job.LockedUntil)
					// run_at is the time of the hand-back, which is in the past by
					// however long the shutdown and these checks took.
					require.LessOrEqual(t, secondsUntilRunAt(t, pool, id), 0.0, "ready at once")
				}
			}
		})
	}
}

// TestShutdownWaitsForTheResultOfAReturnedHandler checks that shutdown
// leaves a job whose handler has returned to the job's own goroutine, even
// when writing the job's result goes on past the hand-back grace, as it can
// on a busy machine or database. A hand-back that got in before that write
// would put a finished job back in the queue to run again.
func TestShutdownWaitsForTheResultOfAReturnedHandler(t *testing.T) {
	t.Parallel()

	const (
		shutdownTimeout = 10 * time.Millisecond
		grace           = 10 * time.Millisecond
	)

	pool := setup(t)
	handedBack := &handBackRecorder{}
	started := make(chan struct{}, 2)
	// finish lets the handler of finishedID return; release lets the
	// other handler, which ignores the cancel, return.
	finish := make(chan struct{})
	release := make(chan struct{})

	finishedID := mustEnqueue(t, pool, NewJob{Kind: KindPublish})
	stuckID := mustEnqueue(t, pool, NewJob{Kind: KindPublish})

	// The reaper is kept out of the way of the row lock below.
	runner := newTestRunner(t, pool, Options{
		ShutdownTimeout: shutdownTimeout,
		ReapInterval:    time.Hour,
		Logger:          slog.New(handedBack),
	})
	runner.handBackGrace = grace
	require.NoError(t, runner.Register(KindPublish, HandlerFunc(func(_ context.Context, job db.Job) error {
		started <- struct{}{}

		if job.ID == finishedID {
			<-finish
		} else {
			<-release
		}

		return nil
	}), KindOptions{Concurrency: 2}))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ran := make(chan error, 1)

	go func() { ran <- runner.Run(ctx) }()

	for range 2 {
		<-started
	}

	// Hold the finished job's row, so that writing its result waits.
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	t.Cleanup(func() { close(release) })

	_, err = tx.Exec(t.Context(), "SELECT 1 FROM jobs WHERE id = $1 FOR UPDATE", finishedID)
	require.NoError(t, err)

	close(finish)
	eventually(t, func() bool { return lockWaiters(t, pool) > 0 })

	cancel()
	eventually(t, func() bool { return len(handedBack.jobIDs()) > 0 })
	require.NoError(t, tx.Rollback(t.Context()))
	require.NoError(t, <-ran)

	require.Equal(t, []int64{stuckID}, handedBack.jobIDs(), "only the job whose handler still runs is handed back")
	require.Equal(t, StatusDone, getJob(t, pool, finishedID).Status)
	require.Equal(t, StatusPending, getJob(t, pool, stuckID).Status)
}

// TestLimitsHoldWithTwoRunners runs investigations on two Runners at once
// and checks the per-route and global limits.
func TestLimitsHoldWithTwoRunners(t *testing.T) {
	t.Parallel()

	const (
		jobsPerRoute = 10
		concurrency  = 4
		jobTime      = 30 * time.Millisecond
	)

	limits := Limits{Global: 3, Routes: map[string]int{"a": 1, "b": 2}}
	routes := []string{"a", "b", "c"}
	url := storetest.NewURL(t)
	tracker := newConcurrencyTracker(len(routes)*jobsPerRoute, limits.Global)

	handler := HandlerFunc(func(_ context.Context, job db.Job) error {
		route := deref(job.Route)
		tracker.start(route)
		time.Sleep(jobTime)
		tracker.stop(route)

		return nil
	})

	var runners []*Runner

	for range 2 {
		runner := newTestRunner(t, openReplica(t, url), Options{Limits: limits})
		for _, kind := range limitedKinds {
			require.NoError(t, runner.Register(kind, handler, KindOptions{Long: true, Concurrency: concurrency}))
		}

		runners = append(runners, runner)
	}

	pool := openReplica(t, url)

	for _, route := range routes {
		for i := range jobsPerRoute {
			mustEnqueue(t, pool, NewJob{Kind: limitedKinds[i%len(limitedKinds)], Route: route})
		}
	}

	stop := startRunners(t, runners...)

	waitForCount(t, pool, StatusDone, len(routes)*jobsPerRoute)
	stop()

	peaks, globalPeak := tracker.peaks()
	require.LessOrEqual(t, globalPeak, limits.Global)
	require.Positive(t, globalPeak)
	require.LessOrEqual(t, peaks["a"], limits.Routes["a"])
	require.LessOrEqual(t, peaks["b"], limits.Routes["b"])
}

// TestRunnerMetrics checks the queue metrics that a Runner records.
func TestRunnerMetrics(t *testing.T) {
	t.Parallel()

	const metricsInterval = 50 * time.Millisecond

	pool := setup(t)
	metrics, err := NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	runner := newTestRunner(t, pool, Options{Metrics: metrics, MetricsInterval: metricsInterval})
	require.NoError(t, runner.Register(KindPublish, HandlerFunc(func(context.Context, db.Job) error {
		return fmt.Errorf("channel archived: %w", ErrGiveUp)
	}), KindOptions{}))

	// No Runner handles rollup jobs, so this one stays pending.
	mustEnqueue(t, pool, NewJob{Kind: KindRollup})
	id := mustEnqueue(t, pool, NewJob{Kind: KindPublish})

	stop := startRunners(t, runner)
	waitForStatus(t, pool, id, StatusDead)

	eventually(t, func() bool {
		return testutil.ToFloat64(metrics.jobs.WithLabelValues(KindPublish, StatusDead)) == 1
	})
	stop()

	require.InDelta(t, 1, testutil.ToFloat64(metrics.jobs.WithLabelValues(KindRollup, StatusPending)), 0)
	require.InDelta(t, 1, testutil.ToFloat64(metrics.dead.WithLabelValues(KindPublish)), 0)

	count, _ := histogramOf(t, metrics.wait, KindPublish)
	require.Equal(t, uint64(1), count)
}

// newTestRunner returns a Runner with short intervals and no backoff.
func newTestRunner(t *testing.T, pool *pgxpool.Pool, opts Options) *Runner {
	t.Helper()

	opts.PollInterval = orDefault(opts.PollInterval, testPoll)
	opts.ReapInterval = orDefault(opts.ReapInterval, testReap)

	runner, err := NewRunner(pool, opts)
	require.NoError(t, err)

	runner.backoff = func(int32) time.Duration { return 0 }

	return runner
}

// startRunners runs each Runner until the returned function is called. The
// function waits for every Run to return and checks its error, and may be
// called more than once.
func startRunners(t *testing.T, runners ...*Runner) (stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, len(runners))

	for _, runner := range runners {
		go func() { errs <- runner.Run(ctx) }()
	}

	var once sync.Once

	stop = func() {
		once.Do(func() {
			cancel()

			for range runners {
				require.NoError(t, <-errs)
			}
		})
	}

	t.Cleanup(stop)

	return stop
}

// waitForStatus waits until the job has status, and returns it.
func waitForStatus(t *testing.T, pool *pgxpool.Pool, id int64, status string) db.Job {
	t.Helper()

	var job db.Job

	eventually(t, func() bool {
		job = getJob(t, pool, id)

		return job.Status == status
	})

	return job
}

// waitForCount waits until want jobs have status.
func waitForCount(t *testing.T, pool *pgxpool.Pool, status string, want int) {
	t.Helper()

	eventually(t, func() bool {
		var count int

		require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE status = $1", status).Scan(&count))

		return count >= want
	})
}

// eventually checks done every waitTick until it is true, and fails the
// test after waitTimeout.
func eventually(t *testing.T, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	for !done() {
		if time.Now().After(deadline) {
			require.FailNow(t, "the queue did not reach the expected state", "waited %s", waitTimeout)
		}

		time.Sleep(waitTick)
	}
}

// lockWaiters returns how many sessions on the test's database wait for
// a lock.
func lockWaiters(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var count int

	err := pool.QueryRow(t.Context(),
		"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'").Scan(&count)
	require.NoError(t, err)

	return count
}

// handBackRecorder is a slog.Handler that keeps the IDs of the jobs that a
// Runner logs as handed back by its shutdown. It passes every other record
// on to the default logger, so that a failing test still shows them.
type handBackRecorder struct {
	mu  sync.Mutex
	ids []int64
}

func (h *handBackRecorder) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *handBackRecorder) Handle(ctx context.Context, record slog.Record) error {
	if record.Message != "a handler did not return after shutdown, handing its job back" {
		if err := slog.Default().Handler().Handle(ctx, record); err != nil {
			return fmt.Errorf("pass the record on: %w", err)
		}

		return nil
	}

	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != "job_id" {
			return true
		}

		h.mu.Lock()
		defer h.mu.Unlock()

		h.ids = append(h.ids, attr.Value.Int64())

		return false
	})

	return nil
}

func (h *handBackRecorder) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *handBackRecorder) WithGroup(string) slog.Handler {
	return h
}

func (h *handBackRecorder) jobIDs() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]int64(nil), h.ids...)
}

// runCounter counts how many times each job ran and which workers ran
// them. Handlers on several goroutines share it.
type runCounter struct {
	mu      sync.Mutex
	counts  map[int64]int
	workers map[string]bool
}

func newRunCounter() *runCounter {
	return &runCounter{counts: map[int64]int{}, workers: map[string]bool{}}
}

func (c *runCounter) add(id int64, worker string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.counts[id]++
	c.workers[worker] = true
}

func (c *runCounter) snapshot() (map[int64]int, map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.counts, c.workers
}

// deadRecorder keeps the jobs that an OnDead hook saw.
type deadRecorder struct {
	mu   sync.Mutex
	seen []db.Job
}

func newDeadRecorder() *deadRecorder {
	return &deadRecorder{}
}

func (d *deadRecorder) record(_ context.Context, job db.Job) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.seen = append(d.seen, job)

	return nil
}

func (d *deadRecorder) jobs() []db.Job {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]db.Job(nil), d.seen...)
}
