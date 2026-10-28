//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/auth/authtest"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// serveStartTimeout bounds how long the test waits for serve to start, and
// for an investigation to end.
const serveStartTimeout = time.Minute

// messagesPath is the path of the Anthropic Messages API.
const messagesPath = "/v1/messages"

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
	Route         string `json:"route"`
	Verdict       string `json:"verdict"`
	StopReason    string `json:"stop_reason"`
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

			cmd, lines, started := startServe(t, writeConfig(t, serveConfig(fakeAnthropic(t).URL()), storetest.NewURL(t)))

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
// the source keeps trying to connect in the background, and that
// sidekick_source_up says the source is down.
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

			config := serveConfig(fakeAnthropic(t).URL()) + "sources:\n  signoz: { type: mcp, url: \"" + mcpServer.URL() + "/mcp\" }\n"
			cmd, lines, started := startServe(t, writeConfig(t, config, storetest.NewURL(t)))
			metrics := "http://" + started.MetricsListen

			status, _ := getURL(t, metrics+"/readyz")
			require.Equal(t, http.StatusOK, status, "readiness does not depend on sources")

			require.Eventually(t, func() bool { return len(mcpServer.Requests()) > 0 }, serveStartTimeout, 10*time.Millisecond,
				"serve tries to connect in the background")

			_, body := getURL(t, metrics+"/metrics")
			require.Contains(t, body, `sidekick_source_up{source="signoz"} 0`)

			stopServe(t, cmd, lines, syscall.SIGTERM)
		})
	}
}

// TestServeWebhooks checks that `sidekick serve` serves the configured
// webhook endpoints with the built-in adapters, stores what they accept,
// counts it in the metrics, and routes and investigates each stored event.
func TestServeWebhooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		endpoint    string
		token       string
		wantStatus  int
		wantMetrics []string
		// wantRoute is the route the stored event goes to, if any; its
		// investigation gets the recorded inconclusive verdict.
		wantRoute string
	}{
		{
			name:        "an authenticated Alertmanager delivery is stored",
			endpoint:    "alertmanager",
			token:       "fake-value",
			wantStatus:  http.StatusAccepted,
			wantMetrics: []string{`sidekick_events_received_total{intake="alertmanager",result="accepted"} 1`},
			wantRoute:   "default",
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

			var answers []string
			if tt.wantRoute != "" {
				answers = []string{"fixture/anthropic-inconclusive.sse"}
			}

			anthropic := fakeAnthropic(t, answers...)

			cmd, lines, started := startServe(t, writeConfig(t, webhookConfig(anthropic.URL()), storetest.NewURL(t), "hooks/alertmanager"))

			postAlert(t, started, tt.endpoint, tt.token, tt.wantStatus)

			status, metrics := getURL(t, "http://"+started.MetricsListen+"/metrics")
			require.Equal(t, http.StatusOK, status)

			for _, want := range tt.wantMetrics {
				require.Contains(t, metrics, want)
			}

			if tt.wantRoute != "" {
				require.Equal(t, tt.wantRoute, waitForLog(t, lines, "event routed").Route)
				require.Equal(t, "inconclusive", waitForLog(t, lines, "investigation finished").Verdict)
			}

			stopServe(t, cmd, lines, syscall.SIGTERM)
		})
	}
}

// TestServeAPI runs `sidekick serve` with an oidc section and checks that
// it serves the API, which needs a session, and the login, which sends the
// browser to the provider.
func TestServeAPI(t *testing.T) {
	t.Parallel()

	provider := authtest.NewProvider(t, "sidekick")

	content := strings.Replace(serveConfig(fakeAnthropic(t).URL()), "server: { listen:", `server: { publicURL: "http://127.0.0.1", listen:`, 1) + `
oidc:
  issuer: "` + provider.Issuer() + `"
  clientID: sidekick
  clientSecret: { name: oidc, key: clientSecret }
  roles: { viewer: ["*"] }
`

	cmd, lines, started := startServe(t, writeConfig(t, content, storetest.NewURL(t), "oidc/clientSecret"))

	status, body := getURL(t, "http://"+started.Listen+"/api/v1/me")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Contains(t, body, `"status":401`)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+started.Listen+"/auth/login", nil)
	require.NoError(t, err)

	noRedirects := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noRedirects.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.True(t, strings.HasPrefix(resp.Header.Get("Location"), provider.Issuer()+authtest.AuthorizePath+"?"))

	stopServe(t, cmd, lines, syscall.SIGTERM)
}

