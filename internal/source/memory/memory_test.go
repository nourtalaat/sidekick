package memory

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tool     string
		required []string
	}{
		{name: "search needs a query", tool: toolSearch, required: []string{"query"}},
		{name: "service_notes needs a service", tool: toolServiceNotes, required: []string{"service"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src, err := New(t.Context(), "memory", Config{}, registry.Deps{})
			require.NoError(t, err)

			list, err := src.Tools(t.Context())
			require.NoError(t, err)
			require.Len(t, list, 2)

			for _, tool := range list {
				if tool.Name != tt.tool {
					continue
				}

				require.True(t, tool.ReadOnly, "%s only reads", tool.Name)
				_, err := source.QualifiedName("memory", tool.Name)
				require.NoError(t, err)

				var schema struct {
					Type     string   `json:"type"`
					Required []string `json:"required"`
				}
				require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
				require.Equal(t, "object", schema.Type)
				require.Equal(t, tt.required, schema.Required)

				return
			}

			require.Fail(t, "the tool is missing", tt.tool)
		})
	}
}

func TestCallWithoutDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tool    string
		input   string
		wantErr error
	}{
		{name: "search", tool: toolSearch, input: `{"query":"latency"}`, wantErr: source.ErrUnavailable},
		{name: "service_notes", tool: toolServiceNotes, input: `{"service":"checkout-api"}`, wantErr: source.ErrUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src, err := New(t.Context(), "memory", Config{}, registry.Deps{})
			require.NoError(t, err)

			_, err = src.Call(t.Context(), source.Call{ID: "c1", Tool: tt.tool, Input: json.RawMessage(tt.input)})
			require.ErrorIs(t, err, tt.wantErr)
			require.NoError(t, src.Close())
		})
	}
}

func TestNewChecksSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "the defaults", cfg: Config{}},
		{
			name:    "a negative timeout",
			cfg:     Config{Settings: source.Settings{Timeout: -1}},
			wantErr: "check settings: timeout: must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(t.Context(), "memory", tt.cfg, registry.Deps{})
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}
