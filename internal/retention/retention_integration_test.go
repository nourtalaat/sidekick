//go:build integration

package retention

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Queries that list what is left in each table, as text in a fixed order.
const (
	listInvestigations = "SELECT id::text FROM investigations ORDER BY id"
	listGroups         = "SELECT id::text FROM event_groups ORDER BY id"
	listSteps          = "SELECT right(investigation_id::text, 2) || '/' || seq FROM steps ORDER BY 1"
	listInbox          = "SELECT right(investigation_id::text, 2) FROM investigation_inbox ORDER BY 1"
	listOutputRefs     = "SELECT right(investigation_id::text, 2) FROM output_refs ORDER BY 1"
	listPublishStates  = "SELECT right(investigation_id::text, 2) FROM publish_states ORDER BY 1"
	listFeedback       = "SELECT right(investigation_id::text, 2) FROM feedback ORDER BY 1"
	listWatchers       = "SELECT right(id::text, 2) FROM watchers ORDER BY 1"
	listWatcherChanges = "SELECT right(investigation_id::text, 2) FROM watcher_changes ORDER BY 1"
	listWatcherChecks  = "SELECT right(watcher_id::text, 2) || '/' || seq FROM watcher_checks ORDER BY 1"
	listEvents         = "SELECT key FROM events ORDER BY key"
	listAuditLog       = "SELECT action FROM audit_log ORDER BY action"
	listJobs           = "SELECT unique_key FROM jobs ORDER BY unique_key"
)

// The investigations and groups in fixture/records.sql.
const (
	group3         = "0192d3a4-0000-7000-8000-0000000000b3"
	group4         = "0192d3a4-0000-7000-8000-0000000000b4"
	investigation2 = "0192d3a4-0000-7000-8000-0000000000c2"
	investigation3 = "0192d3a4-0000-7000-8000-0000000000c3"
	investigation4 = "0192d3a4-0000-7000-8000-0000000000c4"
)

// TestHandleDeletesOnlyOldRows runs the job over records inside and
// outside each period, with batches that take everything at once and
// batches smaller than what there is to delete.
func TestHandleDeletesOnlyOldRows(t *testing.T) {
	t.Parallel()

	want := []struct {
		query string
		rows  []string
	}{
		{listInvestigations, []string{investigation3, investigation4}},
		{listGroups, []string{group3, group4}},
		{listSteps, []string{"c4/1"}},
		{listInbox, []string{"c4"}},
		{listOutputRefs, []string{"c4"}},
		{listPublishStates, []string{"c4"}},
		{listFeedback, nil},
		{listWatchers, []string{"d4"}},
		{listWatcherChanges, []string{"c4"}},
		{listWatcherChecks, []string{"d4/3"}},
		{listEvents, []string{"ev-3"}},
		{listAuditLog, []string{"audit-3"}},
		{listJobs, []string{"job-dead-2d", "job-done-1h", "job-pending"}},
	}

	tests := []struct {
		name  string
		batch int32
	}{
		{name: "the default batch size", batch: 0},
		{name: "batches of one row", batch: 1},
		{name: "batches of two rows", batch: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.batch)
			require.NoError(t, h.handler.Handle(t.Context(), db.Job{ID: 1}))

			for _, w := range want {
				require.Equal(t, w.rows, h.strings(t, w.query), w.query)
			}

			for table, n := range map[string]float64{
				TableInvestigations: 2, TableWatchers: 2, TableEventGroups: 2, TableSteps: 2,
				TableWatcherChecks: 2, TableEvents: 3, TableAuditLog: 2, TableJobs: 3,
			} {
				require.InDelta(t, n, testutil.ToFloat64(h.metrics.deletedRows.WithLabelValues(table)), 0, table)
			}

			require.NoError(t, h.handler.Handle(t.Context(), db.Job{ID: 2}), "a second run finds nothing left to delete")
			require.Equal(t, []string{"job-dead-2d", "job-done-1h", "job-pending"}, h.strings(t, listJobs))
		})
	}
}