// TestServeEndToEnd runs `sidekick serve` with an Alertmanager webhook, an
// MCP source, the Anthropic connector and a Slack output, each third party
// faked with fakehttp, and follows one alert from the webhook to the
// verdict in Slack: the router opens the investigation and posts "Looking
// into it", the model calls the source and submits a verdict, and the
// Slack message ends with it.
func TestServeEndToEnd(t *testing.T) {
	t.Parallel()

	anthropic := fakeAnthropic(t, "fixture/anthropic-search.sse", "fixture/anthropic-verdict.sse")
	signoz := fakeMCP(t)
	slackAPI := newFakeSlack(t)

	content := strings.Replace(webhookConfig(anthropic.URL()), "  - name: default\n", "  - name: default\n    outputs: [slack-thread]\n", 1) + `
slack:
  botTokenSecret: { name: slack, key: botToken }
outputs:
  slack-thread: { type: slack, channel: "#alerts", apiURL: "` + slackAPI.api.URL() + `/api/" }
sources:
  signoz: { type: mcp, url: "` + signoz.URL() + `/mcp" }
`

	cmd, lines, started := startServe(t, writeConfig(t, content, storetest.NewURL(t), "hooks/alertmanager", "slack/botToken"))

	postAlert(t, started, "alertmanager", "fake-value", http.StatusAccepted)

	finished := waitForLog(t, lines, "investigation finished")
	require.Equal(t, "true_positive", finished.Verdict)
	require.Equal(t, "submitted", finished.StopReason)

	const verdictText = "Real issue (high confidence): Disk almost full"

	require.Eventually(t, func() bool { return slackAPI.has("chat.update", verdictText) }, serveStartTimeout, 50*time.Millisecond,
		"the Slack message ends with the verdict")

	posts := slackAPI.callsTo("chat.postMessage")
	require.Equal(t, "Looking into it: Disk almost full", posts[0].Text)
	require.Equal(t, "#alerts", posts[0].Channel)

	require.Eventually(t, func() bool { return slackAPI.has("chat.postMessage", verdictText) }, serveStartTimeout, 50*time.Millisecond,
		"a thread reply announces the verdict")

	sent := anthropic.Requests()
	require.Len(t, sent, 2)
	require.Equal(t, "<untrusted_data source=\"signoz\" tool=\"signoz__search_logs\" call=\"toolu_01Search\">\n"+
		"db-1 disk use: 80% at 09:00, 97% at 10:00\n</untrusted_data>", lastToolResult(t, sent[1].Body),
		"the source's result goes back to the model redacted and wrapped as untrusted data")

	_, metrics := getURL(t, "http://"+started.MetricsListen+"/metrics")
	for _, want := range []string{
		`sidekick_source_up{source="signoz"} 1`,
		`sidekick_tool_calls_total{outcome="ok",source="signoz",tool="search_logs"} 1`,
		`sidekick_tool_call_duration_seconds_count{source="signoz"} 1`,
		`sidekick_model_calls_total{connector="claude",model="claude-opus-5-5",outcome="ok",role="investigate"} 2`,
		`sidekick_investigations_total{route="default",stop_reason="submitted",verdict="true_positive"} 1`,
	} {
		require.Contains(t, metrics, want)
	}

	stopServe(t, cmd, lines, syscall.SIGTERM)
}

// serveConfig is validConfig listening on ports the system picks, with the
// claude connector sending its requests to anthropicURL.
func serveConfig(anthropicURL string) string {
	return strings.Replace(validConfig, "apiKeySecret: { name: anthropic, key: apiKey } }",
		`apiKeySecret: { name: anthropic, key: apiKey }, baseURL: "`+anthropicURL+`" }`, 1) + `
server: { listen: "127.0.0.1:0", metricsListen: "127.0.0.1:0" }
`
}

// webhookConfig is serveConfig with an Alertmanager webhook endpoint whose
// bearer token is the fake secret value.
func webhookConfig(anthropicURL string) string {
	return serveConfig(anthropicURL) + `
intake:
  webhooks:
    alertmanager:
      adapter: alertmanager
      auth: { bearer: { name: hooks, key: alertmanager } }
`
}

