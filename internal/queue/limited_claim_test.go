//go:build integration

package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// testJob is a job that a limits test enqueues.
type testJob struct {
	kind  string
	route string
}

func TestClaimLimitedRespectsLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limits  Limits
		running []testJob
		// pending are enqueued oldest first, all ready.
		pending   []testJob
		claimKind string
		// wantRoute is the route of the job that should be claimed; nil
		// means none may start.
		wantRoute *string
	}{
		{
			name:      "under every limit",
			limits:    Limits{Global: 2, Routes: map[string]int{"a": 1}},
			pending:   []testJob{{KindInvestigate, "a"}},
			claimKind: KindInvestigate,
			wantRoute: new("a"),
		},
		{
			name:      "a route at its limit is skipped for a newer job",
			limits:    Limits{Global: 10, Routes: map[string]int{"a": 1}},
			running:   []testJob{{KindInvestigate, "a"}},
			pending:   []testJob{{KindInvestigate, "a"}, {KindInvestigate, "b"}},
			claimKind: KindInvestigate,
			wantRoute: new("b"),
		},
		{
			name:      "global limit reached",
			limits:    Limits{Global: 2},
			running:   []testJob{{KindInvestigate, "a"}, {KindFollowUp, "b"}},
			pending:   []testJob{{KindInvestigate, "c"}},
			claimKind: KindInvestigate,
		},
		{
			name:      "follow-ups count toward the route limit",
			limits:    Limits{Global: 10, Routes: map[string]int{"a": 1}},
			running:   []testJob{{KindFollowUp, "a"}},
			pending:   []testJob{{KindInvestigate, "a"}},
			claimKind: KindInvestigate,
		},
		{
			name:      "investigations hold back follow-ups",
			limits:    Limits{Global: 1},
			running:   []testJob{{KindInvestigate, "a"}},
			pending:   []testJob{{KindFollowUp, "b"}},
			claimKind: KindFollowUp,
		},
		{
			name:      "other kinds do not count",
			limits:    Limits{Global: 1, Routes: map[string]int{"a": 1}},
			running:   []testJob{{KindPublish, "a"}, {KindBenchGate, "a"}},
			pending:   []testJob{{KindInvestigate, "a"}},
			claimKind: KindInvestigate,
			wantRoute: new("a"),
		},
		{
			name:      "a route without a limit has only the global one",
			limits:    Limits{Global: 5, Routes: map[string]int{"a": 1}},
			running:   []testJob{{KindInvestigate, "b"}, {KindInvestigate, "b"}, {KindInvestigate, "b"}},
			pending:   []testJob{{KindInvestigate, "b"}},
			claimKind: KindInvestigate,
			wantRoute: new("b"),
		},
		{
			name:      "a job without a route has only the global limit",
			limits:    Limits{Global: 2, Routes: map[string]int{"a": 0}},
			running:   []testJob{{KindInvestigate, ""}},
			pending:   []testJob{{KindInvestigate, ""}},
			claimKind: KindInvestigate,
			wantRoute: new(""),
		},
		{
			name:      "a route limit of zero stops the route",
			limits:    Limits{Global: 2, Routes: map[string]int{"a": 0}},
			pending:   []testJob{{KindInvestigate, "a"}},
			claimKind: KindInvestigate,
		},
		{
			name:      "only the asked kind is claimed",
			limits:    Limits{Global: 2},
			pending:   []testJob{{KindFollowUp, "a"}},
			claimKind: KindInvestigate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			for _, job := range tt.running {
				mustEnqueue(t, pool, NewJob{Kind: job.kind, Route: job.route})
				mustClaimOne(t, pool, job.kind)
			}

			base := time.Now().Add(-time.Hour)
			for i, job := range tt.pending {
				mustEnqueue(t, pool, NewJob{Kind: job.kind, Route: job.route, RunAt: base.Add(time.Duration(i) * time.Second)})
			}

			params, err := tt.limits.params()
			require.NoError(t, err)

			job, claimed, err := claimLimited(t.Context(), pool, testWorker, tt.claimKind, testLease, params)
			require.NoError(t, err)

			if tt.wantRoute == nil {
				require.False(t, claimed, "claimed job %d on route %v", job.ID, job.Route)

				return
			}

			require.True(t, claimed)
			require.Equal(t, tt.claimKind, job.Kind)
			require.Equal(t, StatusRunning, job.Status)
			require.Equal(t, *tt.wantRoute, deref(job.Route))
		})
	}
}

