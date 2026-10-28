//go:build integration

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The questions the follow-up test asks in the thread of slackMessage.
const (
	askDeploy = "did the deploy at 14:00 cause it?"
	askPods   = "which pods log the errors?"
)

// followUpAnswer is the answer in fixture/anthropic-follow-up-answer.sse.
const followUpAnswer = "No source is configured, so neither the deploy nor the pods could be checked."

// slowFirstAnswer is how long the first model call of the follow-up test
// takes, so both replies arrive while the investigation runs.
const slowFirstAnswer = 3 * time.Second

// slackMention returns an app_mention payload with eventID, a reply that
// mentions Sidekick in the thread of slackMessage.
func slackMention(eventID, ts, text string) string {
	return fmt.Sprintf(`{
  "type": "event_callback",
  "team_id": "T01TEAM",
  "api_app_id": "A01SIDEKICK",
  "event_id": %q,
  "authorizations": [{"user_id": "U01SIDEKICK", "is_bot": true}],
  "event": {"type": "app_mention", "channel": "C01OPS", "user": "U03BOB", "text": "<@U01SIDEKICK> %s",
            "ts": %q, "thread_ts": "1761472960.000500"}
}`, eventID, text, ts)
}

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
		answers []fakehttp.Response
		// run acts on the started serve and checks what it did.
		run func(t *testing.T, s served)
	}{
		{
			name:    "events mode: a listener's message is investigated and answered in its thread",
			answers: []fakehttp.Response{sse("fixture/anthropic-inconclusive.sse")},
			slack: `
  signingSecret: { name: slack, key: signingSecret }
  mode: events
listeners:
  ops:
    channel: C01OPS
    match: { sender: { kind: human } }
`,
			run: func(t *testing.T, s served) {
				t.Helper()

				started, lines, slackAPI := s.started, s.lines, s.slack

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
			run: func(t *testing.T, s served) {
				t.Helper()

				waitForLog(t, s.lines, "Slack refused the app token; check slack.appTokenSecret")
			},
		},
		{
			name: "events mode: two replies that mention Sidekick in the thread get one follow-up answer",
			answers: []fakehttp.Response{
				withDelay(sse("fixture/anthropic-inconclusive.sse"), slowFirstAnswer),
				sse("fixture/anthropic-follow-up-answer.sse"),
			},
			slack: `
  signingSecret: { name: slack, key: signingSecret }
  mode: events
listeners:
  ops:
    channel: C01OPS
    match: { sender: { kind: human } }
`,
			run: func(t *testing.T, s served) {
				t.Helper()

				postSlackEvent(t, s.started, slackMessage)
				waitForOutputRef(t, s.dbURL)

				postSlackEvent(t, s.started, slackMention("Ev02DEPLOY", "1761472990.000600", askDeploy))
				postSlackEvent(t, s.started, slackMention("Ev03PODS", "1761472995.000700", askPods))
				postSlackEvent(t, s.started, slackMention("Ev03PODS", "1761472995.000700", askPods))

				require.Equal(t, "inconclusive", waitForLog(t, s.lines, "investigation finished").Verdict)
				waitForLog(t, s.lines, "follow-up finished")

				require.Eventually(t, func() bool { return s.slack.has("chat.postMessage", "Answer: "+followUpAnswer) }, serveStartTimeout, 50*time.Millisecond)

				for _, c := range s.slack.callsTo("chat.postMessage")[1:] {
					require.Equal(t, "1761472960.000500", c.ThreadTS, "every reply goes in the alert's thread")
				}

				calls := s.anthropic.Requests()
				require.Len(t, calls, 2, "one follow-up run answers both questions")
				require.NotContains(t, string(calls[0].Body), askDeploy, "the first run leaves the questions to the follow-up")

				followUp := string(calls[1].Body)
				require.Contains(t, followUp, askDeploy)
				require.Contains(t, followUp, askPods)
				require.Equal(t, 1, strings.Count(followUp, askPods), "a reply that Slack sends again is asked once")
				require.Contains(t, followUp, `"answer_follow_up"`, "the follow-up run offers answer_follow_up")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			anthropic := fakehttp.New(t)
			if len(tt.answers) > 0 {
				anthropic.Handle(http.MethodPost, messagesPath, tt.answers...)
				anthropic.ExpectAllCalled()
			}

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

			dbURL := storetest.NewURL(t)
			cmd, lines, started := startServe(t, writeConfig(t, content, dbURL, "slack/botToken", "slack/appToken", "slack/signingSecret"))

			tt.run(t, served{started: started, lines: lines, slack: slackAPI, anthropic: anthropic, dbURL: dbURL})

			stopServe(t, cmd, lines, syscall.SIGTERM)
		})
	}
}

// served is a started `sidekick serve` with its fakes and database.
type served struct {
	started   logLine
	lines     <-chan logLine
	slack     *fakeSlack
	anthropic *fakehttp.Server
	dbURL     string
}

// waitForOutputRef waits until the database at dbURL holds the ref of a
// message an output published, so a reply in its thread finds the
// investigation.
func waitForOutputRef(t *testing.T, dbURL string) {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), dbURL)
	require.NoError(t, err)

	defer pool.Close()

	require.Eventually(t, func() bool {
		var n int

		err := pool.QueryRow(t.Context(), "SELECT count(*) FROM output_refs").Scan(&n)

		return err == nil && n > 0
	}, serveStartTimeout, 50*time.Millisecond)
}

// sse returns a recorded Messages API answer from file.
func sse(file string) fakehttp.Response {
	return fakehttp.Response{BodyFile: file, Headers: map[string]string{"Content-Type": "text/event-stream"}}
}

// withDelay returns r, answered after d.
func withDelay(r fakehttp.Response, d time.Duration) fakehttp.Response {
	r.Delay = d

	return r
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
