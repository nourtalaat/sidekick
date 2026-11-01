//go:build integration

package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

var (
	errSimulatedCrash = errors.New("the worker was killed")
	errBackendDown    = errors.New("the notes service is down")
)

// approved proposes the note, approves it as Ana, and returns its ID.
func (h *harness) approved(t *testing.T) uuid.UUID {
	t.Helper()

	id := h.propose(t, "call-1", noteInput)
	_, err := h.svc.Approve(t.Context(), id, Decider{UserID: anaUser})
	require.NoError(t, err)

	return id
}

// runJob claims the next ready execute_action job and runs it with the
// service, then completes, fails or buries it as the queue's runner does.
// It reports whether there was a job, and the handler's error.
func (h *harness) runJob(t *testing.T) (bool, error) {
	t.Helper()

	queries := db.New(h.pool)

	jobs, err := queries.ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindExecuteAction}, Batch: 1,
	})
	require.NoError(t, err)

	if len(jobs) == 0 {
		return false, nil
	}

	job := jobs[0]

	herr := h.svc.Handle(t.Context(), job)
	if herr == nil {
		_, err = queries.CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
		require.NoError(t, err)

		return true, nil
	}

	failed, err := queries.FailJob(t.Context(), db.FailJobParams{
		LastError: herr.Error(), GiveUp: errors.Is(herr, queue.ErrGiveUp), ID: job.ID, ClaimToken: *job.ClaimToken,
	})
	require.NoError(t, err)

	if failed.Status == queue.StatusDead {
		require.NoError(t, h.svc.OnDead(t.Context(), failed))
	}

	return true, herr
}

func TestExecute(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.approved(t)

	ran, err := h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err)

	row := h.row(t, id)
	require.Equal(t, "succeeded", row.Status)
	require.NotNil(t, row.ExecutedAt)
	require.Nil(t, row.Error)
	require.JSONEq(t, `{"summary": "Wrote a note.", "links": [{"title": "Note", "url": "https://notes.example.com/n/`+id.String()+`"}]}`, string(row.Result))

	require.Equal(t, []string{AuditProposed, AuditApproved, AuditExecuting, AuditSucceeded}, h.auditActions(t, id))

	calls := h.note.callList()
	require.Len(t, calls, 1)
	require.Equal(t, action.Invocation{
		ProposalID:      id.String(),
		InvestigationID: checkoutInvestigation.String(),
		Params:          row.Params,
		IdempotencyKey:  "sidekick/" + id.String(),
		ApprovedBy:      anaUser.String(),
	}, calls[0])

	ran, _ = h.runJob(t)
	require.False(t, ran, "the job is done")

	proposals, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
	require.NoError(t, err)
	require.Equal(t, action.StatusSucceeded, proposals[0].Status)
	require.Equal(t, "Wrote a note.", proposals[0].Result.Summary)
}

// TestExecuteTwiceAfterACrashHasTheEffectOfOne simulates a worker that
// dies after the action made its change and before the result was saved.
// The job is tried again, the action gets the same idempotency key, and
// the change was made once.
func TestExecuteTwiceAfterACrashHasTheEffectOfOne(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.approved(t)

	h.note.afterEffect = func() error {
		h.note.mu.Lock()
		h.note.afterEffect = nil
		h.note.mu.Unlock()

		return errSimulatedCrash
	}

	ran, err := h.runJob(t)
	require.True(t, ran)
	require.ErrorIs(t, err, errSimulatedCrash)

	row := h.row(t, id)
	require.Equal(t, "executing", row.Status, "the result was not saved")
	require.Equal(t, 1, h.note.effectCount())

	ran, err = h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err)

	require.Equal(t, "succeeded", h.row(t, id).Status)
	require.Equal(t, 1, h.note.effectCount(), "the second run did not change anything again")

	calls := h.note.callList()
	require.Len(t, calls, 2)
	require.Equal(t, calls[0].IdempotencyKey, calls[1].IdempotencyKey)
	require.Equal(t, []string{AuditProposed, AuditApproved, AuditExecuting, AuditSucceeded}, h.auditActions(t, id), "executing is audited once")
}

func TestExecuteFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// change runs after the approval.
		change func(h *harness) *Service
		// errs are the errors of Execute, in order.
		errs       []error
		wantStatus string
		wantRetry  bool
		wantError  string
		wantCalls  int
	}{
		{
			name: "an error that a repeat cannot fix", errs: []error{action.ErrPermanent},
			wantStatus: "failed", wantError: "permanent failure", wantCalls: 1,
		},
		{
			name: "a conflict", errs: []error{action.ErrConflict},
			wantStatus: "failed", wantError: "changed since the snapshot", wantCalls: 1,
		},
		{
			name: "an error that may pass", errs: []error{errBackendDown},
			wantStatus: "executing", wantRetry: true, wantError: "the notes service is down", wantCalls: 1,
		},
		{
			name:       "limits tightened since the approval",
			change:     func(h *harness) *Service { h.note.validateErr = action.ErrLimit; return h.svc },
			wantStatus: "failed", wantError: "The parameters are not valid any more", wantCalls: 0,
		},
		{
			name: "the action left the route",
			change: func(h *harness) *Service {
				h.policy.set("payments", action.RoutePolicy{Mode: action.Approved, Actions: []string{"other"}})
				return h.svc
			},
			wantStatus: "failed", wantError: `route "payments" does not list action "monitor-note"`, wantCalls: 0,
		},
		{
			name: "the route became read-only",
			change: func(h *harness) *Service {
				h.policy.set("payments", action.RoutePolicy{Mode: action.ReadOnly})
				return h.svc
			},
			wantStatus: "failed", wantError: `route "payments" is read-only`, wantCalls: 0,
		},
		{
			name: "the action was removed from the config",
			change: func(h *harness) *Service {
				s, err := New(Options{Pool: h.pool, Policy: h.policy, Now: h.clock.Now, Actions: map[string]action.Action{}})
				require.NoError(t, err)

				return s
			},
			wantStatus: "failed", wantError: `action "monitor-note" is not defined in the config any more`, wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			id := h.approved(t)

			h.note.executeErrs = tt.errs

			if tt.change != nil {
				h.svc = tt.change(h)
			}

			ran, err := h.runJob(t)
			require.True(t, ran)

			if tt.wantRetry {
				require.ErrorIs(t, err, errBackendDown)
			} else {
				require.NoError(t, err, "the job is done")
			}

			row := h.row(t, id)
			require.Equal(t, tt.wantStatus, row.Status)
			require.NotNil(t, row.Error)
			require.Contains(t, *row.Error, tt.wantError)
			require.Len(t, h.note.callList(), tt.wantCalls)

			if tt.wantStatus == "failed" {
				require.NotNil(t, row.ExecutedAt)
				require.Equal(t, AuditFailed, last(h.auditActions(t, id)))
			}
		})
	}
}

func TestExecuteFailsForGoodWhenTheJobIsDead(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.approved(t)

	h.note.executeErrs = []error{errBackendDown, errBackendDown, errBackendDown}

	for range executeMaxAttempts {
		ran, err := h.runJob(t)
		require.True(t, ran)
		require.ErrorIs(t, err, errBackendDown)
	}

	ran, _ := h.runJob(t)
	require.False(t, ran, "the job is dead")

	row := h.row(t, id)
	require.Equal(t, "failed", row.Status)
	require.Contains(t, *row.Error, "Sidekick tried 3 times and stopped. The last error:")
	require.Contains(t, *row.Error, "the notes service is down")
	require.Len(t, h.note.callList(), executeMaxAttempts)
	require.Equal(t, AuditFailed, last(h.auditActions(t, id)))

	// An admin's retry of the dead job leaves the proposal as it is.
	_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET status = 'pending', run_at = now() WHERE kind = $1", queue.KindExecuteAction)
	require.NoError(t, err)

	ran, err = h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err)
	require.Len(t, h.note.callList(), executeMaxAttempts, "nothing ran again")
	require.Equal(t, "failed", h.row(t, id).Status)
}

func TestExecuteLeavesOtherProposalsAlone(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.propose(t, "call-1", noteInput)

	// A job for a proposal that nobody approved does nothing.
	require.NoError(t, enqueueTestJob(t, h, id))

	ran, err := h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err)
	require.Equal(t, "proposed", h.row(t, id).Status)
	require.Empty(t, h.note.callList())

	// A job for a proposal that does not exist is given up.
	require.NoError(t, enqueueTestJob(t, h, uuid.New()))

	ran, err = h.runJob(t)
	require.True(t, ran)
	require.ErrorIs(t, err, queue.ErrGiveUp)

	// A job without a payload is given up too.
	err = h.svc.Handle(t.Context(), db.Job{Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, queue.ErrGiveUp)
}

// TestExecuteRunsOnceWhenTwoWorkersGetTheJob lets two workers run the same
// proposal at the same time, as happens when a lease expires while a worker
// is slow. The action is called with the same key each time, and the
// proposal ends once.
func TestExecuteRunsOnceWhenTwoWorkersGetTheJob(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.approved(t)

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "worker-a", LeaseSeconds: 60, Kinds: []string{queue.KindExecuteAction}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for i := range errs {
		wg.Go(func() { errs[i] = h.svc.Handle(context.Background(), jobs[0]) })
	}

	wg.Wait()

	require.NoError(t, errors.Join(errs...))

	require.Equal(t, "succeeded", h.row(t, id).Status)
	require.Equal(t, 1, h.note.effectCount())
	require.Equal(t, []string{AuditProposed, AuditApproved, AuditExecuting, AuditSucceeded}, h.auditActions(t, id))
}

// enqueueTestJob queues an execute_action job for id as approving does,
// without approving.
func enqueueTestJob(t *testing.T, h *harness, id uuid.UUID) error {
	t.Helper()

	return enqueueExecute(t.Context(), h.pool, id)
}

// last returns the last of values.
func last(values []string) string {
	return values[len(values)-1]
}
