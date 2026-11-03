package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Type is the action type this package registers.
const Type = "mock"

// snapshotIndent is the indent of a spec snapshot file.
const snapshotIndent = "  "

// ErrNeverExecuted is returned by Execute: a benchmark runs nothing.
var ErrNeverExecuted = errors.New("a mock action never executes")

func init() {
	registry.Actions.Register(Type, registry.Typed(New))
}

// Action is a mock action. It is safe for concurrent use.
type Action struct {
	spec   action.Spec
	schema *tools.InputSchema
}

var _ action.Action = (*Action)(nil)

// snapshot is the content of a spec snapshot file.
type snapshot struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	ParamsSchema json.RawMessage `json:"paramsSchema"`
}

// Spec returns the snapshot's description and schema under the action's
// name in the scenario, which is the name the model proposes it by.
func (a *Action) Spec() action.Spec {
	return a.spec
}

// Validate checks the parameters against the snapshot's schema.
func (a *Action) Validate(_ context.Context, params json.RawMessage) error {
	if _, err := a.schema.Check(params); err != nil {
		return fmt.Errorf("params: %w", err)
	}

	return nil
}

// Execute fails: a mock action never runs.
func (a *Action) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, fmt.Errorf("%w: %w", ErrNeverExecuted, action.ErrPermanent)
}

// SnapshotOf returns the content of the spec snapshot file of spec, the
// Spec of a real action: indented with two spaces, ending in a newline.
func SnapshotOf(spec action.Spec) ([]byte, error) {
	var out bytes.Buffer

	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", snapshotIndent)

	if err := enc.Encode(snapshot{Name: spec.Name, Description: spec.Description, ParamsSchema: spec.ParamsSchema}); err != nil {
		return nil, fmt.Errorf("encode the spec snapshot of %s: %w", spec.Name, err)
	}

	return out.Bytes(), nil
}

// New builds a mock action called name from cfg, reading its spec
// snapshot.
func New(_ context.Context, name string, cfg Config, _ registry.Deps) (action.Action, error) {
	snap, err := readSnapshot(cfg.Spec.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("spec.snapshot: %w", err)
	}

	schema, err := tools.ParseInputSchema(snap.ParamsSchema)
	if err != nil {
		return nil, fmt.Errorf("spec.snapshot: paramsSchema: %w", err)
	}

	return &Action{
		spec:   action.Spec{Name: name, Description: snap.Description, ParamsSchema: snap.ParamsSchema},
		schema: schema,
	}, nil
}

// readSnapshot reads the spec snapshot file at path and checks that it
// has a description and a schema.
func readSnapshot(path string) (snapshot, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // The scenario author chooses the snapshot file.
	if err != nil {
		return snapshot{}, fmt.Errorf("read %s: %w", path, err)
	}

	var snap snapshot

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&snap); err != nil {
		return snapshot{}, fmt.Errorf("decode %s: %w", path, err)
	}

	switch {
	case snap.Description == "":
		return snapshot{}, fmt.Errorf("%s: description: required", path)
	case len(snap.ParamsSchema) == 0:
		return snapshot{}, fmt.Errorf("%s: paramsSchema: required", path)
	}

	return snap, nil
}
