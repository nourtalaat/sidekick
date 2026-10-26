package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	token := &registry.SecretRef{Name: "signoz-mcp", Key: "token"}

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "http by default", cfg: Config{URL: "https://mcp.example.com/mcp"}},
		{
			name: "http with headers",
			cfg: Config{
				Transport: TransportHTTP,
				URL:       "http://127.0.0.1:8101/mcp",
				Headers: map[string]Value{
					"Authorization": {Secret: token, Prefix: "Bearer "},
					"X-Org":         {Value: "acme"},
				},
			},
		},
		{name: "sse", cfg: Config{Transport: TransportSSE, URL: "https://mcp.example.com/sse"}},
		{
			name: "stdio with env",
			cfg: Config{
				Transport: TransportStdio,
				Command:   []string{"/usr/local/bin/mcp-git", "--repository", "/work"},
				Env:       map[string]Value{"GIT_TOKEN": {Secret: token}, "LOG_LEVEL": {Value: "info"}},
			},
		},
		{name: "http without url", cfg: Config{}, want: []string{"url: required with transport http"}},
		{name: "sse without url", cfg: Config{Transport: TransportSSE}, want: []string{"url: required with transport sse"}},
		{name: "relative url", cfg: Config{URL: "/mcp"}, want: []string{`url: "/mcp" must be an absolute http or https URL`}},
		{name: "other scheme", cfg: Config{URL: "ftp://mcp.example.com"}, want: []string{`url: "ftp://mcp.example.com" must be an absolute http or https URL`}},
		{
			name: "http with stdio keys",
			cfg:  Config{URL: "https://mcp.example.com/mcp", Command: []string{"x"}, Env: map[string]Value{"A": {Value: "b"}}},
			want: []string{"command: only for transport stdio, not http", "env: only for transport stdio, not http"},
		},
		{name: "stdio without command", cfg: Config{Transport: TransportStdio}, want: []string{"command: required with transport stdio"}},
		{name: "stdio with an empty program", cfg: Config{Transport: TransportStdio, Command: []string{" "}}, want: []string{"command: required with transport stdio"}},
		{
			name: "stdio with http keys",
			cfg: Config{
				Transport: TransportStdio,
				Command:   []string{"x"},
				URL:       "https://mcp.example.com/mcp",
				Headers:   map[string]Value{"X-Org": {Value: "acme"}},
			},
			want: []string{"url: only for transports http and sse, not stdio", "headers: only for transports http and sse, not stdio"},
		},
		{name: "unknown transport", cfg: Config{Transport: "websocket"}, want: []string{`transport: unknown transport "websocket"; use http, sse or stdio`}},
		{
			name: "bad headers",
			cfg: Config{
				URL: "https://mcp.example.com/mcp",
				Headers: map[string]Value{
					"Bad Name":       {Value: "x"},
					"Both":           {Value: "x", Secret: token},
					"Empty":          {},
					"Prefix":         {Value: "x", Prefix: "Bearer "},
					"mcp-session-id": {Value: "x"},
				},
			},
			want: []string{
				`headers.Bad Name: "Bad Name" is not a valid header name`,
				"headers.Both: set one of value and secret, not both",
				"headers.Empty: set value or secret",
				"headers.Prefix.prefix: only with secret; put the whole text in value",
				"headers.mcp-session-id: the MCP transport sets this header itself",
			},
		},
		{
			name: "bad env",
			cfg:  Config{Transport: TransportStdio, Command: []string{"x"}, Env: map[string]Value{"1X": {Value: "y"}, "OK": {}}},
			want: []string{`env.1X: "1X" is not a valid environment variable name`, "env.OK: set value or secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, strings.Join(tt.want, "\n"))
		})
	}
}
