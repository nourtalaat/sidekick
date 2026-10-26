package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// stdioServerEnv makes the test binary run as a stdio MCP server instead
// of the tests, for the stdio transport test.
const stdioServerEnv = "SIDEKICK_TEST_MCP_STDIO_SERVER"

// waitFor is how long tests wait for the background connection.
const waitFor = 10 * time.Second

// pollEvery is how often tests check the background connection.
const pollEvery = 10 * time.Millisecond

// objectSchema is the input schema of the test tools.
var objectSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)

func TestMain(m *testing.M) {
	if os.Getenv(stdioServerEnv) == "1" {
		if err := newMCPServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

// newMCPServer returns a server with an echo tool, for the stdio test.
func newMCPServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	addTool(s, "echo", nil, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(string(req.Params.Arguments)), nil
	})

	return s
}

// serveMCP serves server over streamable HTTP at /mcp of a new fake server.
func serveMCP(t *testing.T, server *mcp.Server) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		api.HandleFunc(method, "/mcp", h.ServeHTTP)
	}

	return api
}

// addTool adds a tool with objectSchema and the given annotations.
func addTool(s *mcp.Server, name string, annotations *mcp.ToolAnnotations, h mcp.ToolHandler) {
	s.AddTool(&mcp.Tool{Name: name, Description: "The " + name + " tool.", InputSchema: objectSchema, Annotations: annotations}, h)
}

// text returns a result with one text content.
func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// newSource builds a source from cfg with the fake server's client and the
// given secrets, closes it when the test ends, and returns it.
func newSource(t *testing.T, cfg Config, client *http.Client, s registry.Secrets) *Source {
	t.Helper()

	src, err := New(t.Context(), "test", cfg, registry.Deps{HTTP: client, Secrets: s, Logger: discardLogger()})
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, src.Close()) })

	return src.(*Source)
}

// connected builds a source for the streamable HTTP server api and waits
// until it is connected.
func connected(t *testing.T, api *fakehttp.Server, cfg Config) *Source {
	t.Helper()

	cfg.URL = api.URL() + "/mcp"
	src := newSource(t, cfg, api.Client(), nil)
	require.Eventually(t, src.Up, waitFor, pollEvery)

	return src
}

// discardLogger returns a logger that writes nothing.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSecrets resolves every reference to its name and key, joined.
type fakeSecrets struct{}

func (fakeSecrets) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	return []byte(ref.Name + "-" + ref.Key + "\n"), nil
}

// toolNames returns the names of tools.
func toolNames(tools []source.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}

	return names
}
