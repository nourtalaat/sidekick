package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// discard logs nothing.
var discard = slog.New(slog.DiscardHandler)

// Recorder records the state changes of watchers. Every change is written
// to the inbox of the watcher's investigation, so a running investigation
// sees it at its next turn, shown in the investigation's outputs and
// audited. The Recorder also logs each change, with IDs and names only,
// and counts it in MetricStateChanges. Make it with NewRecorder; a nil
// *Recorder still writes, shows and audits every change, but logs and
// counts nothing.
type Recorder struct {
	log     *slog.Logger
	changes *prometheus.CounterVec
}

// NewRecorder returns a Recorder that logs to log, when it is not nil, and
// counts in MetricStateChanges on reg. Recorders made on one registry
// share the counter.
func NewRecorder(log *slog.Logger, reg prometheus.Registerer) (*Recorder, error) {
	changes, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricStateChanges,
		Help: "Watcher state changes, by the new status: started, recovered, timed_out or stopped.",
	}, []string{labelStatus}))
	if err != nil {
		return nil, fmt.Errorf("set up the watcher state change metric: %w", err)
	}

	if log == nil {
		log = discard
	}

	return &Recorder{log: log, changes: changes}, nil
}

// changed records that watcher w changed state from old, nil for a
// watcher that just started; the watcher row holds the new state already.
// In q it writes the change to the inbox of the watcher's investigation,
// shows it in the investigation's outputs and writes its audit row, then
// logs and counts it. A watcher without an investigation gets only the
// audit row, the log line and the count.
func (r *Recorder) changed(ctx context.Context, q db.DBTX, old *db.Watcher, w db.Watcher) error {
	if w.InvestigationID != nil {
		if err := writeChange(ctx, q, *w.InvestigationID, w); err != nil {
			return fmt.Errorf("record the state change of watcher %s: %w", w.ID, err)
		}
	}

	log := discard
	if r != nil {
		log = r.log
	}

	if err := stateChanged(ctx, q, log, old, w); err != nil {
		return fmt.Errorf("watcher %s: %w", w.ID, err)
	}

	if r != nil {
		r.changes.WithLabelValues(statusLabel(w.Status)).Inc()
	}

	return nil
}

// States returns the state of every watcher of the group with groupID,
// oldest first, as the outputs show them.
func States(ctx context.Context, q db.DBTX, groupID uuid.UUID) ([]output.WatcherState, error) {
	rows, err := db.New(q).ListGroupWatchers(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list the watchers of group %s: %w", groupID, err)
	}

	states := make([]output.WatcherState, len(rows))
	for i, row := range rows {
		states[i] = stateOf(row.Watcher, deref(row.LastDetail))
	}

	return states, nil
}

// NotStarted writes to the inbox of the investigation with
// investigationID that the route or strategy watcher named name, with a
// check of checkType, did not start, and why, in q.
func NotStarted(ctx context.Context, q db.DBTX, investigationID uuid.UUID, name string, checkType Type, reason string) error {
	item := InboxItem{State: output.WatcherState{Name: name, CheckType: string(checkType)}, NotStarted: reason}
	if err := addToInbox(ctx, q, investigationID, item); err != nil {
		return fmt.Errorf("note that watcher %s did not start: %w", name, err)
	}

	return nil
}

// writeChange writes the new state of w to the inbox of the investigation
// with investigationID and shows it in the investigation's outputs.
func writeChange(ctx context.Context, q db.DBTX, investigationID uuid.UUID, w db.Watcher) error {
	row, err := db.New(q).GetWatcherWithLastCheck(ctx, w.ID)
	if err != nil {
		return fmt.Errorf("load the last check: %w", err)
	}

	state := stateOf(w, deref(row.LastDetail))

	if err := addToInbox(ctx, q, investigationID, InboxItem{State: state}); err != nil {
		return fmt.Errorf("write it to the inbox: %w", err)
	}

	if err := publish.WatcherChanged(ctx, q, investigationID, state); err != nil {
		return fmt.Errorf("show it in the outputs: %w", err)
	}

	return nil
}

// addToInbox adds item to the inbox of the investigation with
// investigationID.
func addToInbox(ctx context.Context, q db.DBTX, investigationID uuid.UUID, item InboxItem) error {
	body, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode the inbox item: %w", err)
	}

	if err := db.New(q).InsertInboxItem(ctx, db.InsertInboxItemParams{InvestigationID: investigationID, Kind: InboxKind, Body: body}); err != nil {
		return fmt.Errorf("add it to the inbox of %s: %w", investigationID, err)
	}

	return nil
}

// stateOf returns w as the outputs show it, with lastResult, the detail of
// its last check.
func stateOf(w db.Watcher, lastResult string) output.WatcherState {
	s := output.WatcherState{
		ID:              w.ID.String(),
		Name:            w.Name,
		CheckType:       w.CheckType,
		Status:          output.WatcherStatus(w.Status),
		PassesInARow:    int(w.PassesInARow),
		PassesToRecover: int(w.PassesToRecover),
		IntervalSeconds: int(w.IntervalS),
		Checks:          int(w.Checks),
		LastCheckAt:     deref(w.LastCheckAt),
		LastResult:      lastResult,
		StartedAt:       w.StartedAt,
		EndedAt:         deref(w.EndedAt),
		StopReason:      deref(w.StopReason),
	}

	if w.StoppedBySlackUser != nil {
		s.StoppedBy = &output.Person{SlackUserID: *w.StoppedBySlackUser}
	}

	return s
}

// statusLabel is the MetricStateChanges label of status: started for a
// watcher that is running, the status otherwise.
func statusLabel(status string) string {
	if status == StatusRunning {
		return "started"
	}

	return status
}
