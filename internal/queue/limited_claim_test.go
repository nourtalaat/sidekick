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

// TestLimitsHoldUnderConcurrentClaims runs several claimers on each of two
// replicas against one database and checks that the per-route and global
// limits are never passed.
func TestLimitsHoldUnderConcurrentClaims(t *testing.T) {
	t.Parallel()

	const (
		claimersPerReplica = 4
		jobsPerRoute       = 12
		jobTime            = 20 * time.Millisecond
		idleWait           = 5 * time.Millisecond
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

	tracker := newConcurrencyTracker(len(routes) * jobsPerRoute)
	group, ctx := errgroup.WithContext(ctx)

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
						return err
					}

					if !claimed {
						time.Sleep(idleWait)

						continue
					}

					route := deref(job.Route)
					tracker.start(route)
					time.Sleep(jobTime)
					tracker.stop(route)

					if err := complete(ctx, pool, job); err != nil {
						return err
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
}

// concurrencyTracker counts jobs running at once, per route and in all, and
// keeps the highest counts seen. Claimers on several goroutines share it.
type concurrencyTracker struct {
	mu         sync.Mutex
	running    map[string]int
	peak       map[string]int
	total      int
	totalPeak  int
	done, want int
}

func newConcurrencyTracker(want int) *concurrencyTracker {
	return &concurrencyTracker{running: map[string]int{}, peak: map[string]int{}, want: want}
}

func (c *concurrencyTracker) start(route string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.running[route]++
	c.total++
	c.peak[route] = max(c.peak[route], c.running[route])
	c.totalPeak = max(c.totalPeak, c.total)
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

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
