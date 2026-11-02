package proposals

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/nourtalaat/sidekick/internal/action"
)

// monitorSchema is the parameters schema of the fake monitor action.
var monitorSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["threshold"],
  "properties": {
    "threshold": {"type": "integer"},
    "message": {"type": "string"}
  }
}`)

// The fields of the fake monitor that its parameters set, and the one field
// its limits allow.
var (
	thresholdPath = action.Path{"options", "critical"}
	messagePath   = action.Path{"message"}
)

// maxThreshold is the highest threshold the fake monitor's limits allow.
const maxThreshold = 100

// startMonitor is the definition the fake monitor starts with.
const startMonitor = `{"message":"Checkout is slow","options":{"critical":80,"notify_no_data":false},"tags":["team:payments"]}`

// fakeMonitor is an action that changes a monitor of a made-up service in
// place. It keeps the monitor's definition and a version that grows with
// every change, like a service that reports when a monitor was modified.
// Its limits allow the threshold, not the message.
type fakeMonitor struct {
	name string

	mu      sync.Mutex
	live    json.RawMessage
	version int
	writes  int
	// readErr, when set, is what Snapshot returns instead of reading.
	readErr error
	// undoErrs are the errors of the next Undo calls, in order.
	undoErrs []error
	// undoCalls count the calls to Undo.
	undoCalls int
	// executeCalls are the invocations Execute got.
	executeCalls []action.Invocation
}

var (
	_ action.Action      = (*fakeMonitor)(nil)
	_ action.Snapshotter = (*fakeMonitor)(nil)
	_ action.Undoer      = (*fakeMonitor)(nil)
)

func newFakeMonitor(name string) *fakeMonitor {
	return &fakeMonitor{name: name, live: json.RawMessage(startMonitor), version: 1}
}

func (m *fakeMonitor) Spec() action.Spec {
	return action.Spec{Name: m.name, Description: "Changes the threshold of a monitor.", ParamsSchema: monitorSchema}
}

func (m *fakeMonitor) Validate(_ context.Context, params json.RawMessage) error {
	var p monitorParams
	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	if p.Threshold < 1 || p.Threshold > maxThreshold {
		return fmt.Errorf("threshold %d: %w", p.Threshold, action.ErrLimit)
	}

	return nil
}

// monitorParams are the parameters of the fake monitor action.
type monitorParams struct {
	Threshold int     `json:"threshold"`
	Message   *string `json:"message"`
}

func (m *fakeMonitor) Snapshot(_ context.Context, params json.RawMessage) (action.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.readErr != nil {
		return action.Snapshot{}, m.readErr
	}

	var p monitorParams
	if err := json.Unmarshal(params, &p); err != nil {
		return action.Snapshot{}, fmt.Errorf("decode: %w", err)
	}

	edits := []action.Edit{{Path: thresholdPath, Value: json.RawMessage(strconv.Itoa(p.Threshold))}}

	if p.Message != nil {
		raw, err := json.Marshal(*p.Message)
		if err != nil {
			return action.Snapshot{}, fmt.Errorf("encode: %w", err)
		}

		edits = append(edits, action.Edit{Path: messagePath, Value: raw})
	}

	after, err := action.Apply(m.live, edits)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("apply: %w", err)
	}

	deltas, err := action.Diff(m.live, after)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("diff: %w", err)
	}

	if err := action.CheckFields([]action.Path{thresholdPath}, deltas); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the fields: %w", err)
	}

	return action.Snapshot{Target: "fake:monitor/1", Before: m.live, After: after, Version: strconv.Itoa(m.version)}, nil
}

func (m *fakeMonitor) Execute(_ context.Context, inv action.Invocation) (action.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.executeCalls = append(m.executeCalls, inv)

	if inv.Snapshot == nil {
		return action.Result{}, fmt.Errorf("no snapshot: %w", action.ErrPermanent)
	}

	deltas, err := action.Diff(inv.Snapshot.Before, inv.Snapshot.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("diff: %w", err)
	}

	done, err := action.AtAfter(m.live, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare: %w", err)
	}

	if done {
		return m.result("Changed the monitor."), nil
	}

	unchanged, err := action.AtBefore(m.live, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare: %w", err)
	}

	if !unchanged || strconv.Itoa(m.version) != inv.Snapshot.Version {
		return action.Result{}, fmt.Errorf("monitor 1: %w", action.ErrConflict)
	}

	m.live, m.version, m.writes = inv.Snapshot.After, m.version+1, m.writes+1

	return m.result("Changed the monitor."), nil
}

func (m *fakeMonitor) Undo(_ context.Context, _ action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.undoCalls++

	if len(m.undoErrs) > 0 {
		var err error

		err, m.undoErrs = m.undoErrs[0], m.undoErrs[1:]
		if err != nil {
			return action.Result{}, err
		}
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("diff: %w", err)
	}

	restored, err := action.AtBefore(m.live, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare: %w", err)
	}

	if restored {
		return m.result("Restored the monitor."), nil
	}

	asWritten, err := action.AtAfter(m.live, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare: %w", err)
	}

	if !asWritten && !force {
		return action.Result{}, &action.ConflictError{Live: m.live}
	}

	m.live, err = action.Revert(m.live, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("revert: %w", err)
	}

	m.version++
	m.writes++

	return m.result("Restored the monitor."), nil
}

// result is the result of a change, linking to the monitor.
func (m *fakeMonitor) result(summary string) action.Result {
	return action.Result{Summary: summary, Links: []action.Link{{Title: "Monitor 1", URL: "https://monitors.example.com/1"}}}
}

// definition returns the live definition.
func (m *fakeMonitor) definition() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return string(m.live)
}

// edit is someone else changing the live definition.
func (m *fakeMonitor) edit(definition string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.live, m.version = json.RawMessage(definition), m.version+1
}

// writeCount returns how many times the action changed the monitor.
func (m *fakeMonitor) writeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.writes
}