// TestClaimLimitedSeesTheClaimBeforeIt holds the claim lock in one
// transaction that has claimed a job, starts a second claim that waits for
// the lock, then commits the first. The second claim must count the first
// claim's job as running: it takes its snapshot after it gets the lock,
// because the lock is taken in a statement of its own.
func TestClaimLimitedSeesTheClaimBeforeIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		global      int
		wantClaimed bool
	}{
		{name: "the first claim fills the global limit", global: 1},
		{name: "room is left after the first claim", global: 2, wantClaimed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			base := time.Now().Add(-time.Hour)
			firstID := mustEnqueue(t, pool, NewJob{Kind: KindInvestigate, RunAt: base})
			secondID := mustEnqueue(t, pool, NewJob{Kind: KindInvestigate, RunAt: base.Add(time.Second)})

			params, err := Limits{Global: tt.global}.params()
			require.NoError(t, err)

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) })

			queries := db.New(tx)
			require.NoError(t, queries.LockLimitedClaims(t.Context(), limitedClaimLockKey))

			first, err := queries.ClaimLimitedJob(t.Context(), db.ClaimLimitedJobParams{
				Worker:       testWorker,
				LeaseSeconds: testLease.Seconds(),
				LimitedKinds: limitedKinds,
				Routes:       params.routes,
				RouteLimits:  params.routeLimits,
				Kind:         KindInvestigate,
				GlobalLimit:  params.global,
			})
			require.NoError(t, err)
			require.Equal(t, firstID, first.ID)

			type claimResult struct {
				job     db.Job
				claimed bool
				err     error
			}

			results := make(chan claimResult, 1)

			go func() {
				job, claimed, err := claimLimited(t.Context(), pool, testWorker, KindInvestigate, testLease, params)
				results <- claimResult{job: job, claimed: claimed, err: err}
			}()

			eventually(t, func() bool { return waitsForAdvisoryLock(t, pool) })
			require.NoError(t, tx.Commit(t.Context()))

			second := <-results
			require.NoError(t, second.err)
			require.Equal(t, tt.wantClaimed, second.claimed)

			if tt.wantClaimed {
				require.Equal(t, secondID, second.job.ID)
			}
		})
	}
}

// TestLimitsHoldUnderConcurrentClaims runs several claimers on each of two
// replicas against one database and checks that the per-route and global
// limits are never passed, and that the global limit is reached.
//
// The first jobs keep running until the global limit is reached, so that
// reaching it does not depend on claims being fast next to jobTime: on a
// slow database link, three claims in a row take longer than one job.
func TestLimitsHoldUnderConcurrentClaims(t *testing.T) {
	t.Parallel()

	const (
		claimersPerReplica = 4
		jobsPerRoute       = 12
		jobTime            = 20 * time.Millisecond
		idleWait           = 5 * time.Millisecond
		fillTimeout        = 10 * time.Second
		testTimeout        = 30 * time.Second
	)

	limits := Limits{Global: 3, Routes: map[string]int{"a": 1, "b": 2}}
	routes := []string{"a", "b", "c"}

	url := storetest.NewURL(t)
	replicas := []*pgxpool.Pool{openReplica(t, url), openReplica(t, url)}

	for _, route := range routes {
		for i := range jobsPerRoute {
			kind := KindInvestigate
			if i%2 == 1 {
				kind = KindFollowUp
			}

			mustEnqueue(t, replicas[0], NewJob{Kind: kind, Route: route})
		}
	}

	params, err := limits.params()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()

	tracker := newConcurrencyTracker(len(routes)*jobsPerRoute, limits.Global)
	group, ctx := errgroup.WithContext(ctx)

	fillCtx, cancelFill := context.WithTimeout(ctx, fillTimeout)
	defer cancelFill()

	for r, pool := range replicas {
		for c := range claimersPerReplica {
			kind := KindInvestigate
			if c%2 == 1 {
				kind = KindFollowUp
			}

			worker := fmt.Sprintf("replica-%d-claimer-%d", r, c)

			group.Go(func() error {
				for !tracker.allDone() {
					job, claimed, err := claimLimited(ctx, pool, worker, kind, testLease, params)
					if err != nil {
						return fmt.Errorf("%s: %w", worker, err)
					}

					if !claimed {
						time.Sleep(idleWait)

						continue
					}

					route := deref(job.Route)
					tracker.start(route)

					if err := tracker.observeStored(ctx, pool); err != nil {
						return fmt.Errorf("%s: %w", worker, err)
					}

					if err := tracker.waitUntilFull(fillCtx); err != nil {
						return fmt.Errorf("%s: %w", worker, err)
					}

					time.Sleep(jobTime)
					tracker.stop(route)

					if err := complete(ctx, pool, job); err != nil {
						return fmt.Errorf("%s: %w", worker, err)
					}
				}

				return nil
			})
		}
	}

	require.NoError(t, group.Wait())

	peaks, globalPeak := tracker.peaks()
	require.Equal(t, limits.Global, globalPeak, "the global limit is reached and never passed")
	require.LessOrEqual(t, peaks["a"], limits.Routes["a"])
	require.LessOrEqual(t, peaks["b"], limits.Routes["b"])
	require.Positive(t, peaks["c"])

	storedPeaks, storedGlobalPeak := tracker.storedPeaks()
	require.LessOrEqual(t, storedGlobalPeak, limits.Global, "the database never holds more running jobs than the global limit")
	require.LessOrEqual(t, storedPeaks["a"], limits.Routes["a"])
	require.LessOrEqual(t, storedPeaks["b"], limits.Routes["b"])
}

