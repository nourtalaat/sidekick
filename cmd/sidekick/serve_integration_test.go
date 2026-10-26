//go:build integration

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// serveStartTimeout bounds how long the test waits for serve to start.
const serveStartTimeout = time.Minute

// serveConfig is validConfig listening on ports the system picks.
const serveConfig = validConfig + `
server: { listen: "127.0.0.1:0", metricsListen: "127.0.0.1:0" }
`

// logLine holds the fields of a log line that the test reads.
type logLine struct {
	Msg           string `json:"msg"`
	MetricsListen string `json:"metrics_listen"`
}

// TestServeStopsOnSignal runs `sidekick serve` as its own process, waits
// until it serves, sends it a signal and checks that it exits cleanly
// within the grace period.
func TestServeStopsOnSignal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		signal os.Signal
	}{
		{name: "SIGTERM", signal: syscall.SIGTERM},
		{name: "SIGINT", signal: os.Interrupt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			configPath := writeConfig(t, serveConfig, storetest.NewURL(t))

			//nolint:gosec // Runs this test binary again, with fixed arguments.
			cmd := exec.CommandContext(t.Context(), os.Args[0], "serve", "--config", configPath)
			cmd.Env = append(os.Environ(), runMainEnv+"=1")
			cmd.Stderr = os.Stderr

			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, cmd.Start())

			lines := readLogLines(stdout)
			started := waitForLog(t, lines, "sidekick started")

			resp, err := http.Get("http://" + started.MetricsListen + "/healthz") //nolint:noctx // A plain request to a local server.
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusOK, resp.StatusCode)

			signalled := time.Now()

			require.NoError(t, cmd.Process.Signal(tt.signal))
			waitForLog(t, lines, "sidekick stopped")

			// Wait must come after every read from stdout.
			for range lines {
			}

			require.NoError(t, cmd.Wait(), "serve exits with status 0")
			require.Less(t, time.Since(signalled), app.ShutdownGrace)
		})
	}
}

// TestServeWithSourceDown starts `sidekick serve` with an MCP source whose
// server is down, and checks that serve starts and is ready anyway while
// the source keeps trying to connect in the background.
func TestServeWithSourceDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler func(release <-chan struct{}) http.HandlerFunc
	}{
		{
			name: "the server answers with an error",
			handler: func(<-chan struct{}) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
				}
			},
		},
		{
			name: "the server never answers",
			handler: func(release <-chan struct{}) http.HandlerFunc {
				return func(_ http.ResponseWriter, req *http.Request) {
					select {
					case <-req.Context().Done():
					case <-release:
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mcpServer := fakehttp.New(t)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })

			for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
				mcpServer.HandleFunc(method, "/mcp", tt.handler(release))
			}

			config := serveConfig + "sources:\n  signoz: { type: mcp, url: \"" + mcpServer.URL() + "/mcp\" }\n"
			configPath := writeConfig(t, config, storetest.NewURL(t))

			//nolint:gosec // Runs this test binary again, with fixed arguments.
			cmd := exec.CommandContext(t.Context(), os.Args[0], "serve", "--config", configPath)
			cmd.Env = append(os.Environ(), runMainEnv+"=1")
			cmd.Stderr = os.Stderr

			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, cmd.Start())

			lines := readLogLines(stdout)
			started := waitForLog(t, lines, "sidekick started")
			metrics := "http://" + started.MetricsListen

			status, _ := getURL(t, metrics+"/readyz")
			require.Equal(t, http.StatusOK, status, "readiness does not depend on sources")

			require.Eventually(t, func() bool { return len(mcpServer.Requests()) > 0 }, serveStartTimeout, 10*time.Millisecond,
				"serve tries to connect in the background")

			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			waitForLog(t, lines, "sidekick stopped")

			for range lines {
			}

			require.NoError(t, cmd.Wait(), "serve exits with status 0")
		})
	}
}

// getURL sends a GET request to url and returns the status and the body.
func getURL(t *testing.T, url string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(body)
}

// readLogLines decodes each JSON log line from r and sends it on the
// returned channel, which is closed at the end of r.
func readLogLines(r io.Reader) <-chan logLine {
	lines := make(chan logLine)

	go func() {
		defer close(lines)

		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			var line logLine
			if json.Unmarshal(scanner.Bytes(), &line) == nil {
				lines <- line
			}
		}
	}()

	return lines
}

// waitForLog returns the first line from lines whose message is msg.
func waitForLog(t *testing.T, lines <-chan logLine, msg string) logLine {
	t.Helper()

	timeout := time.After(serveStartTimeout)

	for {
		select {
		case line, ok := <-lines:
			require.True(t, ok, "serve stopped logging before %q", msg)

			if line.Msg == msg {
				return line
			}
		case <-timeout:
			t.Fatalf("serve did not log %q", msg)
		}
	}
}
