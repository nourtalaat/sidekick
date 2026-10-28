//go:build integration

package publish

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// watcherStartedAt is when the test watchers started.
var watcherStartedAt = time.Date(2026, 10, 26, 14, 2, 5, 0, time.UTC)

// liveWatchers is what the test Watchers option returns for every group.
var liveWatchers = []output.WatcherState{
	{ID: "w1", Name: "checkout-health", CheckType: "http", Status: output.WatcherRecovered, StartedAt: watcherStartedAt},
	{ID: "w2", Name: "alerts-cleared", CheckType: "alert", Status: output.WatcherRunning, StartedAt: watcherStartedAt},
}

// errReceiverDown is the error of a recordingSink publish that fails.
var errReceiverDown = errors.New("the receiver is down")

// shown is one update a recordingSink got, as the tests check it.
type shown struct {
	Phase   output.Phase
	Seq     int64
	Watcher string
	Status  output.WatcherStatus
}

func TestWatcherChanges(t *testing.T) {
	t.Parallel()

	started := output.WatcherState{ID: "w1", Name: "checkout-health", Status: output.WatcherRunning}
	recovered := output.WatcherState{ID: "w1", Name: "checkout-health", Status: output.WatcherRecovered}
	other := output.WatcherState{ID: "w2", Name: "alerts-cleared", Status: output.WatcherRunning}
	final := Update{
		InvestigationID: webhookInvestigation,
		Phase:           output.PhaseFinal,
		Seq:             1,
		Report:          &report.Report{Verdict: report.TruePositive, Confidence: report.High, Reason: "Deploy 4f2c1a broke checkout."},
	}

	tests := []struct {
		name string
		// accepts are the phases the sink takes.
		accepts []output.Phase
		// final, when set, is written after the first publish and before
		// the changes.
		final   *Update
		changes []output.WatcherState
		want    []shown
	}{
		{
			name:    "a burst of changes is sent once each, in order, before one state",
			accepts: []output.Phase{output.PhaseQueued, output.PhaseWatcher},
			changes: []output.WatcherState{started, other, recovered},
			want: []shown{
				{Phase: output.PhaseWatcher, Seq: 1, Watcher: "checkout-health", Status: output.WatcherRunning},
				{Phase: output.PhaseWatcher, Seq: 3, Watcher: "alerts-cleared", Status: output.WatcherRunning},
				{Phase: output.PhaseWatcher, Seq: 5, Watcher: "checkout-health", Status: output.WatcherRecovered},
				{Phase: output.PhaseQueued, Seq: 6},
			},
		},
		{
			name:    "a change does not replace a final result that waits",
			accepts: []output.Phase{output.PhaseFinal, output.PhaseWatcher},
			final:   &final,
			changes: []output.WatcherState{recovered},
			want: []shown{
				{Phase: output.PhaseWatcher, Seq: 2, Watcher: "checkout-health", Status: output.WatcherRecovered},
				{Phase: output.PhaseFinal, Seq: 3},
			},
		},
		{
			name:    "a sink that takes no watcher changes gets only the state",
			accepts: []output.Phase{output.PhaseQueued},
			changes: []output.WatcherState{started, recovered},
			want:    []shown{{Phase: output.PhaseQueued, Seq: 4}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, sink := setupRecording(t, tt.accepts)
			h.open(t, webhookInvestigation, slackSink)

			if tt.final != nil {
				h.enqueue(t, *tt.final)
			} else {
				require.NoError(t, h.run(t))
				sink.reset()
			}

			for _, c := range tt.changes {
				h.watcherChanged(t, webhookInvestigation, c)
			}

			require.NoError(t, h.run(t))
			require.Equal(t, tt.want, sink.shown())
			require.Equal(t, string(stateOr(tt.final)), h.state(t, webhookInvestigation).Phase, "the state is not replaced")

			for _, u := range sink.all() {
				require.Equal(t, liveWatchers, u.Watchers, "every update shows the live state of the group's watchers")
			}

			sink.reset()
			require.NoError(t, enqueueJob(t.Context(), h.pool, Payload{InvestigationID: webhookInvestigation, Sink: slackSink}))
			require.NoError(t, h.run(t))
			require.Empty(t, sink.shown(), "nothing is sent twice")
		})
	}
}

