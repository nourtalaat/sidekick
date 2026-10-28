//go:build integration

package grouper

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// recovery is the recovery event the tests add.
var recovery = uuid.MustParse("0192d3a4-0000-7000-8000-000000000010")

// insertRecovery stores a received recovery event of the alert with
// fingerprint, received at 09:20.
const insertRecovery = `INSERT INTO events (id, intake, key, kind, trusted, alert_name, labels, fingerprint, status, received_at)
VALUES ($1, 'grafana', 'disk-1-resolved', 'resolved', false, 'DiskFull', '{}', $2, 'received', '2026-10-26 09:20:00+00')`

func TestRecover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prepare     string
		fingerprint string
		wantLinked  bool
		wantStatus  string
		// wantResolve is whether a close_group job to resolve the group is
		// enqueued.
		wantResolve bool
	}{
		{
			name:        "the recovery of the group's only alert",
			fingerprint: "fp-disk",
			wantLinked:  true,
			wantStatus:  "grouped",
			wantResolve: true,
		},
		{
			name:        "another alert in the group still fires",
			prepare:     "UPDATE events SET status = 'grouped', group_id = '" + diskGroup.String() + "', fingerprint = 'fp-disk-2' WHERE key = 'disk-2'",
			fingerprint: "fp-disk",
			wantLinked:  true,
			wantStatus:  "grouped",
		},
		{
			name:        "the alert's group is closed",
			prepare:     "UPDATE event_groups SET status = 'closed', closed_reason = 'window_ended'",
			fingerprint: "fp-disk",
			wantLinked:  true,
			wantStatus:  "grouped",
		},
		{
			// The alert's event names a group that the retention job
			// deleted with its investigation; events have no foreign key
			// to their group.
			name:        "the alert's group was deleted",
			prepare:     "DELETE FROM investigations; DELETE FROM event_groups",
			fingerprint: "fp-disk",
			wantStatus:  "skipped",
		},
		{
			name:        "no group has the alert",
			fingerprint: "fp-none",
			wantStatus:  "skipped",
		},
		{
			name:        "a recovery that is linked already",
			prepare:     "UPDATE events SET status = 'grouped', group_id = '" + diskGroup.String() + "' WHERE key = 'disk-1-resolved'",
			fingerprint: "fp-disk",
			wantStatus:  "grouped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			_, err := h.pool.Exec(t.Context(), insertRecovery, recovery, tt.fingerprint)
			require.NoError(t, err)

			if tt.prepare != "" {
				_, err := h.pool.Exec(t.Context(), tt.prepare)
				require.NoError(t, err)
			}

			var (
				res    Result
				linked bool
			)

			require.NoError(t, pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
				ev, err := db.New(tx).GetEvent(t.Context(), recovery)
				require.NoError(t, err)

				res, linked, err = h.grouper.Recover(t.Context(), tx, ev)

				return err
			}))

			require.Equal(t, tt.wantLinked, linked)
			require.Equal(t, 1, h.count(t, "SELECT count(*) FROM events WHERE id = $1 AND status = $2", recovery, tt.wantStatus))
			require.Equal(t, boolCount(tt.wantResolve), h.count(t, "SELECT count(*) FROM jobs WHERE unique_key = $1", resolvedKey(diskGroup)))

			if !tt.wantLinked {
				require.Equal(t, Result{}, res)
				require.Zero(t, h.count(t, "SELECT count(*) FROM investigation_inbox"))

				return
			}

			require.Equal(t, Result{GroupID: diskGroup, InvestigationID: diskInvestigation}, res)
			require.Equal(t, 1, h.count(t, "SELECT count(*) FROM investigation_inbox WHERE kind = 'resolved' AND investigation_id = $1", diskInvestigation))
			require.Equal(t, int32(1), h.groupByID(t, diskGroup).EventCount, "a recovery does not count as an event")
		})
	}
}

func TestCloseResolvesARecoveredGroup(t *testing.T) {
	t.Parallel()

	h := setup(t)

	_, err := h.pool.Exec(t.Context(), insertRecovery, recovery, "fp-disk")
	require.NoError(t, err)

	_, err = h.pool.Exec(t.Context(), "UPDATE events SET status = 'grouped', group_id = $1 WHERE id = $2", diskGroup, recovery)
	require.NoError(t, err)

	require.NoError(t, enqueueClose(t.Context(), h.pool, diskGroup, seededWindowEnd, resolvedKey(diskGroup)))

	closer := NewCloser(CloserOptions{Pool: h.pool, Now: func() time.Time { return seededWindowEnd.Add(-time.Minute) }})
	require.NoError(t, closer.Handle(t.Context(), h.claimClose(t)))

	grp := h.groupByID(t, diskGroup)
	require.Equal(t, "closed", grp.Status)
	require.Equal(t, new(ClosedResolved), grp.ClosedReason, "a recovered group closes before its window ends")
}

// boolCount is 1 for true and 0 for false.
func boolCount(b bool) int {
	if b {
		return 1
	}

	return 0
}
