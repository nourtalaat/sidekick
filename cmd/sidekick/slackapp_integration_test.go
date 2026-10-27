//go:build integration

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// slackMessage is an Events API payload with a person's message in
// C01OPS.
const slackMessage = `{
  "type": "event_callback",
  "team_id": "T01TEAM",
  "api_app_id": "A01SIDEKICK",
  "event_id": "Ev01OPS",
  "authorizations": [{"user_id": "U01SIDEKICK", "is_bot": true}],
  "event": {"type": "message", "channel": "C01OPS", "user": "U02ALICE", "text": "Checkout returns 502", "ts": "1761472960.000500"}
}`

// TestServeSlackApp checks that `sidekick serve` receives Slack events in
// the mode the config sets: at /slack/events, where a listener's message
// is stored and investigated and the reply goes in the message's thread,
// or over Socket Mode with the app token.
func TestServeSlackApp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// slack is the slack section and listeners, after the bot token.
		slack string
		// answers are the recorded model answers the investigation gets.
		answers []string
		// run acts on the started serve and checks what it did.
		run func(t *testing.T, started logLine, lines <-chan logLine, slackAPI *fakeSlack)
	}{
		{
			name:    "events mode: a listener's message is investigated and answered in its thread",
			answers: []string{"fixture/anthropic-inconclusive.sse"},
			slack: `
  signingSecret: { name: slack, key: signingSecret }
  mode: events
listeners:
  ops:
    channel: C01OPS
    match: { sender: { kind: human } }
`,
			run: func(t *testing.T, started logLine, lines <-chan logLine, slackAPI *fakeSlack) {
				t.Helper()

				postSlackEvent(t, started, slackMessage)

				require.Equal(t, "default", waitForLog(t, lines, "event routed").Route)
				require.Equal(t, "inconclusive", waitForLog(t, lines, "investigation finished").Verdict)

				require.Eventually(t, func() bool { return len(slackAPI.callsTo("chat.postMessage")) > 0 }, serveStartTimeout, 50*time.Millisecond)

				first := slackAPI.callsTo("chat.postMessage")[0]
				require.Equal(t, "C01OPS", first.Channel)
				require.Equal(t, "1761472960.000500", first.ThreadTS, "the reply goes in the thread of the message")
				require.Equal(t, "Looking into it: Checkout returns 502", first.Text)
			},
		},
		{
			name: "socket mode: the app token opens the connection",
			slack: `
  appTokenSecret: { name: slack, key: appToken }
`,
			run: func(t *testing.T, _ logLine, lines <-chan logLine, _ *fakeSlack) {
				t.Helper()

				waitForLog(t, lines, "Slack refused the app token; check slack.appTokenSecret")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			anthropic := fakeAnthropic(t, tt.answers...)

			slackAPI := newFakeSlack(t)
			slackAPI.api.HandleFunc(http.MethodPost, "/api/apps.connections.open", func(w http.ResponseWriter, _ *http.Request) {
				answerSlack(w, `{"ok":false,"error":"invalid_auth"}`)
			})

			content := strings.Replace(serveConfig(anthropic.URL()), "routes:\n  - name: default\n", "routes:\n  - name: default\n    outputs: [slack-thread]\n", 1) + `
outputs:
  slack-thread: { type: slack, channel: "#alerts", apiURL: "` + slackAPI.api.URL() + `/api/" }
slack:
  botTokenSecret: { name: slack, key: botToken }
  apiURL: "` + slackAPI.api.URL() + `/api/"` + tt.slack

			cmd, lines, started := startServe(t, writeConfig(t, content, storetest.NewURL(t), "slack/botToken", "slack/appToken", "slack/signingSecret"))

			tt.run(t, started, lines, slackAPI)

			stopServe(t, cmd, lines, syscall.SIGTERM)
		})
	}
}

// postSlackEvent posts payload to /slack/events, signed with the fake
// signing secret, and checks that it is accepted.
func postSlackEvent(t *testing.T, started logLine, payload string) {
	t.Helper()

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("fake-value"))
	mac.Write([]byte("v0:" + ts + ":" + payload))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+started.Listen+"/slack/events", strings.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