// TestAFailedChangeIsSentAgainAlone fails the second of two changes; the
// retry sends only that one, then the state.
func TestAFailedChangeIsSentAgainAlone(t *testing.T) {
	t.Parallel()

	h, sink := setupRecording(t, []output.Phase{output.PhaseQueued, output.PhaseWatcher})
	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))
	sink.reset()

	h.watcherChanged(t, webhookInvestigation, output.WatcherState{ID: "w1", Name: "checkout-health", Status: output.WatcherRunning})
	h.watcherChanged(t, webhookInvestigation, output.WatcherState{ID: "w1", Name: "checkout-health", Status: output.WatcherTimedOut})

	sink.failAt(2)

	job := h.claim(t)
	require.ErrorContains(t, h.handler.Handle(t.Context(), job), "the receiver is down")

	_, err := db.New(h.pool).FailJob(t.Context(), db.FailJobParams{ID: job.ID, ClaimToken: *job.ClaimToken, LastError: "failed"})
	require.NoError(t, err)

	require.NoError(t, h.run(t))
	require.Equal(t, []shown{
		{Phase: output.PhaseWatcher, Seq: 1, Watcher: "checkout-health", Status: output.WatcherRunning},
		{Phase: output.PhaseWatcher, Seq: 3, Watcher: "checkout-health", Status: output.WatcherTimedOut},
		{Phase: output.PhaseQueued, Seq: 4},
	}, sink.shown())
}

func TestWatcherChangedWithoutAPublishState(t *testing.T) {
	t.Parallel()

	h, _ := setupRecording(t, []output.Phase{output.PhaseWatcher})
	h.watcherChanged(t, slackInvestigation, output.WatcherState{ID: "w1", Status: output.WatcherRunning})

	require.Empty(t, collect[db.WatcherChange](t, h.pool, "SELECT * FROM watcher_changes"))
	require.Empty(t, h.jobs(t))
}

// setupRecording returns a harness whose only sink, slackSink, records
// what it gets and accepts the phases in accepts, and whose Watchers
// option returns liveWatchers.
func setupRecording(t *testing.T, accepts []output.Phase) (*harness, *recordingSink) {
	t.Helper()

	h := setup(t)
	sink := &recordingSink{accepts: accepts}

	h = setupWith(t, h.pool, h.slack, map[string]output.Sink{slackSink: sink})
	h.handler.watchers = func(context.Context, db.DBTX, uuid.UUID) ([]output.WatcherState, error) {
		return liveWatchers, nil
	}

	return h, sink
}

// watcherChanged calls WatcherChanged in its own transaction.
func (h *harness) watcherChanged(t *testing.T, id uuid.UUID, state output.WatcherState) {
	t.Helper()

	h.inTx(t, func(tx pgx.Tx) error { return WatcherChanged(t.Context(), tx, id, state) })
}

// stateOr returns the phase of u, or queued for nil.
func stateOr(u *Update) output.Phase {
	if u == nil {
		return output.PhaseQueued
	}

	return u.Phase
}

// recordingSink keeps every update it gets and returns a ref that names
// its seq. failAt makes one later publish fail.
type recordingSink struct {
	accepts []output.Phase

	mu      sync.Mutex
	updates []output.Update
	calls   int
	fail    int
}

func (s *recordingSink) Accepts(phase output.Phase) bool {
	return slices.Contains(s.accepts, phase)
}

func (s *recordingSink) Publish(_ context.Context, u output.Update) (output.Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++

	if s.calls == s.fail {
		return output.Ref{}, errReceiverDown
	}

	s.updates = append(s.updates, u)

	seq := strconv.FormatInt(u.Seq, 10)

	return output.Ref{Sink: slackSink, External: "update-" + seq, Data: map[string]string{"seq": seq}}, nil
}

// failAt makes the nth publish from now fail.
func (s *recordingSink) failAt(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.fail = s.calls + n
}

func (s *recordingSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.updates = nil
}

func (s *recordingSink) all() []output.Update {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]output.Update(nil), s.updates...)
}

func (s *recordingSink) shown() []shown {
	var out []shown

	for _, u := range s.all() {
		got := shown{Phase: u.Phase, Seq: u.Seq}
		if u.Watcher != nil {
			got.Watcher, got.Status = u.Watcher.Name, u.Watcher.Status
		}

		out = append(out, got)
	}

	return out
}
