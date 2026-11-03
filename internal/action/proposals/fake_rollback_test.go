package proposals

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
)

// rollbackSchema is the parameters schema of the fake rollback action.
var rollbackSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["back"],
  "properties": {"back": {"type": "integer"}}
}`)

// fakeRevisionKind is what the fake platform calls a revision.
const fakeRevisionKind = "ReplicaSet revision"

// fakeStartRevision is the revision the fake workload runs at first. The
// older revisions are 6, 5 and so on down to 1.
const fakeStartRevision = 7

// hiddenRevision is the revision whose version holds a zero-width space, a
// character that hides inside text.
const hiddenRevision = 5

// fakeDeployedAt is when the fake platform says its odd revisions were
// deployed. It does not say for the even ones, nor who deployed them.
var fakeDeployedAt = time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)

// fakeRollback is an action that rolls a made-up workload back some
// revisions, as the rollback actions do: it limits how far back, snapshots
// two revision documents, and writes the revision the approver read. It
// keeps the number of the revision that runs.
type fakeRollback struct {
	action.RevisionLimit

	name string

	mu      sync.Mutex
	running int
	// undoCalls count the calls to Undo.
	undoCalls int
}

var (
	_ action.Action      = (*fakeRollback)(nil)
	_ action.Snapshotter = (*fakeRollback)(nil)
	_ action.Undoer      = (*fakeRollback)(nil)
)

func newFakeRollback(name string, maxBack *int) *fakeRollback {
	return &fakeRollback{RevisionLimit: action.RevisionLimit{MaxRevisionsBack: maxBack}, name: name, running: fakeStartRevision}
}

func (r *fakeRollback) Spec() action.Spec {
	return action.Spec{Name: r.name, Description: "Rolls the checkout workload back.", ParamsSchema: rollbackSchema}
}

func (r *fakeRollback) Validate(_ context.Context, params json.RawMessage) error {
	back, err := backOf(params)
	if err != nil {
		return fmt.Errorf("read the parameters: %w", err)
	}

	if err := r.CheckRequest(back); err != nil {
		return fmt.Errorf("check the request: %w", err)
	}

	return nil
}

func (r *fakeRollback) Snapshot(_ context.Context, params json.RawMessage) (action.Snapshot, error) {
	back, err := backOf(params)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the parameters: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	target := r.running - back
	if target < 1 {
		return action.Snapshot{}, fmt.Errorf("revision %d does not exist: %w", target, action.ErrLimit)
	}

	snap, err := action.RevisionSnapshot("fake:workload/checkout", strconv.Itoa(r.running), fakeRevision(r.running), fakeRevision(target))
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

func (r *fakeRollback) Execute(_ context.Context, inv action.Invocation) (action.Result, error) {
	if inv.Snapshot == nil {
		return action.Result{}, fmt.Errorf("no snapshot: %w", action.ErrPermanent)
	}

	revisions, err := inv.Snapshot.Revisions()
	if err != nil || revisions == nil {
		return action.Result{}, fmt.Errorf("read the revisions: %w", action.ErrPermanent)
	}

	return r.switchTo(inv.Snapshot.Version, revisions.GoesBackTo)
}

func (r *fakeRollback) Undo(_ context.Context, _ action.Invocation, snap action.Snapshot, _ bool) (action.Result, error) {
	r.mu.Lock()
	r.undoCalls++
	r.mu.Unlock()

	revisions, err := snap.Revisions()
	if err != nil || revisions == nil {
		return action.Result{}, fmt.Errorf("read the revisions: %w", action.ErrPermanent)
	}

	return r.switchTo(revisions.GoesBackTo.ID, revisions.Running)
}

// switchTo makes to the revision that runs, when the one that runs is the
// one in expected, and fails with action.ErrConflict when it is not.
func (r *fakeRollback) switchTo(expected string, to action.Revision) (action.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, err := strconv.Atoi(to.ID)
	if err != nil {
		return action.Result{}, fmt.Errorf("revision %q: %w", to.ID, action.ErrPermanent)
	}

	if strconv.Itoa(r.running) != expected {
		return action.Result{}, fmt.Errorf("revision %d runs now: %w", r.running, action.ErrConflict)
	}

	r.running = id

	return action.Result{Summary: "Rolled back to revision " + to.ID + "."}, nil
}

// runningRevision is the number of the revision that runs.
func (r *fakeRollback) runningRevision() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.running
}

// fakeRevision is revision n of the fake workload.
func fakeRevision(n int) action.Revision {
	rev := action.Revision{Kind: fakeRevisionKind, ID: strconv.Itoa(n), Version: "checkout:1.4." + strconv.Itoa(n)}

	if n == hiddenRevision {
		rev.Version += string(rune(0x200b))
	}

	if n%2 == 1 {
		at := fakeDeployedAt.Add(time.Duration(n) * time.Hour)
		rev.DeployedAt, rev.DeployedBy = &at, "ana"
	}

	return rev
}

// backOf reads how many revisions back the parameters ask for.
func backOf(params json.RawMessage) (int, error) {
	var p struct {
		Back int `json:"back"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return 0, fmt.Errorf("decode the parameters: %w", err)
	}

	return p.Back, nil
}
