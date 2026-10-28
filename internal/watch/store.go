package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The values of watchers.status. Every status but StatusRunning is final.
const (
	StatusRunning   = "running"
	StatusRecovered = "recovered"
	StatusTimedOut  = "timed_out"
	StatusStopped   = "stopped"
)

// The values of watchers.stop_reason.
const (
	// StopGroupResolved: every alert of the group recovered.
	StopGroupResolved = "group_resolved"
	// StopResponder: a responder stopped the watcher.
	StopResponder = "responder"
	// StopConfigChanged: the config no longer allows the watcher's check.
	StopConfigChanged = "config_changed"
)

const (
	// TickPriority is the priority of watch_tick jobs: ahead of
	// investigations, so checks stay on time when the queue is busy.
	TickPriority int16 = 50
	// tickKeyPrefix starts the unique key of a watcher's watch_tick job.
	tickKeyPrefix = "watch:"
	// startLockKey is the transaction-level advisory lock that every
	// watcher start takes, so the counts against watch.maxPerGroup and
	// watch.maxTotal are exact across replicas.
	startLockKey int64 = 0x736b2d7761746368
	// alertPassesToRecover is the passesToRecover of alert checks: the
	// recovery events are final, so one pass is enough.
	alertPassesToRecover = 1
)

var (
	// ErrNameTaken means the group has a watcher with the same name.
	ErrNameTaken = errors.New("the group has a watcher with this name already")

	errNoWatcherID = errors.New("the payload has no watcher_id")
)

// LimitError means a watcher would go over watch.maxPerGroup or
// watch.maxTotal, so it did not start. Its message names the limit.
type LimitError struct {
	// Limit is the config field, such as watch.maxPerGroup.
	Limit string
	Max   int
	// Count is how many watchers count against the limit.
	Count int64
}

// Error names the limit and the count.
func (e *LimitError) Error() string {
	if e.Limit == limitPerGroup {
		return fmt.Sprintf("%s is %d, and the group has %d watchers", e.Limit, e.Max, e.Count)
	}

	return fmt.Sprintf("%s is %d, and %d watchers are running", e.Limit, e.Max, e.Count)
}

// The limits a LimitError names.
const (
	limitPerGroup = "watch.maxPerGroup"
	limitTotal    = "watch.maxTotal"
)

// NewWatcher is a watcher to start.
type NewWatcher struct {
	GroupID uuid.UUID
	// InvestigationID is the group's investigation; uuid.Nil for none.
	InvestigationID uuid.UUID
	Name            string
	Origin          Origin
	// StartedBySeq is the seq of the model turn that called start_watcher.
	StartedBySeq *int32
	Check        Check
	// Settings are the watcher's resolved settings.
	Settings Settings
}

// tickPayload is the payload of a watch_tick job.
type tickPayload struct {
	WatcherID uuid.UUID `json:"watcher_id"`
}

// Start starts a watcher in tx, which must hold the lock of the
// watcher's group: it checks the counts, stores the watcher, enqueues its
// first watch_tick job to run at once and records the start with rec. It
// returns a *LimitError when a count is full, and ErrNameTaken when the
// group has a watcher with the name; tx stays usable after both.
func Start(ctx context.Context, tx pgx.Tx, p Policy, w NewWatcher, now time.Time, rec *Recorder) (db.Watcher, error) {
	queries := db.New(tx)

	if err := queries.LockWatcherStarts(ctx, startLockKey); err != nil {
		return db.Watcher{}, fmt.Errorf("lock the watcher counts: %w", err)
	}

	if err := checkCounts(ctx, queries, p, w.GroupID, now); err != nil {
		return db.Watcher{}, fmt.Errorf("start watcher %s: %w", w.Name, err)
	}

	params, err := insertParams(w, now)
	if err != nil {
		return db.Watcher{}, fmt.Errorf("start watcher %s: %w", w.Name, err)
	}

	started, err := queries.InsertWatcher(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Watcher{}, fmt.Errorf("start watcher %s: %w", w.Name, ErrNameTaken)
	}

	if err != nil {
		return db.Watcher{}, fmt.Errorf("store watcher %s: %w", w.Name, err)
	}

	if err := enqueueTick(ctx, tx, started.ID, time.Time{}); err != nil {
		return db.Watcher{}, fmt.Errorf("watcher %s: %w", w.Name, err)
	}

	if err := rec.changed(ctx, tx, nil, started); err != nil {
		return db.Watcher{}, fmt.Errorf("watcher %s: %w", w.Name, err)
	}

	return started, nil
}

// EndGroupResolved ends the running watchers of a group whose alerts have
// all recovered, in tx: alert watchers have recovered, and every other one
// stops with group_resolved. It records each change with rec and returns
// the watchers it ended.
func EndGroupResolved(ctx context.Context, tx pgx.Tx, groupID uuid.UUID, now time.Time, rec *Recorder) ([]db.Watcher, error) {
	ended, err := db.New(tx).EndGroupWatchersResolved(ctx, db.EndGroupWatchersResolvedParams{At: now, GroupID: groupID})
	if err != nil {
		return nil, fmt.Errorf("end the watchers of group %s: %w", groupID, err)
	}

	for _, w := range ended {
		before := w
		before.Status = StatusRunning

		if err := rec.changed(ctx, tx, &before, w); err != nil {
			return nil, fmt.Errorf("group %s: %w", groupID, err)
		}
	}

	return ended, nil
}

