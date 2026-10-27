//go:build integration

package grouper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// Fixtures that add watchers and a recovery to the seeded group.
const (
	runningHTTPWatcher  = "fixture/watcher-http.sql"
	runningAlertWatcher = "fixture/watcher-alert.sql"
	pastDeadlineWatcher = "fixture/watcher-past-deadline.sql"
	recoveryOfDisk      = "fixture/recovery-of-disk.sql"
)

func TestCloseGroupWithWatchers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// prepare are the fixtures loaded first.
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

			for _, fixture := range tt.prepare {
				storetest.LoadFixture(t, h.pool, fixture)
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

			storetest.LoadFixture(t, h.pool, runningHTTPWatcher)
			storetest.LoadFixture(t, h.pool, runningAlertWatcher)

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
				storetest.LoadFixture(t, h.pool, runningHTTPWatcher)
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
