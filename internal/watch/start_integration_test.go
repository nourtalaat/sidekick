//go:build integration

package watch

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// before are started first, in the checkout group unless they say
		// otherwise.
		before      []NewWatcher
		maxPerGroup int
		maxTotal    int
		watcher     NewWatcher
		wantErr     error
		wantLimit   string
	}{
		{name: "a new watcher starts", watcher: httpWatcher("health", "http://127.0.0.1/healthz", 2)},
		{
			name:    "a name the group has already",
			before:  []NewWatcher{httpWatcher("health", "http://127.0.0.1/a", 2)},
			watcher: httpWatcher("health", "http://127.0.0.1/b", 2),
			wantErr: ErrNameTaken,
		},
		{
			name:        "the group is at watch.maxPerGroup",
			before:      []NewWatcher{httpWatcher("one", "http://127.0.0.1/a", 2), httpWatcher("two", "http://127.0.0.1/b", 2)},
			maxPerGroup: 2,
			watcher:     httpWatcher("three", "http://127.0.0.1/c", 2),
			wantLimit:   "watch.maxPerGroup is 2, and the group has 2 watchers",
		},
		{
			name:      "the deployment is at watch.maxTotal",
			before:    []NewWatcher{inGroup(httpWatcher("other", "http://127.0.0.1/a", 2), otherGroup)},
			maxTotal:  1,
			watcher:   httpWatcher("health", "http://127.0.0.1/healthz", 2),
			wantLimit: "watch.maxTotal is 1, and 1 watchers are running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			for _, w := range tt.before {
				h.mustStart(t, w)
			}

			if tt.maxPerGroup > 0 {
				h.policy.MaxPerGroup = tt.maxPerGroup
			}

			if tt.maxTotal > 0 {
				h.policy.MaxTotal = tt.maxTotal
			}

			got, err := h.start(t, tt.watcher)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantLimit != "":
				limit, ok := errors.AsType[*LimitError](err)
				require.True(t, ok, "a limit error: %v", err)
				require.Equal(t, tt.wantLimit, limit.Error())
			default:
				require.NoError(t, err)
				require.Equal(t, StatusRunning, got.Status)
				require.Equal(t, int32(testInterval/time.Second), got.IntervalS)
				require.Equal(t, int32(2), got.PassesToRecover)
				require.Equal(t, int32(5), got.CheckTimeoutS)
				require.True(t, h.now.Add(testTimeout).Equal(got.Deadline))
				require.Equal(t, &checkoutInvestigation, got.InvestigationID)

				ticks := h.pendingTicks(t)
				require.Len(t, ticks, 1)
				require.Equal(t, TickKey(got.ID), *ticks[0].UniqueKey)
				require.Equal(t, TickPriority, ticks[0].Priority)

				return
			}

			require.Len(t, h.pendingTicks(t), len(tt.before), "a refused watcher enqueues no tick")
		})
	}
}

func TestEndGroupResolved(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		watcher        NewWatcher
		wantStatus     string
		wantStopReason *string
	}{
		{
			name:           "an HTTP watcher stops",
			watcher:        httpWatcher("health", "http://127.0.0.1/healthz", 2),
			wantStatus:     StatusStopped,
			wantStopReason: new(StopGroupResolved),
		},
		{
			name:       "an alert watcher recovers",
			watcher:    NewWatcher{GroupID: checkoutGroup, Name: "cleared", Origin: OriginRoute, Check: AlertCheck{}},
			wantStatus: StatusRecovered,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			w := h.mustStart(t, tt.watcher)

			var ended []string

			require.NoError(t, pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
				got, err := EndGroupResolved(t.Context(), tx, checkoutGroup, h.now, h.recorder)
				for _, e := range got {
					ended = append(ended, e.Name)
				}

				return err
			}))

			require.Equal(t, []string{tt.watcher.Name}, ended)

			got := h.watcher(t, w.ID)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantStopReason, got.StopReason)
			require.NotNil(t, got.EndedAt)

			running, err := HasRunning(t.Context(), h.pool, checkoutGroup, h.now)
			require.NoError(t, err)
			require.False(t, running)
		})
	}
}

func TestHasRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		after time.Duration
		end   bool
		want  bool
	}{
		{name: "a running watcher", want: true},
		{name: "a watcher past its deadline", after: testTimeout},
		{name: "an ended watcher", end: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			h.mustStart(t, httpWatcher("health", "http://127.0.0.1/healthz", 2))

			if tt.end {
				_, err := h.pool.Exec(t.Context(), "UPDATE watchers SET status = 'timed_out'")
				require.NoError(t, err)
			}

			running, err := HasRunning(t.Context(), h.pool, checkoutGroup, h.now.Add(tt.after))
			require.NoError(t, err)
			require.Equal(t, tt.want, running)

			other, err := HasRunning(t.Context(), h.pool, otherGroup, h.now)
			require.NoError(t, err)
			require.False(t, other, "another group's watcher does not count")
		})
	}
}

// inGroup returns w in the group with id, without an investigation.
func inGroup(w NewWatcher, id uuid.UUID) NewWatcher {
	w.GroupID = id
	w.InvestigationID = uuid.Nil

	return w
}
