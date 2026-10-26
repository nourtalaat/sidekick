package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestSourceCall(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", maxResultSize+1000)

	tests := []struct {
		name      string
		handler   mcp.ToolHandler
		block     bool
		timeout   time.Duration
		input     json.RawMessage
		want      source.Result
		wantTool  string
		wantErrIs error
	}{
		{
			name: "text result",
			handler: func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return text("got " + string(req.Params.Arguments)), nil
			},
			input: json.RawMessage(`{"query":"errors"}`),
			want:  source.Result{Text: `got {"query":"errors"}`},
		},
		{
			name: "structured result",
			handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content:           []mcp.Content{&mcp.TextContent{Text: "2 rows"}},
					StructuredContent: map[string]any{"rows": 2},
				}, nil
			},
			want: source.Result{Text: "2 rows", Structured: json.RawMessage(`{"rows":2}`)},
		},
		{
			name: "empty result is no data",
			handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
			want: source.Result{NoData: true},
		},
		{
			name: "isError becomes a tool error",
			handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				res := text("bad query: unknown field")
				res.IsError = true

				return res, nil
			},
			wantTool: "bad query: unknown field",
		},
		{
			name:      "a slow tool times out",
			block:     true,
			timeout:   50 * time.Millisecond,
			wantErrIs: context.DeadlineExceeded,
		},
		{
			name: "an oversized result is cut",
			handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return text(big), nil
			},
			want: source.Result{Text: big[:maxResultSize], Truncated: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := tt.handler
			release := make(chan struct{})

			if tt.block {
				handler = func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					<-release

					return text("too late"), nil
				}
			}

			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			addTool(server, "search", nil, handler)

			cfg := Config{}
			cfg.Timeout = tt.timeout
			src := connected(t, serveMCP(t, server), cfg)

			t.Cleanup(func() { close(release) })

			got, err := src.Call(t.Context(), source.Call{ID: "call-1", Tool: "search", Input: tt.input})

			switch {
			case tt.wantErrIs != nil:
				require.ErrorIs(t, err, tt.wantErrIs)
			case tt.wantTool != "":
				var terr *source.ToolError
				require.ErrorAs(t, err, &terr)
				require.Equal(t, tt.wantTool, terr.Message)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSourceTools(t *testing.T) {
	t.Parallel()

	no := false
	yes := true

	tests := []struct {
		name         string
		trust        *bool
		wantReadOnly map[string]bool
	}{
		{
			name:         "annotations decide by default",
			wantReadOnly: map[string]bool{"plain": true, "read": true, "write": false, "destroy": false, "unmarked": false},
		},
		{
			name:         "annotations trusted explicitly",
			trust:        &yes,
			wantReadOnly: map[string]bool{"plain": true, "read": true, "write": false, "destroy": false, "unmarked": false},
		},
		{
			name:         "annotations not trusted",
			trust:        &no,
			wantReadOnly: map[string]bool{"plain": true, "read": true, "write": true, "destroy": true, "unmarked": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			noop := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return text(""), nil }
			addTool(server, "plain", nil, noop)
			addTool(server, "read", &mcp.ToolAnnotations{ReadOnlyHint: true}, noop)
			addTool(server, "write", &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &no}, noop)
			addTool(server, "destroy", &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &yes}, noop)
			addTool(server, "unmarked", &mcp.ToolAnnotations{Title: "Unmarked"}, noop)

			cfg := Config{}
			cfg.Tools.TrustAnnotations = tt.trust
			src := connected(t, serveMCP(t, server), cfg)

			tools, err := src.Tools(t.Context())
			require.NoError(t, err)

			got := map[string]bool{}
			for _, tool := range tools {
				got[tool.Name] = tool.ReadOnly

				require.Equal(t, "The "+tool.Name+" tool.", tool.Description)
				require.JSONEq(t, string(objectSchema), string(tool.InputSchema))
			}

			require.Equal(t, tt.wantReadOnly, got)
		})
	}
}

func TestSourceToolListChanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		add  []string
		want []string
	}{
		{name: "a new tool appears", add: []string{"metrics"}, want: []string{"logs", "metrics"}},
		{name: "two new tools appear", add: []string{"metrics", "traces"}, want: []string{"logs", "metrics", "traces"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			noop := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return text(""), nil }
			addTool(server, "logs", nil, noop)

			src := connected(t, serveMCP(t, server), Config{})

			for _, name := range tt.add {
				addTool(server, name, nil, noop)
			}

			require.Eventually(t, func() bool {
				tools, err := src.Tools(t.Context())

				return err == nil && len(tools) == len(tt.want)
			}, waitFor, pollEvery)

			tools, err := src.Tools(t.Context())
			require.NoError(t, err)
			require.ElementsMatch(t, tt.want, toolNames(tools))
		})
	}
}

func TestSourceUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		recover bool
	}{
		{name: "a server that is down"},
		{name: "a server that comes back", recover: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var up atomic.Bool

			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			addTool(server, "logs", nil, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return text("ok"), nil })
			h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

			api := fakehttp.New(t)
			for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
				api.HandleFunc(method, "/mcp", func(w http.ResponseWriter, req *http.Request) {
					if !up.Load() {
						http.Error(w, "down", http.StatusServiceUnavailable)

						return
					}

					h.ServeHTTP(w, req)
				})
			}

			start := time.Now()
			src := newSource(t, Config{URL: api.URL() + "/mcp"}, api.Client(), nil)
			require.Less(t, time.Since(start), time.Second, "New must not wait for the server")
			require.False(t, src.Up())

			_, err := src.Tools(t.Context())
			require.ErrorIs(t, err, source.ErrUnavailable)

			_, err = src.Call(t.Context(), source.Call{Tool: "logs"})
			require.ErrorIs(t, err, source.ErrUnavailable)

			if !tt.recover {
				return
			}

			up.Store(true)
			require.Eventually(t, src.Up, waitFor, pollEvery)

			got, err := src.Call(t.Context(), source.Call{Tool: "logs"})
			require.NoError(t, err)
			require.Equal(t, "ok", got.Text)
		})
	}
}

func TestSourceReconnects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a session the server ends is opened again"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			addTool(server, "logs", nil, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return text("ok"), nil })

			src := connected(t, serveMCP(t, server), Config{})
			first := src.current()

			for session := range server.Sessions() {
				require.NoError(t, session.Close())
			}

			require.Eventually(t, func() bool {
				current := src.current()

				return current != nil && current != first
			}, waitFor, pollEvery, tt.name)

			got, err := src.Call(t.Context(), source.Call{Tool: "logs"})
			require.NoError(t, err)
			require.Equal(t, "ok", got.Text)
		})
	}
}

func TestSourceHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]Value
		want    map[string]string
	}{
		{
			name:    "secret with prefix",
			headers: map[string]Value{"Authorization": {Secret: &registry.SecretRef{Name: "mcp", Key: "token"}, Prefix: "Bearer "}},
			want:    map[string]string{"Authorization": "Bearer mcp-token"},
		},
		{
			name:    "plain value",
			headers: map[string]Value{"X-Org": {Value: "acme"}},
			want:    map[string]string{"X-Org": "acme"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			api := serveMCP(t, server)

			src := newSource(t, Config{URL: api.URL() + "/mcp", Headers: tt.headers}, api.Client(), fakeSecrets{})
			require.Eventually(t, src.Up, waitFor, pollEvery)

			reqs := api.Requests()
			require.NotEmpty(t, reqs)

			for _, req := range reqs {
				for name, want := range tt.want {
					require.Equal(t, want, req.Header.Get(name))
				}
			}
		})
	}
}

func TestSourceTransports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T) (Config, *http.Client)
	}{
		{
			name: "sse",
			setup: func(t *testing.T) (Config, *http.Client) {
				t.Helper()

				api := fakehttp.New(t)
				h := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return newMCPServer() }, nil)
				api.HandleFunc(http.MethodGet, "/sse", h.ServeHTTP)
				api.HandleFunc(http.MethodPost, "/sse", h.ServeHTTP)

				return Config{Transport: TransportSSE, URL: api.URL() + "/sse"}, api.Client()
			},
		},
		{
			name: "stdio",
			setup: func(t *testing.T) (Config, *http.Client) {
				t.Helper()

				exe, err := os.Executable()
				require.NoError(t, err)

				return Config{
					Transport: TransportStdio,
					Command:   []string{exe, "-test.run=^$"},
					Env:       map[string]Value{stdioServerEnv: {Value: "1"}},
				}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, client := tt.setup(t)
			src := newSource(t, cfg, client, nil)
			require.Eventually(t, src.Up, waitFor, pollEvery)

			tools, err := src.Tools(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{"echo"}, toolNames(tools))

			got, err := src.Call(t.Context(), source.Call{Tool: "echo", Input: json.RawMessage(`{"query":"hi"}`)})
			require.NoError(t, err)
			require.JSONEq(t, `{"query":"hi"}`, got.Text)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	token := &registry.SecretRef{Name: "mcp", Key: "token"}

	tests := []struct {
		name    string
		cfg     Config
		deps    registry.Deps
		wantErr error
	}{
		{name: "http without a client uses the shared default", cfg: Config{URL: "http://127.0.0.1:1/mcp"}},
		{
			name:    "secret without a resolver",
			cfg:     Config{URL: "http://127.0.0.1:1/mcp", Headers: map[string]Value{"Authorization": {Secret: token}}},
			deps:    registry.Deps{HTTP: &http.Client{}},
			wantErr: errNoSecrets,
		},
		{
			name: "bad rename",
			cfg: Config{
				URL:      "http://127.0.0.1:1/mcp",
				Settings: source.Settings{Tools: source.ToolSettings{Rename: map[string]string{"a": "b c"}}},
			},
			deps:    registry.Deps{HTTP: &http.Client{}},
			wantErr: source.ErrBadName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src, err := New(t.Context(), "test", tt.cfg, tt.deps)
			if tt.wantErr == nil {
				require.NoError(t, err)
				require.NoError(t, src.Close())

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
