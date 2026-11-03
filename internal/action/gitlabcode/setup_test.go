//go:build integration

package gitlabcode

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The rows of fixture/investigations.sql.
var (
	checkoutInvestigation = uuid.MustParse(testInvestID)
	anaUser               = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000d1")
)

// routeName is the route of the investigation in the fixture.
const routeName = "payments"

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// stack is the action behind the real proposals service and a database of
// its own, with the fake GitLab of its bed.
type stack struct {
	*bed

	pool *pgxpool.Pool
	svc  *proposals.Service
}

// newStack returns a proposals service on a seeded database whose only
// action is code-mr, which the route payments may propose.
func newStack(t *testing.T) *stack {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")
	require.NoError(t, publish.Open(t.Context(), pool, checkoutInvestigation, []string{"slack"}))

	b := newBedWithDB(t, nil, pool)

	svc, err := proposals.New(proposals.Options{
		Pool:    pool,
		Actions: map[string]action.Action{"code-mr": b.code},
		Policy:  action.StaticPolicy{routeName: {Mode: action.Approved, Actions: []string{"code-mr"}}},
	})
	require.NoError(t, err)

	return &stack{bed: b, pool: pool, svc: svc}
}

// runJob claims the next execute_action job, runs it with the service and
// completes it, or fails it as the queue's runner does. It returns the
// handler's error.
func (s *stack) runJob(t *testing.T) error {
	t.Helper()

	queries := db.New(s.pool)

	jobs, err := queries.ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindExecuteAction}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	job := jobs[0]

	herr := s.svc.Handle(t.Context(), job)
	if herr == nil {
		_, err = queries.CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
		require.NoError(t, err)

		return nil
	}

	_, err = queries.FailJob(t.Context(), db.FailJobParams{
		LastError: herr.Error(), GiveUp: errors.Is(herr, queue.ErrGiveUp), ID: job.ID, ClaimToken: *job.ClaimToken,
	})
	require.NoError(t, err)

	return fmt.Errorf("run the execute_action job: %w", herr)
}

// proposals counts the stored proposals.
func (s *stack) proposalCount(t *testing.T) int {
	t.Helper()

	var n int

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM action_proposals").Scan(&n))

	return n
}
