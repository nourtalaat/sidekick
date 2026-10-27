//go:build integration

package grouper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// Watchers of the seeded group, as SQL, for the tests to insert.
const (
	runningHTTPWatcher = `INSERT INTO watchers (id, group_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s, status, started_at, deadline)
		VALUES ('0192d3a4-0000-7000-8000-0000000000d1', '0192d3a4-0000-7000-8000-0000000000b1', 'health', 'route', 'http',
		        '{"url": "http://127.0.0.1/healthz"}', 30, 2, 10, 'running', '2026-10-26 09:00:00+00', '2100-01-01 00:00:00+00')`
	runningAlertWatcher = `INSERT INTO watchers (id, group_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s, status, started_at, deadline)
		VALUES ('0192d3a4-0000-7000-8000-0000000000d2', '0192d3a4-0000-7000-8000-0000000000b1', 'cleared', 'route', 'alert',
		        '{}', 30, 1, 10, 'running', '2026-10-26 09:00:00+00', '2100-01-01 00:00:00+00')`
	pastDeadlineWatcher = `INSERT INTO watchers (id, group_id, name, origin, check_type, spec, interval_s, passes_to_recover, check_timeout_s, status, started_at, deadline)
		VALUES ('0192d3a4-0000-7000-8000-0000000000d3', '0192d3a4-0000-7000-8000-0000000000b1', 'late', 'route', 'http',
		        '{"url": "http://127.0.0.1/healthz"}', 30, 2, 10, 'running', '2026-10-26 08:00:00+00', '2026-10-26 09:00:00+00')`
	// recoveryOfDisk is the recovery of the seeded group's only alert.
	recoveryOfDisk = `INSERT INTO events (id, intake, key, kind, trusted, alert_name, service, labels, fingerprint, status, route, group_id, received_at)
		VALUES ('0192d3a4-0000-7000-8000-000000000009', 'grafana', 'disk-1-resolved', 'resolved', false, 'DiskFull', 'db',
		        '{}', 'fp-disk', 'grouped', 'default', '0192d3a4-0000-7000-8000-0000000000b1', '2026-10-26 09:20:00+00')`
)

func TestCloseGroupWithWatchers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		prepare    []string
		wantStatus string
		wantReason *string
		// wantWatchers are the statuses of the group's watchers by name
		// after the job.
		wantWatchers map[string]string
		// wantKeyReleased is whether the job gave up its unique key.
		wantKeyReleased bool
	}{
		{
			name:            "the window ended while a watcher runs",
			prepare:         []string{runningHTTPWatcher},
			wantStatus:      "open",
			wantWatchers:    map[string]string{"health": watch.StatusRunning},
			wantKeyReleased: true,
		},
		{
			name:         "the window ended and the only watcher is past its deadline",
			prepare:      []string{pastDeadlineWatcher},
			wantStatus:   "closed",
			wantReason:   new(ClosedWindowEnded),
			wantWatchers: map[string]string{"late": watch.StatusRunning},
		},
		{
			name:         "every alert recovered while watchers run",
			prepare:      []string{runningHTTPWatcher, runningAlertWatcher, recoveryOfDisk},
			wantStatus:   "closed",
			wantReason:   new(ClosedResolved),
			wantWatchers: map[string]string{"health": watch.StatusStopped, "cleared": watch.StatusRecovered},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			for _, sql := range tt.prepare {
				_, err := h.pool.Exec(t.Context(), sql)
				require.NoError(t, err)
			}

			require.NoError(t, enqueueClose(t.Context(), h.pool, diskGroup, seededWindowEnd, windowKey(diskGroup)))

			job := h.claimClose(t)
			closer := NewCloser(CloserOptions{Pool: h.pool, Now: func() time.Time { return seededWindowEnd.Add(time.Second) }})
			require.NoError(t, closer.Handle(t.Context(), job))

			grp := h.groupByID(t, diskGroup)
			require.Equal(t, tt.wantStatus, grp.Status)
			require.Equal(t, tt.wantReason, grp.ClosedReason)
			require.Equal(t, tt.wantWatchers, h.watcherStatuses(t))

			after, err := db.New(h.pool).GetJob(t.Context(), job.ID)
			require.NoError(t, err)
			require.Equal(t, tt.wantKeyReleased, after.UniqueKey == nil)
		})
	}
}

func TestGroupClosesWhenTheLastWatcherEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// ended are the watchers that end before the second job runs.
		ended      []string
		wantStatus string
	}{
		{name: "the last watcher ended", ended: []string{"health", "cleared"}, wantStatus: "closed"},
		{name: "one of two watchers ended", ended: []string{"health"}, wantStatus: "open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			for _, sql := range []string{runningHTTPWatcher, runningAlertWatcher} {
				_, err := h.pool.Exec(t.Context(), sql)
				require.NoError(t, err)
			}

			closer := NewCloser(CloserOptions{Pool: h.pool, Now: func() time.Time { return seededWindowEnd.Add(time.Second) }})

			require.NoError(t, enqueueClose(t.Context(), h.pool, diskGroup, seededWindowEnd, windowKey(diskGroup)))
			first := h.claimClose(t)
			require.NoError(t, closer.Handle(t.Context(), first))
			require.Equal(t, "open", h.groupByID(t, diskGroup).Status, "the window ended, but watchers run")

			for _, name := range tt.ended {
				_, err := h.pool.Exec(t.Context(), "UPDATE watchers SET status = 'recovered' WHERE name = $1", name)
				require.NoError(t, err)

				// The first job is still running, as a worker completes it
				// only after Handle returns; its key is free already.
				require.NoError(t, EnqueueClose(t.Context(), h.pool, diskGroup))
			}

			second := h.claimClose(t)
			require.NoError(t, closer.Handle(t.Context(), second))

			grp := h.groupByID(t, diskGroup)
			require.Equal(t, tt.wantStatus, grp.Status)

			if tt.wantStatus == "closed" {
				require.Equal(t, new(ClosedWindowEnded), grp.ClosedReason)
			}
		})
	}
}

func TestGroupJoinsAfterTheWindowWhileAWatcherRuns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		watcher    bool
		wantOpened bool
	}{
		{name: "a running watcher keeps the group open", watcher: true},
		{name: "without a watcher a new group opens", wantOpened: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			_, err := h.pool.Exec(t.Context(), "UPDATE event_groups SET window_until = '2026-10-26 09:05:00+00'")
			require.NoError(t, err)

			if tt.watcher {
				_, err = h.pool.Exec(t.Context(), runningHTTPWatcher)
				require.NoError(t, err)
			}

			res, err := h.group(t, diskRouted, Route{Name: "default", Window: window})
			require.NoError(t, err)
			require.Equal(t, tt.wantOpened, res.Opened)

			if !tt.wantOpened {
				require.Equal(t, diskGroup, res.GroupID)
			}
		})
	}
}

// watcherStatuses returns the status of every watcher, by name.
func (h *harness) watcherStatuses(t *testing.T) map[string]string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT name, status FROM watchers")
	require.NoError(t, err)

	out := map[string]string{}

	for rows.Next() {
		var name, status string

		require.NoError(t, rows.Scan(&name, &status))

		out[name] = status
	}

	require.NoError(t, rows.Err())

	return out
}
