//go:build integration

package linear

import (
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

const proposeIssue = `{"action": "ops-issue", "params": {"team": "OPS", "title": "Issue %d", "labels": ["incident"]}, "reason": "p99 doubled."}`

// approveIssue proposes the issue number n and approves it as Ana.
func (h *harness) approveIssue(t *testing.T, n int) uuid.UUID {
	t.Helper()

	res, err := h.svc.Propose(t.Context(), proposals.ProposeRequest{
		InvestigationID: checkoutInvestigation,
		ToolCallID:      fmt.Sprintf("call-%d", n),
		Route:           "payments",
		Mode:            action.Approved,
		Input:           []byte(fmt.Sprintf(proposeIssue, n)),
	})
	require.NoError(t, err)
	require.Empty(t, res.Refusal)

	_, err = h.svc.Approve(t.Context(), res.ID, proposals.Decider{UserID: anaUser})
	require.NoError(t, err)

	return res.ID
}

// claim claims up to n execute_action jobs.
func (h *harness) claim(t *testing.T, n int32) []db.Job {
	t.Helper()

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindExecuteAction}, Batch: n,
	})
	require.NoError(t, err)

	return jobs
}

// status returns the status and the error of a proposal.
func (h *harness) status(t *testing.T, id uuid.UUID) (string, string) {
	t.Helper()

	row, err := db.New(h.pool).GetActionProposal(t.Context(), id)
	require.NoError(t, err)

	var msg string
	if row.Error != nil {
		msg = *row.Error
	}

	return row.Status, msg
}

// TestDailyLimitWithProposalsRunningAtOnce approves three issues for an
// action that may create two a day, and runs the three jobs at the same
// time. Two issues get made and the third proposal fails at the limit.
func TestDailyLimitWithProposalsRunningAtOnce(t *testing.T) {
	t.Parallel()

	const (
		proposed  = 3
		maxPerDay = 2
	)

	h := setup(t, maxPerDay)

	ids := make([]uuid.UUID, proposed)
	for i := range ids {
		ids[i] = h.approveIssue(t, i)
	}

	jobs := h.claim(t, proposed)
	require.Len(t, jobs, proposed)

	var wg sync.WaitGroup

	errs := make([]error, len(jobs))

	for i, job := range jobs {
		wg.Go(func() { errs[i] = h.svc.Handle(t.Context(), job) })
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	succeeded, failed := 0, 0

	for _, id := range ids {
		status, msg := h.status(t, id)

		switch status {
		case "succeeded":
			succeeded++
		case "failed":
			failed++

			require.Contains(t, msg, "outside configured limits")
		default:
			require.Failf(t, "unexpected status", "%s", status)
		}
	}

	require.Equal(t, maxPerDay, succeeded)
	require.Equal(t, proposed-maxPerDay, failed)
	require.Equal(t, maxPerDay, h.linear.creates)
}

// TestRepeatedExecutionCreatesOneIssue runs the job of one approved
// proposal twice, as a crash after the issue was made would, and requires
// one issue.
func TestRepeatedExecutionCreatesOneIssue(t *testing.T) {
	t.Parallel()

	h := setup(t, 1)
	id := h.approveIssue(t, 0)

	jobs := h.claim(t, 1)
	require.Len(t, jobs, 1)

	row, err := db.New(h.pool).GetActionProposal(t.Context(), id)
	require.NoError(t, err)

	issue := newIssueFor(t, h)
	inv := action.Invocation{
		ProposalID: id.String(), InvestigationID: checkoutInvestigation.String(),
		Params: row.Params, IdempotencyKey: row.IdempotencyKey, ApprovedBy: anaUser.String(),
	}

	first, err := issue.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := issue.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, h.linear.creates)

	require.NoError(t, h.svc.Handle(t.Context(), jobs[0]))

	status, msg := h.status(t, id)
	require.Equal(t, "succeeded", status, msg)
	require.Equal(t, 1, h.linear.creates, "the job finds the issue the direct runs made")
}

// newIssueFor builds a second linear.issue action on the harness's database
// and fake Linear, for runs that do not go through the service.
func newIssueFor(t *testing.T, h *harness) action.Action {
	t.Helper()

	deps := testDeps(h.api, testAPIKey)
	deps.DB = h.pool

	a, err := NewIssue(t.Context(), "ops-issue", IssueConfig{
		Connection: connection(h.api),
		Limits:     IssueLimits{Teams: []string{"OPS"}, Labels: []string{"incident"}, MaxPerDay: 1},
	}, deps)
	require.NoError(t, err)

	return a
}
