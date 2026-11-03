//go:build integration

package proposals

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nourtalaat/sidekick/internal/action"
)

// muteSchema is the parameters schema of the fake mute action.
var muteSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["service"],
  "properties": {"service": {"type": "string"}}
}`)

// fakeMute is an action that creates something that did not exist, a mute
// of a made-up service, and ends it again on Undo. Its snapshot has no
// Before. It keeps the live mute, or nothing.
type fakeMute struct {
	name string

	mu   sync.Mutex
	live json.RawMessage
}

var (
	_ action.Action      = (*fakeMute)(nil)
	_ action.Snapshotter = (*fakeMute)(nil)
	_ action.Undoer      = (*fakeMute)(nil)
)

func newFakeMute(name string) *fakeMute {
	return &fakeMute{name: name}
}

func (m *fakeMute) Spec() action.Spec {
	return action.Spec{Name: m.name, Description: "Mutes a service.", ParamsSchema: muteSchema}
}

func (m *fakeMute) Validate(_ context.Context, params json.RawMessage) error {
	var p struct {
		Service string `json:"service"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	if p.Service == "" {
		return fmt.Errorf("no service: %w", action.ErrLimit)
	}

	return nil
}

func (m *fakeMute) Snapshot(_ context.Context, params json.RawMessage) (action.Snapshot, error) {
	var p struct {
		Service string `json:"service"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return action.Snapshot{}, fmt.Errorf("decode: %w", err)
	}

	return action.Snapshot{Target: "fake:mute", After: json.RawMessage(fmt.Sprintf(`{"service":%q,"state":"active"}`, p.Service))}, nil
}

func (m *fakeMute) Execute(_ context.Context, inv action.Invocation) (action.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inv.Snapshot == nil {
		return action.Result{}, fmt.Errorf("no snapshot: %w", action.ErrPermanent)
	}

	m.live = inv.Snapshot.After

	return action.Result{Summary: "Muted."}, nil
}

func (m *fakeMute) Undo(_ context.Context, _ action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.live == nil {
		return action.Result{Summary: "Already ended."}, nil
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return action.Result{}, fmt.Errorf("diff: %w", err)
	}

	asWritten, err := action.AtAfter(m.live, deltas)
	if err != nil {
		return action.Result{}, fmt.Errorf("compare: %w", err)
	}

	if !asWritten && !force {
		return action.Result{}, &action.ConflictError{Live: m.live}
	}

	m.live = nil

	return action.Result{Summary: "Ended the mute."}, nil
}

// definition returns the live mute, or "" when there is none.
func (m *fakeMute) definition() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return string(m.live)
}

// edit is someone else changing the live mute.
func (m *fakeMute) edit(definition string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.live = json.RawMessage(definition)
}