// concurrencyTracker counts jobs running at once, per route and in all, and
// keeps the highest counts seen, both as the claimers count them and as the
// database stores them. Claimers on several goroutines share it.
type concurrencyTracker struct {
	mu              sync.Mutex
	running         map[string]int
	peak            map[string]int
	total           int
	totalPeak       int
	storedPeak      map[string]int
	storedTotalPeak int
	done, want      int
	// full is closed once fullAt jobs have run at once.
	full   chan struct{}
	fullAt int
}

// newConcurrencyTracker returns a tracker that is done after want jobs and
// full once fullAt of them have run at once.
func newConcurrencyTracker(want, fullAt int) *concurrencyTracker {
	return &concurrencyTracker{
		running:    map[string]int{},
		peak:       map[string]int{},
		storedPeak: map[string]int{},
		want:       want,
		full:       make(chan struct{}),
		fullAt:     fullAt,
	}
}

func (c *concurrencyTracker) start(route string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.running[route]++
	c.total++
	c.peak[route] = max(c.peak[route], c.running[route])

	if c.totalPeak < c.fullAt && c.total >= c.fullAt {
		close(c.full)
	}

	c.totalPeak = max(c.totalPeak, c.total)
}

// observeStored reads the running investigation jobs from the database and
// keeps the highest counts seen. It can see an overrun that the claimers'
// own counts miss, because the database counts a job from its claim to its
// completion, which is longer than the claimer counts it.
func (c *concurrencyTracker) observeStored(ctx context.Context, pool *pgxpool.Pool) error {
	counts, total, err := runningLimitedJobs(ctx, pool)
	if err != nil {
		return fmt.Errorf("count running jobs: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for route, count := range counts {
		c.storedPeak[route] = max(c.storedPeak[route], count)
	}

	c.storedTotalPeak = max(c.storedTotalPeak, total)

	return nil
}

// waitUntilFull blocks until fullAt jobs have run at once, or ctx ends.
func (c *concurrencyTracker) waitUntilFull(ctx context.Context) error {
	// Once full is closed, a job must not fail because ctx has also ended,
	// so look at full on its own first.
	select {
	case <-c.full:
		return nil
	default:
	}

	select {
	case <-c.full:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for %d jobs running at once: %w", c.fullAt, context.Cause(ctx))
	}
}

func (c *concurrencyTracker) stop(route string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.running[route]--
	c.total--
	c.done++
}

func (c *concurrencyTracker) allDone() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.done >= c.want
}

func (c *concurrencyTracker) peaks() (map[string]int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.peak, c.totalPeak
}

func (c *concurrencyTracker) storedPeaks() (map[string]int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.storedPeak, c.storedTotalPeak
}

// runningLimitedJobs counts the running jobs of limitedKinds per route, and
// in all, in one snapshot.
func runningLimitedJobs(ctx context.Context, pool *pgxpool.Pool) (map[string]int, int, error) {
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(route, ''), count(*)::int FROM jobs
		WHERE status = 'running' AND kind = ANY ($1::text[])
		GROUP BY route`, limitedKinds)
	if err != nil {
		return nil, 0, fmt.Errorf("query running jobs: %w", err)
	}

	defer rows.Close()

	counts := map[string]int{}
	total := 0

	for rows.Next() {
		var (
			route string
			count int
		)

		if err := rows.Scan(&route, &count); err != nil {
			return nil, 0, fmt.Errorf("scan running jobs: %w", err)
		}

		counts[route] = count
		total += count
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read running jobs: %w", err)
	}

	return counts, total, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// waitsForAdvisoryLock reports whether a session on pool's database is
// waiting for an advisory lock.
func waitsForAdvisoryLock(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()

	var waiting bool

	err := pool.QueryRow(t.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks l JOIN pg_database d ON d.oid = l.database
			WHERE l.locktype = 'advisory' AND NOT l.granted AND d.datname = current_database()
		)`).Scan(&waiting)
	require.NoError(t, err)

	return waiting
}