// fakeAnthropic returns a fake Anthropic API that answers Messages calls
// with the recorded responses in files, in order. Any other call, or a
// response left unused, fails the test.
func fakeAnthropic(t *testing.T, files ...string) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)

	responses := make([]fakehttp.Response, 0, len(files))
	for _, f := range files {
		responses = append(responses, fakehttp.Response{BodyFile: f, Headers: map[string]string{"Content-Type": "text/event-stream"}})
	}

	if len(responses) > 0 {
		api.Handle(http.MethodPost, messagesPath, responses...)
		api.ExpectAllCalled()
	}

	return api
}

// fakeMCP returns a fake MCP server with one read-only tool, search_logs,
// served over streamable HTTP at /mcp.
func fakeMCP(t *testing.T) *fakehttp.Server {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "signoz", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "search_logs",
		Description: "Search the logs.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string"}}}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "db-1 disk use: 80% at 09:00, 97% at 10:00"}}}, nil
	})

	api := fakehttp.New(t)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		api.HandleFunc(method, "/mcp", h.ServeHTTP)
	}

	return api
}

// slackCall is one call the fake Slack got.
type slackCall struct {
	Method   string
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
}

// fakeSlack is the Slack Web API for one test: each post gets the next
// timestamp, and every call is recorded. A post to a channel ID is in that
// channel, as in Slack; a post to a channel name is in C123.
type fakeSlack struct {
	api *fakehttp.Server

	mu    sync.Mutex
	calls []slackCall
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()

	f := &fakeSlack{api: fakehttp.New(t)}

	f.api.HandleFunc(http.MethodPost, "/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		call := f.record(t, "chat.postMessage", r)

		channel := "C123"
		if strings.HasPrefix(call.Channel, "C") {
			channel = call.Channel
		}

		answerSlack(w, `{"ok":true,"channel":"%s","ts":"1793023321.%06d"}`, channel, len(f.callsTo("chat.postMessage")))
	})

	f.api.HandleFunc(http.MethodPost, "/api/chat.update", func(w http.ResponseWriter, r *http.Request) {
		call := f.record(t, "chat.update", r)
		answerSlack(w, `{"ok":true,"channel":"%s","ts":"%s"}`, call.Channel, call.TS)
	})

	f.api.HandleFunc(http.MethodGet, "/api/chat.getPermalink", func(w http.ResponseWriter, r *http.Request) {
		answerSlack(w, `{"ok":true,"permalink":"https://example.slack.com/archives/%s/p%s"}`, r.URL.Query().Get("channel"), r.URL.Query().Get("message_ts"))
	})

	return f
}

// record decodes and keeps one call.
func (f *fakeSlack) record(t *testing.T, method string, r *http.Request) slackCall {
	t.Helper()

	call := slackCall{Method: method}
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		t.Errorf("decode the %s body: %v", method, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)

	return call
}

// callsTo returns the calls to method, in order.
func (f *fakeSlack) callsTo(method string) []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []slackCall

	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}

	return out
}

// has reports whether a call to method had the text.
func (f *fakeSlack) has(method, text string) bool {
	for _, c := range f.callsTo(method) {
		if c.Text == text {
			return true
		}
	}

	return false
}

// answerSlack writes a Slack API answer.
func answerSlack(w http.ResponseWriter, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, format, args...) //nolint:gosec // A fake API answering the code under test, not a browser.
}

// lastToolResult returns the text of the last tool result in a Messages
// API request body.
func lastToolResult(t *testing.T, body []byte) string {
	t.Helper()

	var req struct {
		Messages []struct {
			Content []struct {
				Type    string `json:"type"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(body, &req))

	var text string

	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Type == "tool_result" && len(c.Content) > 0 {
				text = c.Content[0].Text
			}
		}
	}

	return text
}

// postAlert delivers alertmanagerDelivery to the endpoint with the bearer
// token, and checks the status.
func postAlert(t *testing.T, started logLine, endpoint, token string, wantStatus int) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+started.Listen+"/hooks/"+endpoint, strings.NewReader(alertmanagerDelivery))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, wantStatus, resp.StatusCode)
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
