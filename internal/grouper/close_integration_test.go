//go:build integration

package grouper

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// seededWindowEnd is the end of the window of the seeded group.
var seededWindowEnd = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

func TestCloseGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare string
		// now is when the job runs.
		now        time.Time
		wantStatus string
		wantReason *string
		// wantNext is the run_at of the job enqueued for the new end of the
		// window, if any.
		wantNext *time.Time
	}{
		{
			name:       "the window has ended",
			now:        seededWindowEnd.Add(time.Second),
			wantStatus: "closed",
			wantReason: new(ClosedWindowEnded),
		},
		{
			name:       "the window ends exactly now",
			now:        seededWindowEnd,
			wantStatus: "closed",
			wantReason: new(ClosedWindowEnded),
		},
		{
			name:       "an event moved the window on",
			prepare:    "UPDATE event_groups SET window_until = '2026-10-26 09:45:00+00'",
			now:        seededWindowEnd,
			wantStatus: "open",
			wantNext:   new(time.Date(2026, 10, 26, 9, 45, 0, 0, time.UTC)),
		},
		{
			name:       "a group without a window",
			prepare:    "UPDATE event_groups SET window_until = NULL",
			now:        seededWindowEnd,
			wantStatus: "closed",
			wantReason: new(ClosedWindowEnded),
		},
		{
			name:       "a group that is closed already",
			prepare:    "UPDATE event_groups SET status = 'closed', closed_reason = 'resolved'",
			now:        seededWindowEnd.Add(time.Hour),
			wantStatus: "closed",
			wantReason: new(ClosedResolved),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			if tt.prepare != "" {
				_, err := h.pool.Exec(t.Context(), tt.prepare)
				require.NoError(t, err)
			}

			require.NoError(t, enqueueClose(t.Context(), h.pool, diskGroup, seededWindowEnd, windowKey(diskGroup)))

			job := h.claimClose(t)
			closer := NewCloser(CloserOptions{Pool: h.pool, Now: func() time.Time { return tt.now }})
			require.NoError(t, closer.Handle(t.Context(), job))

			grp := h.groupByID(t, diskGroup)
			require.Equal(t, tt.wantStatus, grp.Status)
			require.Equal(t, tt.wantReason, grp.ClosedReason)

			pending := h.pendingCloses(t)
			if tt.wantNext == nil {
				require.Empty(t, pending)

				return
			}

			require.Len(t, pending, 1)
			require.True(t, tt.wantNext.Equal(pending[0].RunAt), "the job runs again when the new window ends")
			require.Equal(t, job.UniqueKey, pending[0].UniqueKey)
		})
	}
}

func TestCloseGroupGivesUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{name: "a payload that is not JSON", payload: `"group"`},
		{name: "a payload without a group ID", payload: `{}`},
		{name: "a group that does not exist", payload: `{"group_id": "0192d3a4-0000-7000-8000-0000000000ff"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			_, err := queue.Enqueue(t.Context(), h.pool, queue.NewJob{Kind: queue.KindCloseGroup, Payload: []byte(tt.payload)})
			require.NoError(t, err)

			err = NewCloser(CloserOptions{Pool: h.pool}).Handle(t.Context(), h.claimClose(t))
			require.ErrorIs(t, err, queue.ErrGiveUp)
		})
	}
}

func TestGroupPlansTheClose(t *testing.T) {
	t.Parallel()

	h := setup(t)

	res, err := h.group(t, cpuRouted, Route{Name: "default", Window: window})
	require.NoError(t, err)
	require.True(t, res.Opened)

	_, err = h.group(t, diskRouted, Route{Name: "default", Window: window})
	require.NoError(t, err)

	pending := h.pendingCloses(t)
	require.Len(t, pending, 2, "one job per open group")

	received := time.Date(2026, 10, 26, 9, 10, 0, 0, time.UTC)
	for _, job := range pending {
		require.True(t, received.Add(window).Equal(job.RunAt), "the job runs when the window ends")
	}
}

// claimClose claims the ready close_group job, as a worker does.
func (h *harness) claimClose(t *testing.T) db.Job {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() WHERE kind = 'close_group' AND status = 'pending'")
	require.NoError(t, err)

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindCloseGroup}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1, "one close_group job is ready")

	return jobs[0]
}

// pendingCloses returns the pending close_group jobs, oldest first.
func (h *harness) pendingCloses(t *testing.T) []db.Job {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM jobs WHERE kind = 'close_group' AND status = 'pending' ORDER BY id")
	require.NoError(t, err)

	jobs, err := pgx.CollectRows(rows, pgx.RowToStructByName[db.Job])
	require.NoError(t, err)

	return jobs
}
