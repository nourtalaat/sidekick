//go:build integration

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
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

// webhookConfig is serveConfig with an Alertmanager webhook endpoint whose
// bearer token is the fake secret value.
const webhookConfig = serveConfig + `
intake:
  webhooks:
    alertmanager:
      adapter: alertmanager
      auth: { bearer: { name: hooks, key: alertmanager } }
`

// alertmanagerDelivery is an Alertmanager webhook payload with one alert.
const alertmanagerDelivery = `{
  "version": "4",
  "groupKey": "{}:{alertname=\"DiskFull\"}",
  "status": "firing",
  "receiver": "sidekick",
  "alerts": [{
    "status": "firing",
    "labels": {"alertname": "DiskFull", "severity": "warning"},
    "annotations": {"summary": "Disk almost full"},
    "startsAt": "2026-10-26T09:00:00Z",
    "fingerprint": "0a1b2c3d4e5f6071"
  }]
}`

// logLine holds the fields of a log line that the test reads.
type logLine struct {
	Msg           string `json:"msg"`
	Listen        string `json:"listen"`
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

			cmd, lines, started := startServe(t, writeConfig(t, serveConfig, storetest.NewURL(t)))

			resp, err := http.Get("http://" + started.MetricsListen + "/healthz") //nolint:noctx // A plain request to a local server.
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusOK, resp.StatusCode)

			signalled := time.Now()

			stopServe(t, cmd, lines, tt.signal)
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
			cmd, lines, started := startServe(t, writeConfig(t, config, storetest.NewURL(t)))
			metrics := "http://" + started.MetricsListen

			status, _ := getURL(t, metrics+"/readyz")
			require.Equal(t, http.StatusOK, status, "readiness does not depend on sources")

			require.Eventually(t, func() bool { return len(mcpServer.Requests()) > 0 }, serveStartTimeout, 10*time.Millisecond,
				"serve tries to connect in the background")

			stopServe(t, cmd, lines, syscall.SIGTERM)
		})
	}
}

// TestServeWebhooks checks that `sidekick serve` serves the configured
// webhook endpoints with the built-in adapters, stores what they accept
// and counts it in the metrics.
func TestServeWebhooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		endpoint    string
		token       string
		wantStatus  int
		wantMetrics []string
	}{
		{
			name:        "an authenticated Alertmanager delivery is stored",
			endpoint:    "alertmanager",
			token:       "fake-value",
			wantStatus:  http.StatusAccepted,
			wantMetrics: []string{`sidekick_events_received_total{intake="alertmanager",result="accepted"} 1`},
		},
		{
			name:       "a wrong token is rejected",
			endpoint:   "alertmanager",
			token:      "wrong",
			wantStatus: http.StatusUnauthorized,
			wantMetrics: []string{
				`sidekick_events_received_total{intake="alertmanager",result="rejected"} 1`,
				`sidekick_events_rejected_total{intake="alertmanager",reason="unauthenticated"} 1`,
			},
		},
		{
			name:        "an endpoint that is not configured is not found",
			endpoint:    "other",
			token:       "fake-value",
			wantStatus:  http.StatusNotFound,
			wantMetrics: []string{`sidekick_events_rejected_total{intake="_unknown",reason="unauthenticated"} 1`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd, lines, started := startServe(t, writeConfig(t, webhookConfig, storetest.NewURL(t), "hooks/alertmanager"))

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+started.Listen+"/hooks/"+tt.endpoint, strings.NewReader(alertmanagerDelivery))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+tt.token)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tt.wantStatus, resp.StatusCode)

			status, metrics := getURL(t, "http://"+started.MetricsListen+"/metrics")
			require.Equal(t, http.StatusOK, status)

			for _, want := range tt.wantMetrics {
				require.Contains(t, metrics, want)
			}

			stopServe(t, cmd, lines, syscall.SIGTERM)
		})
	}
}

// startServe runs `sidekick serve` with the config at configPath as its
// own process and waits until it serves. It returns the process, its log
// lines and the start-up line.
func startServe(t *testing.T, configPath string) (*exec.Cmd, <-chan logLine, logLine) {
	t.Helper()

	//nolint:gosec // Runs this test binary again, with fixed arguments.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "serve", "--config", configPath)
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	lines := readLogLines(stdout)

	return cmd, lines, waitForLog(t, lines, "sidekick started")
}

// stopServe sends sig to the serve process and checks that it stops
// cleanly.
func stopServe(t *testing.T, cmd *exec.Cmd, lines <-chan logLine, sig os.Signal) {
	t.Helper()

	require.NoError(t, cmd.Process.Signal(sig))
	waitForLog(t, lines, "sidekick stopped")

	// Wait must come after every read from stdout.
	for range lines {
	}

	require.NoError(t, cmd.Wait(), "serve exits with status 0")
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
