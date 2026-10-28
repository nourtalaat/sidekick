//go:build integration

package watch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// stopper is the Slack user who stops watchers in the tests.
const stopper = "U01STOP"

// TestStateChangesReachTheInboxAndTheOutputs starts two watchers, stops
// one for a responder and lets the other recover at a tick, and checks
// that every change is in the investigation's inbox and among the changes
// its outputs get, in order, without replacing the publish state.
func TestStateChangesReachTheInboxAndTheOutputs(t *testing.T) {
	t.Parallel()

	h := setup(t)

	cleared := h.mustStart(t, NewWatcher{
		GroupID: checkoutGroup, InvestigationID: checkoutInvestigation, Name: "cleared", Origin: OriginModel,
		Check: AlertCheck{}, Settings: Settings{Interval: testInterval, Timeout: testTimeout},
	})
	health := h.mustStart(t, httpWatcher("health", "http://127.0.0.1/healthz", 2))

	_, stopped, err := h.stopper(t).Stop(t.Context(), health.ID, output.Person{SlackUserID: stopper})
	require.NoError(t, err)
	require.True(t, stopped)

	_, err = h.pool.Exec(t.Context(), `INSERT INTO events (id, intake, key, kind, trusted, labels, fingerprint, status, group_id, received_at)
		VALUES ($1, 'alertmanager', 'checkout-1-resolved', 'resolved', true, '{}', 'alert-checkout', 'grouped', $2, '2026-10-26 09:20:00+00')`,
		ids.New(), checkoutGroup)
	require.NoError(t, err)

	h.tick(t, h.ticker(t))
	require.Equal(t, StatusRecovered, h.watcher(t, cleared.ID).Status)
	require.Equal(t, StatusStopped, h.watcher(t, health.ID).Status, "the stopped watcher's tick changes nothing")

	type change struct {
		Name      string
		Status    output.WatcherStatus
		Reason    string
		StoppedBy string
	}

	want := []change{
		{Name: "cleared", Status: output.WatcherRunning},
		{Name: "health", Status: output.WatcherRunning},
		{Name: "health", Status: output.WatcherStopped, Reason: StopResponder, StoppedBy: stopper},
		{Name: "cleared", Status: output.WatcherRecovered},
	}

	of := func(s output.WatcherState) change {
		c := change{Name: s.Name, Status: s.Status, Reason: s.StopReason}
		if s.StoppedBy != nil {
			c.StoppedBy = s.StoppedBy.SlackUserID
		}

		return c
	}

	var inbox []change

	for _, item := range h.inbox(t) {
		require.Equal(t, InboxKind, item.Kind)
		require.Nil(t, item.DeliveredSeq, "the item waits for the investigation's next turn")

		var body InboxItem
		require.NoError(t, json.Unmarshal(item.Body, &body))

		inbox = append(inbox, of(body.State))
	}

	require.Equal(t, want, inbox)

	var (
		sent []change
		seqs []int64
	)

	for _, c := range h.changes(t) {
		var state output.WatcherState
		require.NoError(t, json.Unmarshal(c.State, &state))

		sent = append(sent, of(state))
		seqs = append(seqs, c.Seq)
	}

	require.Equal(t, want, sent)
	require.Equal(t, []int64{5, 7, 9, 11}, seqs, "each change takes the seq below the state's new one")

	var (
		seq   int64
		phase string
	)

	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT seq, phase FROM publish_states WHERE investigation_id = $1", checkoutInvestigation).Scan(&seq, &phase))
	require.Equal(t, int64(12), seq)
	require.Equal(t, "running", phase, "a change does not replace the state")

	require.Equal(t, map[string]float64{"started": 2, "recovered": 1, "stopped": 1}, h.stateChanges(t))
	require.Equal(t, []uuid.UUID{checkoutGroup, checkoutGroup}, h.closed(), "both ended watchers let the group close")
}

func TestStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// end sets the watcher's status before the stop.
		end         string
		unknown     bool
		wantStopped bool
	}{
		{name: "a running watcher stops", wantStopped: true},
		{name: "a watcher that has ended already", end: StatusRecovered},
		{name: "a watcher that does not exist", unknown: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			w := h.mustStart(t, httpWatcher("health", "http://127.0.0.1/healthz", 2))

			if tt.end != "" {
				_, err := h.pool.Exec(t.Context(), "UPDATE watchers SET status = $1", tt.end)
				require.NoError(t, err)
			}

			id := w.ID
			if tt.unknown {
				id = ids.New()
			}

			got, stopped, err := h.stopper(t).Stop(t.Context(), id, output.Person{SlackUserID: stopper})
			require.NoError(t, err)
			require.Equal(t, tt.wantStopped, stopped)

			if !tt.wantStopped {
				require.Len(t, h.inbox(t), 1, "only the start is recorded")
				require.Empty(t, h.closed())

				return
			}

			require.Equal(t, w.ID, got.ID)

			stored := h.watcher(t, w.ID)
			require.Equal(t, StatusStopped, stored.Status)
			require.Equal(t, new(StopResponder), stored.StopReason)
			require.Equal(t, new(stopper), stored.StoppedBySlackUser)
			require.True(t, h.now.Equal(*stored.EndedAt))
			require.Equal(t, []uuid.UUID{checkoutGroup}, h.closed())

			again, stopped, err := h.stopper(t).Stop(t.Context(), w.ID, output.Person{SlackUserID: "U01OTHER"})
			require.NoError(t, err)
			require.False(t, stopped, "a second stop changes nothing")
			require.Equal(t, new(stopper), again.StoppedBySlackUser)
		})
	}
}

func TestStates(t *testing.T) {
	t.Parallel()

	h := setup(t)
	w := h.mustStart(t, httpWatcher("health", "http://127.0.0.1/healthz", 2))

	_, err := h.pool.Exec(t.Context(), "UPDATE watchers SET checks = 3, passes_in_a_row = 1, last_check_at = $1", h.now)
	require.NoError(t, err)

	_, err = h.pool.Exec(t.Context(), `INSERT INTO watcher_checks (watcher_id, seq, at, result, detail, duration_ms)
		VALUES ($1, 2, $2, 'fail', 'HTTP 503', 5), ($1, 3, $2, 'pass', 'HTTP 200', 4)`, w.ID, h.now)
	require.NoError(t, err)

	got, err := States(t.Context(), h.pool, checkoutGroup)
	require.NoError(t, err)
	require.Equal(t, []output.WatcherState{{
		ID:              w.ID.String(),
		Name:            "health",
		CheckType:       string(TypeHTTP),
		Status:          output.WatcherRunning,
		PassesInARow:    1,
		PassesToRecover: 2,
		IntervalSeconds: int(testInterval.Seconds()),
		Checks:          3,
		LastCheckAt:     h.now,
		LastResult:      "HTTP 200",
		StartedAt:       h.now,
	}}, normalized(got))

	none, err := States(t.Context(), h.pool, otherGroup)
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestRunningGauge(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.mustStart(t, httpWatcher("one", "http://127.0.0.1/a", 2))
	h.mustStart(t, httpWatcher("two", "http://127.0.0.1/b", 2))

	require.NoError(t, RegisterRunning(h.reg, h.pool, nil))

	families, err := h.reg.Gather()
	require.NoError(t, err)

	var running *dto.MetricFamily

	for _, f := range families {
		if f.GetName() == MetricRunning {
			running = f
		}
	}

	require.NotNil(t, running)
	require.Len(t, running.GetMetric(), 1)
	require.InDelta(t, 2, running.GetMetric()[0].GetGauge().GetValue(), 0)
}

// stopper returns a Stopper on the harness's clock that records the
// groups it lets close.
func (h *harness) stopper(t *testing.T) *Stopper {
	t.Helper()

	s, err := NewStopper(StopperOptions{Pool: h.pool, CloseGroup: h.closeGroup, Recorder: h.recorder, Now: func() time.Time { return h.now }})
	require.NoError(t, err)

	return s
}

// inbox returns the inbox items of the checkout investigation, oldest
// first.
func (h *harness) inbox(t *testing.T) []db.InvestigationInbox {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM investigation_inbox WHERE investigation_id = $1 ORDER BY id", checkoutInvestigation)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.InvestigationInbox])
	require.NoError(t, err)

	return out
}

// changes returns the watcher changes of the checkout investigation,
// oldest first.
func (h *harness) changes(t *testing.T) []db.WatcherChange {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM watcher_changes WHERE investigation_id = $1 ORDER BY seq", checkoutInvestigation)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.WatcherChange])
	require.NoError(t, err)

	return out
}

// stateChanges returns MetricStateChanges by status.
func (h *harness) stateChanges(t *testing.T) map[string]float64 {
	t.Helper()

	families, err := h.reg.Gather()
	require.NoError(t, err)

	out := map[string]float64{}

	for _, f := range families {
		if f.GetName() != MetricStateChanges {
			continue
		}

		for _, m := range f.GetMetric() {
			out[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}

	return out
}

// normalized returns states with their times in UTC, as the database
// returns them in the local zone.
func normalized(states []output.WatcherState) []output.WatcherState {
	for i := range states {
		states[i].StartedAt = states[i].StartedAt.UTC()
		states[i].LastCheckAt = states[i].LastCheckAt.UTC()
	}

	return states
}
