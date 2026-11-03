package mock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// specFixture is the snapshot of a restart-service action.
const specFixture = "fixture/spec.json"

// newAction builds the action called name from the snapshot at path.
func newAction(t *testing.T, name, path string) action.Action {
	t.Helper()

	a, err := New(t.Context(), name, Config{Spec: Spec{Snapshot: path}}, registry.Deps{})
	require.NoError(t, err)

	return a
}

func TestSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want action.Spec
	}{
		{
			name: "the name is the scenario's, the rest comes from the snapshot",
			want: action.Spec{
				Name:        "restart",
				Description: "Restarts a service. Allowed services: checkout-api, ledger.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newAction(t, "restart", specFixture).Spec()
			require.Equal(t, tt.want.Name, got.Name)
			require.Equal(t, tt.want.Description, got.Description)
			require.JSONEq(t, `{
				"type": "object",
				"additionalProperties": false,
				"required": ["service"],
				"properties": {
					"service": {"type": "string", "enum": ["checkout-api", "ledger"]},
					"graceSeconds": {"type": "integer", "minimum": 0, "maximum": 60}
				}
			}`, string(got.ParamsSchema))
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		wantErr string
	}{
		{name: "parameters that fit the schema", params: `{"service": "ledger", "graceSeconds": 10}`},
		{name: "a required parameter is missing", params: `{"graceSeconds": 10}`, wantErr: "service"},
		{name: "a value outside the enum", params: `{"service": "billing"}`, wantErr: "billing"},
		{name: "a parameter the schema does not have", params: `{"service": "ledger", "force": true}`, wantErr: "force"},
		{name: "a value of the wrong type", params: `{"service": "ledger", "graceSeconds": "ten"}`, wantErr: "graceSeconds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := newAction(t, "restart", specFixture).Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestExecuteNeverRuns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "an approved invocation fails and is not retried"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := newAction(t, "restart", specFixture).Execute(t.Context(), action.Invocation{
				Params: json.RawMessage(`{"service": "ledger"}`), ApprovedBy: "u1",
			})
			require.ErrorIs(t, err, ErrNeverExecuted)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Equal(t, action.Result{}, res)
		})
	}
}

func TestNewRefusesBadSnapshots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "no snapshot path", cfg: Config{}, wantErr: "spec.snapshot: required"},
		{name: "a file that is not there", cfg: Config{Spec: Spec{Snapshot: "fixture/missing.json"}}, wantErr: "missing.json"},
		{name: "no description", cfg: Config{Spec: Spec{Snapshot: "fixture/no-description.json"}}, wantErr: "description: required"},
		{name: "no schema", cfg: Config{Spec: Spec{Snapshot: "fixture/no-schema.json"}}, wantErr: "paramsSchema: required"},
		{name: "a key the format does not have", cfg: Config{Spec: Spec{Snapshot: "fixture/unknown-key.json"}}, wantErr: "extra"},
		{name: "a schema that does not resolve", cfg: Config{Spec: Spec{Snapshot: "fixture/bad-schema.json"}}, wantErr: "paramsSchema"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(t.Context(), "restart", tt.cfg, registry.Deps{})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{name: "a config in a scenario's form", yaml: "spec: {snapshot: " + specFixture + "}"},
		{name: "no snapshot", yaml: "spec: {}", wantErr: "spec.snapshot: required"},
		{name: "an unknown key", yaml: "spec: {snapshot: " + specFixture + "}\nlimits: {}", wantErr: "limits"},
	}

	require.Contains(t, registry.Actions.Types(), Type)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &node))

			built, err := registry.Actions.Build(t.Context(), Type, "restart", node.Content[0], registry.Deps{})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, "restart", built.Spec().Name)

			_, isSnapshotter := built.(action.Snapshotter)
			require.False(t, isSnapshotter, "a benchmark proposal has no snapshot")
		})
	}
}

func TestSnapshotOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec action.Spec
		want string
	}{
		{
			name: "indented, with the schema as it is and no HTML escaping",
			spec: action.Spec{
				Name:         "restart-service",
				Description:  "Restarts <a service> & waits.",
				ParamsSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string"}}}`),
			},
			want: `{
  "name": "restart-service",
  "description": "Restarts <a service> & waits.",
  "paramsSchema": {
    "type": "object",
    "properties": {
      "service": {
        "type": "string"
      }
    }
  }
}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := SnapshotOf(tt.spec)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))

			path := filepath.Join(t.TempDir(), "spec.json")
			require.NoError(t, os.WriteFile(path, got, 0o600))

			back := newAction(t, tt.spec.Name, path).Spec()
			require.Equal(t, tt.spec.Description, back.Description)
			require.JSONEq(t, string(tt.spec.ParamsSchema), string(back.ParamsSchema))
		})
	}
}
