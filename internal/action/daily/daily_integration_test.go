//go:build integration

package daily

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

const (
	issues   = "ops-issues"
	maxIssue = 2
)

func TestCheck(t *testing.T) {
	t.Parallel()

	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)

	// 00:30 on 3 November in Berlin, still 2 November in UTC.
	now := time.Date(2026, 11, 2, 23, 30, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	later := now.Add(time.Hour)

	tests := []struct {
		name      string
		loc       *time.Location
		existing  []proposal
		wantLimit bool
	}{
		{name: "nothing used", loc: berlin},
		{
			name: "one success today",
			loc:  berlin,
			existing: []proposal{
				{action: issues, status: "succeeded", executedAt: at(now.Add(-10 * time.Minute))},
			},
		},
		{
			name: "two successes today",
			loc:  berlin,
			existing: []proposal{
				{action: issues, status: "succeeded", executedAt: at(now.Add(-10 * time.Minute))},
				{action: issues, status: "succeeded", executedAt: at(now.Add(-20 * time.Minute))},
			},
			wantLimit: true,
		},
		{
			name: "yesterday in the zone does not count",
			loc:  berlin,
			existing: []proposal{
				{action: issues, status: "succeeded", executedAt: at(now.Add(-90 * time.Minute))},
				{action: issues, status: "succeeded", executedAt: at(now.Add(-2 * time.Hour))},
			},
		},
		{
			name: "the same successes count in UTC",
			loc:  time.UTC,
			existing: []proposal{
				{action: issues, status: "succeeded", executedAt: at(now.Add(-90 * time.Minute))},
				{action: issues, status: "succeeded", executedAt: at(now.Add(-2 * time.Hour))},
			},
			wantLimit: true,
		},
		{
			name: "other actions and other outcomes do not count",
			loc:  berlin,
			existing: []proposal{
				{action: "other-issues", status: "succeeded", executedAt: at(now.Add(-10 * time.Minute))},
				{action: issues, status: "failed", executedAt: at(now.Add(-10 * time.Minute))},
				{action: issues, status: "rejected", decidedAt: at(earlier)},
				{action: issues, status: "proposed"},
			},
		},
		{
			name: "approved and running before this one count",
			loc:  berlin,
			existing: []proposal{
				{action: issues, status: "approved", decidedAt: at(earlier)},
				{action: issues, status: "executing", decidedAt: at(earlier.Add(-time.Minute))},
			},
			wantLimit: true,
		},
		{
			name: "approved after this one does not count",
			loc:  berlin,
			existing: []proposal{
				{action: issues, status: "approved", decidedAt: at(later)},
				{action: issues, status: "executing", decidedAt: at(later.Add(time.Minute))},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			for _, p := range tt.existing {
				p.insert(t, pool)
			}

			self := proposal{action: issues, status: "executing", decidedAt: at(now)}.insert(t, pool)

			err := Counter{DB: pool, Location: tt.loc, Now: func() time.Time { return now }}.Check(t.Context(), issues, self.String(), maxIssue)
			if tt.wantLimit {
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestCheckRefusesAProposalIDThatIsNotAUUID(t *testing.T) {
	t.Parallel()

	err := Counter{DB: setup(t)}.Check(t.Context(), issues, "not-a-uuid", maxIssue)
	require.ErrorIs(t, err, errBadID)
	require.ErrorIs(t, err, action.ErrPermanent)
}

// TestCheckInParallel runs six approved proposals at the same time, each
// creating its ticket when the check passes, and requires that exactly two
// get through.
func TestCheckInParallel(t *testing.T) {
	t.Parallel()

	const proposals = 6

	pool := setup(t)
	now := time.Now()
	counter := Counter{DB: pool}

	ids := make([]uuid.UUID, proposals)
	for i := range ids {
		ids[i] = proposal{action: issues, status: "executing", decidedAt: at(now.Add(time.Duration(i) * time.Millisecond))}.insert(t, pool)
	}

	var wg sync.WaitGroup

	// Each goroutine writes only its own slot, so nothing needs a lock.
	checks := make([]error, proposals)
	writes := make([]error, proposals)

	for i, id := range ids {
		wg.Go(func() {
			checks[i] = counter.Check(t.Context(), issues, id.String(), maxIssue)
			if checks[i] != nil {
				return
			}

			_, writes[i] = pool.Exec(t.Context(), `UPDATE action_proposals SET status = 'succeeded', executed_at = now() WHERE id = $1`, id)
		})
	}

	wg.Wait()

	passed := 0

	for i := range ids {
		require.NoError(t, writes[i])

		if checks[i] == nil {
			require.Less(t, i, maxIssue, "only the first approved may pass")

			passed++

			continue
		}

		require.ErrorIs(t, checks[i], action.ErrLimit)
	}

	require.Equal(t, maxIssue, passed)

	var succeeded int64

	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM action_proposals WHERE status = 'succeeded'`).Scan(&succeeded))
	require.EqualValues(t, maxIssue, succeeded)
}