// TestBatchTakesTheOldestFirst runs one batch of one row on each table and
// checks that it took the oldest row older than the period.
func TestBatchTakesTheOldestFirst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		delete func(h *Handler) func(ctx context.Context, before time.Time, batch int32) (int64, error)
		period time.Duration
		query  string
		// want is what is left: every row but the oldest old one.
		want []string
	}{
		{
			// c3 is older than c1, but its group is still open.
			name:   "investigations",
			delete: func(h *Handler) func(context.Context, time.Time, int32) (int64, error) { return h.deleteInvestigations },
			period: testPeriods.Investigations,
			query:  listInvestigations,
			want:   []string{investigation2, investigation3, investigation4},
		},
		{
			name:   "steps",
			delete: func(h *Handler) func(context.Context, time.Time, int32) (int64, error) { return h.deleteSteps },
			period: testPeriods.Steps,
			query:  listSteps,
			want:   []string{"c1/0", "c1/1", "c2/0", "c4/0", "c4/1"},
		},
		{
			name:   "watcher checks",
			delete: func(h *Handler) func(context.Context, time.Time, int32) (int64, error) { return h.deleteWatcherChecks },
			period: testPeriods.WatcherChecks,
			query:  listWatcherChecks,
			want:   []string{"d4/1", "d4/2", "d4/3"},
		},
		{
			name:   "events",
			delete: func(h *Handler) func(context.Context, time.Time, int32) (int64, error) { return h.deleteEvents },
			period: testPeriods.Events,
			query:  listEvents,
			want:   []string{"ev-1", "ev-2", "ev-3"},
		},
		{
			name:   "audit log",
			delete: func(h *Handler) func(context.Context, time.Time, int32) (int64, error) { return h.deleteAuditEntries },
			period: testPeriods.AuditLog,
			query:  listAuditLog,
			want:   []string{"audit-2", "audit-3"},
		},
		{
			name:   "done jobs",
			delete: func(h *Handler) func(context.Context, time.Time, int32) (int64, error) { return h.deleteDoneJobs },
			period: DoneJobsPeriod,
			query:  listJobs,
			want:   []string{"job-dead-2d", "job-dead-31d", "job-done-1h", "job-done-2d", "job-pending"},
		},
		{
			name:   "dead jobs",
			delete: func(h *Handler) func(context.Context, time.Time, int32) (int64, error) { return h.deleteDeadJobs },
			period: DeadJobsPeriod,
			query:  listJobs,
			want:   []string{"job-dead-2d", "job-done-1h", "job-done-2d", "job-done-3d", "job-pending"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, 1)

			n, err := tt.delete(h.handler)(t.Context(), testNow.Add(-tt.period), 1)
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
			require.Equal(t, tt.want, h.strings(t, tt.query))
		})
	}
}

// TestDeletingAnInvestigationTakesWhatBelongsToIt checks that deleting the
// old investigations removes their steps, inbox rows, output refs, publish
// states, feedback, watchers with their checks, and event groups, and
// leaves those of the others.
func TestDeletingAnInvestigationTakesWhatBelongsToIt(t *testing.T) {
	t.Parallel()

	h := setup(t, 0)

	n, err := h.handler.deleteInvestigations(t.Context(), testNow.Add(-testPeriods.Investigations), DefaultBatchSize)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	want := []struct {
		query string
		rows  []string
	}{
		{listInvestigations, []string{investigation3, investigation4}},
		{listGroups, []string{group3, group4}},
		{listSteps, []string{"c3/0", "c4/0", "c4/1"}},
		{listInbox, []string{"c4"}},
		{listOutputRefs, []string{"c4"}},
		{listPublishStates, []string{"c4"}},
		{listFeedback, nil},
		{listWatchers, []string{"d4"}},
		{listWatcherChanges, []string{"c4"}},
		{listWatcherChecks, []string{"d4/1", "d4/2", "d4/3"}},
		{listEvents, []string{"ev-1", "ev-2", "ev-3", "ev-4"}},
	}

	for _, w := range want {
		require.Equal(t, w.rows, h.strings(t, w.query), w.query)
	}
}

// TestAuditLogDeletesOnlyThroughTheJob checks that a plain DELETE of the
// audit log fails while the job's delete works.
func TestAuditLogDeletesOnlyThroughTheJob(t *testing.T) {
	t.Parallel()

	h := setup(t, 0)

	_, err := h.pool.Exec(t.Context(), "DELETE FROM audit_log WHERE action = 'audit-1'")
	require.ErrorContains(t, err, "audit_log rows are deleted only by the retention job")

	_, err = h.pool.Exec(t.Context(), "UPDATE audit_log SET action = 'changed'")
	require.ErrorContains(t, err, "audit_log rows cannot be updated")

	n, err := h.handler.deleteAuditEntries(t.Context(), testNow.Add(-testPeriods.AuditLog), DefaultBatchSize)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	require.Equal(t, []string{"audit-3"}, h.strings(t, listAuditLog))
}
