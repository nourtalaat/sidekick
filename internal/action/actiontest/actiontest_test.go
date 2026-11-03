package actiontest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mock"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// fakeAction is an action with a fixed Spec.
type fakeAction struct {
	spec action.Spec
}

func (f fakeAction) Spec() action.Spec { return f.spec }

func (fakeAction) Validate(context.Context, json.RawMessage) error { return nil }

func (fakeAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

func TestCheckSnapshotWritesThenChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec action.Spec
	}{
		{
			name: "a written snapshot passes the check and gives the mock the same spec",
			spec: action.Spec{
				Name:         "restart",
				Description:  "Restarts a service.",
				ParamsSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string"}}}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "actions")
			a := fakeAction{spec: tt.spec}

			checkSnapshot(t, true, dir, "vendor.thing", a)
			checkSnapshot(t, false, dir, "vendor.thing", a)

			built, err := mock.New(t.Context(), "restart", mock.Config{Spec: mock.Spec{Snapshot: filepath.Join(dir, "vendor.thing.json")}}, registry.Deps{})
			require.NoError(t, err)
			require.Equal(t, tt.spec.Description, built.Spec().Description)
			require.JSONEq(t, string(tt.spec.ParamsSchema), string(built.Spec().ParamsSchema))
		})
	}
}

func TestRepoRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "the directory with go.mod"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := os.Stat(filepath.Join(repoRoot(t), "go.mod"))
			require.NoError(t, err)
		})
	}
}
