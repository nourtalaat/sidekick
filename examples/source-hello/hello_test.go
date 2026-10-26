package hello

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		call    source.Call
		want    source.Result
		wantErr error
	}{
		{
			name: "greets by name",
			call: source.Call{Tool: toolGreet, Input: json.RawMessage(`{"name":"Ada"}`)},
			want: source.Result{
				Text:     "Hello, Ada!",
				Evidence: []source.Evidence{{Kind: source.EvidenceOther, Title: "Greetings guide", URL: "https://example.com/greetings"}},
			},
		},
		{
			name: "uses the configured greeting",
			cfg:  Config{Greeting: "Hi"},
			call: source.Call{Tool: toolGreet, Input: json.RawMessage(`{"name":"Ada"}`)},
			want: source.Result{
				Text:     "Hi, Ada!",
				Evidence: []source.Evidence{{Kind: source.EvidenceOther, Title: "Greetings guide", URL: "https://example.com/greetings"}},
			},
		},
		{
			name: "a blank name is no data",
			call: source.Call{Tool: toolGreet, Input: json.RawMessage(`{"name":" "}`)},
			want: source.Result{NoData: true},
		},
		{
			name:    "an unknown tool",
			call:    source.Call{Tool: "wave", Input: json.RawMessage(`{}`)},
			wantErr: errUnknownTool,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New(t.Context(), "hello", tt.cfg, registry.Deps{})
			require.NoError(t, err)

			got, err := s.Call(t.Context(), tt.call)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.NoError(t, s.Close())
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{name: "no greeting", cfg: Config{}},
		{name: "a greeting", cfg: Config{Greeting: "Hi"}},
		{name: "a greeting with spaces", cfg: Config{Greeting: " Hi"}, wantErr: errGreetingSpaces},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tt.cfg.Validate(), tt.wantErr)
		})
	}
}

// TestThroughTheGuard shows what the model sees: the writing tool is left
// out, and a call goes through the guard's checks.
func TestThroughTheGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantIsError bool
	}{
		{name: "a good call", input: `{"name":"Ada"}`},
		{name: "input that fails the schema never reaches the source", input: `{"name":"Ada","age":36}`, wantIsError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New(t.Context(), "hello", Config{}, registry.Deps{})
			require.NoError(t, err)

			r, err := redact.New(redact.Options{HashKey: []byte("example")})
			require.NoError(t, err)

			g, err := tools.New([]tools.Source{{Name: "hello", Source: s}}, tools.Options{Redactor: r})
			require.NoError(t, err)

			run := g.Start(t.Context(), "inv-1", nil)

			specs := run.Tools()
			require.Len(t, specs, 1)
			require.Equal(t, "hello__greet", specs[0].Name)

			res := run.Call(t.Context(), model.ToolCall{ID: "call-1", Name: "hello__greet", Input: json.RawMessage(tt.input)})
			require.Equal(t, tt.wantIsError, res.ToolResult.IsError)
		})
	}
}
