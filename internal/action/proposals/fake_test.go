package proposals

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nourtalaat/sidekick/internal/action"
)

// noteSchema is the parameters schema of the fake note action.
var noteSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["board", "title"],
  "properties": {
    "board": {"type": "string"},
    "title": {"type": "string", "maxLength": 40},
    "body": {"type": "string"}
  }
}`)

// allowedBoard is the one board the fake note action allows.
const allowedBoard = "ops"

// fakeNote is an action that writes notes to a made-up service. It keeps
// the effects it made by idempotency key, like a service that honors the
// key, and what it was called with.
type fakeNote struct {
	name string

	mu      sync.Mutex
	effects map[string]int
	calls   []action.Invocation
	// executeErrs are the errors of the next Execute calls, in order; a
	// nil entry means success. When it is empty, Execute succeeds.
	executeErrs []error
	// afterEffect, when set, runs after Execute made its effect and before
	// it returns, for example to simulate a crash.
	afterEffect func() error
	// validateErr, when set, is what Validate returns after its own checks.
	validateErr error
}

var _ action.Action = (*fakeNote)(nil)

func newFakeNote(name string) *fakeNote {
	return &fakeNote{name: name, effects: map[string]int{}}
}

func (n *fakeNote) Spec() action.Spec {
	return action.Spec{Name: n.name, Description: "Writes a note to a board.", ParamsSchema: noteSchema}
}

func (n *fakeNote) Validate(_ context.Context, params json.RawMessage) error {
	var p struct {
		Board string `json:"board"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	if p.Board != allowedBoard {
		return fmt.Errorf("board %q: %w", p.Board, action.ErrLimit)
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	return n.validateErr
}

func (n *fakeNote) Execute(_ context.Context, inv action.Invocation) (action.Result, error) {
	n.mu.Lock()
	n.calls = append(n.calls, inv)

	var err error
	if len(n.executeErrs) > 0 {
		err, n.executeErrs = n.executeErrs[0], n.executeErrs[1:]
	}

	if err == nil || n.afterEffect != nil {
		n.effects[inv.IdempotencyKey] = 1
	}

	after := n.afterEffect
	n.mu.Unlock()

	if err != nil {
		return action.Result{}, err
	}

	if after != nil {
		if err := after(); err != nil {
			return action.Result{}, err
		}
	}

	return action.Result{Summary: "Wrote a note.", Links: []action.Link{{Title: "Note", URL: "https://notes.example.com/n/" + inv.ProposalID}}}, nil
}

// effectCount returns how many effects the action made in all, counting
// each idempotency key once.
func (n *fakeNote) effectCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()

	return len(n.effects)
}

// callList returns the invocations Execute got.
func (n *fakeNote) callList() []action.Invocation {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]action.Invocation(nil), n.calls...)
}
