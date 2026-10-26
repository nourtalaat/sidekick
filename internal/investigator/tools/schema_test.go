package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInputSchema(t *testing.T) {
	t.Parallel()

	schema, err := ParseInputSchema(json.RawMessage(`{"type": "object", "required": ["claim"], "properties": {"claim": {"type": "string"}}}`))
	require.NoError(t, err)

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "matching input", input: `{"claim": "disk full"}`, want: `{"claim": "disk full"}`},
		{name: "a missing field", input: `{}`, wantErr: "the input does not match the tool's schema"},
		{name: "not an object", input: `[1]`, wantErr: "the input must be a JSON object"},
		{name: "not JSON", input: `{`, wantErr: "the input is not valid JSON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := schema.Check(json.RawMessage(tt.input))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestParseInputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "empty accepts any object", raw: ``},
		{name: "a schema", raw: `{"type": "object"}`},
		{name: "not JSON", raw: `{`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			schema, err := ParseInputSchema(json.RawMessage(tt.raw))
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)

			got, err := schema.Check(nil)
			require.NoError(t, err)
			require.JSONEq(t, `{}`, string(got))
		})
	}
}
