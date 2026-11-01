//go:build integration

package proposals

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestExpire(t *testing.T) {
	t.Parallel()

	h := setup(t)

	waiting := h.propose(t, "call-1", noteInput)
	approved := h.propose(t, "call-2", noteInput)
	rejected := h.propose(t, "call-3", noteInput)

	_, err := h.svc.Approve(t.Context(), approved, Decider{UserID: anaUser})
	require.NoError(t, err)

	_, err = h.svc.Reject(t.Context(), rejected, Decider{UserID: anaUser}, "")
	require.NoError(t, err)

	n, err := h.svc.Expire(t.Context())
	require.NoError(t, err)
	require.Zero(t, n, "nothing has expired yet")

	h.clock.advance(action.DefaultProposalTTL - time.Minute)

	n, err = h.svc.Expire(t.Context())
	require.NoError(t, err)
	require.Zero(t, n, "a minute is left")

	h.clock.advance(time.Minute)

	seq := h.publishSeq(t, checkoutInvestigation)

	n, err = h.svc.Expire(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the proposal nobody decided expires")

	require.Equal(t, "expired", h.row(t, waiting).Status)
	require.Equal(t, "approved", h.row(t, approved).Status)
	require.Equal(t, "rejected", h.row(t, rejected).Status)
	require.Equal(t, []string{AuditProposed, AuditExpired}, h.auditActions(t, waiting))
	require.Greater(t, h.publishSeq(t, checkoutInvestigation), seq, "the outputs show the expiry")

	var actorKind string
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT actor_kind FROM audit_log WHERE action = $1 AND target_id = $2", AuditExpired, waiting.String()).Scan(&actorKind))
	require.Equal(t, "system", actorKind)

	_, err = h.svc.Approve(t.Context(), waiting, Decider{UserID: anaUser})
	require.ErrorIs(t, err, ErrExpired, "an expired proposal cannot be approved")

	n, err = h.svc.Expire(t.Context())
	require.NoError(t, err)
	require.Zero(t, n, "and it expires once")
}

func TestExpireTakesBatches(t *testing.T) {
	t.Parallel()

	const proposals = 2*expireBatch + 30

	h := setup(t)

	_, err := h.pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at)
		SELECT gen_random_uuid(), $1, 'monitor-note', '{}', 'approved', 'proposed', 'sidekick/bulk-' || i, now() - interval '2 days', now() - interval '1 day'
		FROM generate_series(1, $2) AS i`, checkoutInvestigation, proposals)
	require.NoError(t, err)

	n, err := h.svc.Expire(t.Context())
	require.NoError(t, err)
	require.Equal(t, proposals, n)
	require.EqualValues(t, proposals, countRows(t, h, "SELECT count(*) FROM action_proposals WHERE status = 'expired'"))
	require.EqualValues(t, proposals, countRows(t, h, "SELECT count(*) FROM audit_log WHERE action = 'action.expired'"))
}

func TestExpireJob(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.propose(t, "call-1", noteInput)
	h.clock.advance(action.DefaultProposalTTL)

	scheduler := NewScheduler(SchedulerOptions{Pool: h.pool})

	queued, err := scheduler.Schedule(t.Context())
	require.NoError(t, err)
	require.True(t, queued)

	queued, err = scheduler.Schedule(t.Context())
	require.NoError(t, err)
	require.False(t, queued, "one job waits for a worker at a time")

	queries := db.New(h.pool)

	jobs, err := queries.ClaimJobs(t.Context(), db.ClaimJobsParams{Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindExpireProposals}, Batch: 5})
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	require.NoError(t, h.svc.HandleExpire(t.Context(), jobs[0]))
	require.Equal(t, "expired", h.row(t, id).Status)

	_, err = queries.CompleteJob(t.Context(), db.CompleteJobParams{ID: jobs[0].ID, ClaimToken: *jobs[0].ClaimToken})
	require.NoError(t, err)

	queued, err = scheduler.Schedule(t.Context())
	require.NoError(t, err)
	require.True(t, queued, "the next tick queues the next job")
}
