package queue

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	// limitedClaimLockKey is the transaction-level advisory lock that every
	// claim of an investigation kind takes, so that the running counts it
	// checks are exact across replicas. It is "sk-invcl" as bytes.
	limitedClaimLockKey int64 = 0x736b2d696e76636c

	// maxLimit is the largest concurrency limit that Limits accepts.
	maxLimit = 1 << 20
)

// limitedKinds are the kinds whose claims respect Limits. Their running jobs
// count together toward the limits.
var limitedKinds = []string{KindInvestigate, KindFollowUp}

// Limits caps how many investigate and follow_up jobs run at once, counted
// together across every replica.
type Limits struct {
	// Global is the most of these jobs that may run at once. It must be at
	// least 1 when a Runner handles an investigation kind.
	Global int
	// Routes caps the jobs of each named route. A route without an entry,
	// and a job without a route, is held only by Global.
	Routes map[string]int
}

// limitParams are Limits in the form ClaimLimitedJob takes them.
type limitParams struct {
	global      int32
	routes      []string
	routeLimits []int32
}

// params checks the limits and turns them into query parameters, with the
// routes in a fixed order.
func (l Limits) params() (limitParams, error) {
	if l.Global < 1 || l.Global > maxLimit {
		return limitParams{}, fmt.Errorf("global limit is %d, want 1 to %d", l.Global, maxLimit)
	}

	// An empty slice, not nil, so the query gets an empty array and not
	// NULL.
	routes := append([]string{}, slices.Sorted(maps.Keys(l.Routes))...)
	routeLimits := make([]int32, len(routes))

	for i, route := range routes {
		limit := l.Routes[route]
		if limit < 0 || limit > maxLimit {
			return limitParams{}, fmt.Errorf("limit of route %q is %d, want 0 to %d", route, limit, maxLimit)
		}

		routeLimits[i] = int32(limit) //nolint:gosec // Checked against maxLimit above.
	}

	return limitParams{
		global:      int32(l.Global), //nolint:gosec // Checked against maxLimit above.
		routes:      routes,
		routeLimits: routeLimits,
	}, nil
}

// claimLimited leases the oldest ready job of kind whose route is under its
// limit, when the global limit allows one more. It returns false when no
// job may start now. When the commit fails, it returns the job it leased
// and true with the error: the commit may have gone through, so the caller
// hands the job back with handBackLost.
func claimLimited(ctx context.Context, pool *pgxpool.Pool, worker, kind string, lease time.Duration, limits limitParams) (job db.Job, claimed bool, err error) {
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		if err := queries.LockLimitedClaims(ctx, limitedClaimLockKey); err != nil {
			return fmt.Errorf("take the claim lock: %w", err)
		}

		job, err = queries.ClaimLimitedJob(ctx, db.ClaimLimitedJobParams{
			Worker:       worker,
			LeaseSeconds: lease.Seconds(),
			LimitedKinds: limitedKinds,
			Routes:       limits.routes,
			RouteLimits:  limits.routeLimits,
			Kind:         kind,
			GlobalLimit:  limits.global,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}

		claimed = true

		return nil
	})
	if err != nil {
		return job, claimed, fmt.Errorf("claim %s job: %w", kind, err)
	}

	return job, claimed, nil
}