// HasRunning reports whether the group with id has a running watcher that
// has not passed its deadline, which keeps the group open after its
// window.
func HasRunning(ctx context.Context, q db.DBTX, groupID uuid.UUID, now time.Time) (bool, error) {
	running, err := db.New(q).GroupHasRunningWatchers(ctx, db.GroupHasRunningWatchersParams{GroupID: groupID, Now: now})
	if err != nil {
		return false, fmt.Errorf("look for running watchers of group %s: %w", groupID, err)
	}

	return running, nil
}

// StoreAlerts reads the alert states of groups from the database, for
// alert checks.
type StoreAlerts struct {
	DB db.DBTX
}

var _ AlertStates = StoreAlerts{}

// AlertStates returns one state per alert of the group: resolved when the
// newest event of the alert is a recovery.
func (s StoreAlerts) AlertStates(ctx context.Context, groupID uuid.UUID) ([]AlertState, error) {
	rows, err := db.New(s.DB).ListGroupAlertStates(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list the alerts of group %s: %w", groupID, err)
	}

	out := make([]AlertState, len(rows))
	for i, r := range rows {
		out[i] = AlertState{Fingerprint: r.Fingerprint, Resolved: r.Resolved}
	}

	return out, nil
}

// TickKey returns the unique key of the watch_tick job of the watcher
// with id.
func TickKey(id uuid.UUID) string {
	return tickKeyPrefix + id.String()
}

// checkCounts returns a *LimitError when the group or the deployment has
// as many watchers as its limit.
func checkCounts(ctx context.Context, queries *db.Queries, p Policy, groupID uuid.UUID, now time.Time) error {
	inGroup, err := queries.CountGroupWatchers(ctx, groupID)
	if err != nil {
		return fmt.Errorf("count the group's watchers: %w", err)
	}

	running, err := queries.CountRunningWatchers(ctx, now)
	if err != nil {
		return fmt.Errorf("count the running watchers: %w", err)
	}

	if limit := countsProblem(p, inGroup, running); limit != nil {
		return limit
	}

	return nil
}

// countsProblem returns a *LimitError when inGroup or running has reached
// its limit.
func countsProblem(p Policy, inGroup, running int64) *LimitError {
	if inGroup >= int64(p.MaxPerGroup) {
		return &LimitError{Limit: limitPerGroup, Max: p.MaxPerGroup, Count: inGroup}
	}

	if running >= int64(p.MaxTotal) {
		return &LimitError{Limit: limitTotal, Max: p.MaxTotal, Count: running}
	}

	return nil
}

// insertParams returns the row of a new watcher started at now.
func insertParams(w NewWatcher, now time.Time) (db.InsertWatcherParams, error) {
	spec, err := json.Marshal(w.Check)
	if err != nil {
		return db.InsertWatcherParams{}, fmt.Errorf("encode the check: %w", err)
	}

	s := Complete(w.Settings)

	passes := s.PassesToRecover
	if w.Check.Type() == TypeAlert {
		passes = alertPassesToRecover
	}

	p := db.InsertWatcherParams{
		ID:              ids.New(),
		GroupID:         w.GroupID,
		Name:            w.Name,
		Origin:          string(w.Origin),
		StartedBySeq:    w.StartedBySeq,
		CheckType:       string(w.Check.Type()),
		Spec:            spec,
		IntervalS:       seconds(s.Interval),
		PassesToRecover: int32(min(passes, maxInt32)), //nolint:gosec // Capped at maxInt32.
		CheckTimeoutS:   seconds(s.CheckTimeout),
		StartedAt:       now,
		Deadline:        now.Add(s.Timeout),
	}

	if w.InvestigationID != uuid.Nil {
		p.InvestigationID = &w.InvestigationID
	}

	return p, nil
}

// enqueueTick adds the watch_tick job of the watcher with id, to run at
// runAt, or at once for the zero time. A job that is pending or running
// for the watcher already counts as added.
func enqueueTick(ctx context.Context, q db.DBTX, id uuid.UUID, runAt time.Time) error {
	payload, err := json.Marshal(tickPayload{WatcherID: id})
	if err != nil {
		return fmt.Errorf("encode the watch_tick payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:      queue.KindWatchTick,
		Payload:   payload,
		UniqueKey: TickKey(id),
		Priority:  TickPriority,
		RunAt:     runAt,
	})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue watch_tick: %w", err)
	}

	return nil
}

// decodeSpec returns the check a watcher stored.
func decodeSpec(w db.Watcher) (Check, error) {
	var (
		check Check
		err   error
	)

	switch Type(w.CheckType) {
	case TypeHTTP:
		var c HTTPCheck
		err = json.Unmarshal(w.Spec, &c)
		check = c
	case TypeTool:
		var c ToolCheck
		err = json.Unmarshal(w.Spec, &c)
		check = c
	case TypeAlert:
		var c AlertCheck
		err = json.Unmarshal(w.Spec, &c)
		check = c
	default:
		return nil, fmt.Errorf("unknown check type %q", w.CheckType)
	}

	if err != nil {
		return nil, fmt.Errorf("decode the %s check: %w", w.CheckType, err)
	}

	return check, nil
}

// decodeTickPayload returns the watcher ID of a watch_tick payload.
func decodeTickPayload(raw []byte) (uuid.UUID, error) {
	var p tickPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return uuid.Nil, fmt.Errorf("decode: %w", err)
	}

	if p.WatcherID == uuid.Nil {
		return uuid.Nil, errNoWatcherID
	}

	return p.WatcherID, nil
}

// maxInt32 is the largest value an int column holds.
const maxInt32 = 1<<31 - 1

// seconds returns d in whole seconds for an int column, at least 1.
func seconds(d time.Duration) int32 {
	return int32(min(max(int64(d/time.Second), 1), maxInt32)) //nolint:gosec // Capped at maxInt32.
}

// deref returns *p, or the zero value for nil.
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}
