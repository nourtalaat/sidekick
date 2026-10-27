//go:build integration

package slackapp

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/intake/ingest"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// request is one Events API request: a fixture body, signed now unless
// header is set.
type request struct {
	file   string
	header func(body []byte) http.Header
	body   []byte
}

func TestEventsAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// slack adds the fake Web API's responses.
		slack func(s *fakehttp.Server)
		// before are sent first; their answers must be 200.
		before     []request
		send       request
		wantStatus int
		wantBody   string
		check      func(t *testing.T, h *harness)
	}{
		{
			name:       "URL verification answers the challenge",
			send:       request{file: "url-verification"},
			wantStatus: http.StatusOK,
			wantBody:   `{"challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"}`,
			check:      requireNoEvents,
		},
		{
			name:       "a good signature stores a trusted bot's alert with its route_event job",
			slack:      channelInfo,
			send:       request{file: "signoz-firing"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, "alerts", ev.Intake)
				require.Equal(t, "Ev01FIRING", ev.Key)
				require.Equal(t, string(event.KindAlert), ev.Kind)
				require.True(t, ev.Trusted)
				require.Equal(t, "HighErrorRate", *ev.AlertName)
				require.Equal(t, "[FIRING] HighErrorRate", *ev.Title)
				require.Equal(t, "checkout", *ev.Service)
				require.Equal(t, string(event.SeverityCritical), *ev.Severity)
				require.JSONEq(t, `{"env": "prod"}`, string(ev.Labels))
				require.Equal(t, event.Fingerprint("alerts", "HighErrorRate", map[string]string{"env": "prod"}, []string{"env"}), ev.Fingerprint)
				require.Equal(t, ingest.StatusReceived, ev.Status)
				require.JSONEq(t, `[{"title": "[FIRING] HighErrorRate", "url": "https://signoz.example.com/alerts/7"}]`, string(ev.Links))
				require.Equal(t, 1, h.routeJobs(t))

				got, err := store.EventOf(ev)
				require.NoError(t, err)
				require.Equal(t, &event.SlackRef{TeamID: "T01TEAM", Channel: "C01ALERTS", TS: "1761472800.000100"}, got.Slack)
				require.Equal(t, event.Sender{Kind: event.SenderBot, ID: "B01SIGNOZ", Name: "SigNoz"}, got.Sender)

				require.InDelta(t, 1, h.counter(t, "sidekick_events_received_total", "alerts", "result", ingest.ResultAccepted), 0)

				req := h.slack.Requests()[0]
				require.Equal(t, "Bearer "+testBotToken, req.Header.Get("Authorization"))
				require.Equal(t, "C01ALERTS", req.Query.Get("channel"))

				spans := h.spans.Ended()
				require.Equal(t, SpanName, spans[len(spans)-1].Name())
			},
		},
		{
			name:       "a bot that is not in trustedBots gives an untrusted event",
			slack:      channelInfo,
			send:       request{file: "datadog-firing"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, "alerts", ev.Intake)
				require.False(t, ev.Trusted)
				require.Equal(t, "DiskFull", *ev.AlertName)
			},
		},
		{
			name:       "a bad signature is rejected",
			send:       request{file: "signoz-firing", header: func(body []byte) http.Header { return signed("not-the-secret", time.Now(), body) }},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"error":"unauthenticated"}`,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				requireNoEvents(t, h)
				require.InDelta(t, 1, h.counter(t, "sidekick_events_rejected_total", rejectedIntake, "reason", ingest.ReasonUnauthenticated), 0)
			},
		},
		{
			name:       "a missing signature is rejected",
			send:       request{file: "signoz-firing", header: func([]byte) http.Header { return http.Header{} }},
			wantStatus: http.StatusUnauthorized,
			check:      requireNoEvents,
		},
		{
			name: "an old timestamp is rejected, even with a good signature",
			send: request{file: "signoz-firing", header: func(body []byte) http.Header {
				return signed(testSigningSecret, time.Now().Add(-SignatureTolerance-time.Minute), body)
			}},
			wantStatus: http.StatusUnauthorized,
			check:      requireNoEvents,
		},
		{
			name:       "a repeated event ID stores one event",
			slack:      channelInfo,
			before:     []request{{file: "signoz-firing"}},
			send:       request{file: "signoz-firing"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				onlyEvent(t, h)
				require.Equal(t, 1, h.routeJobs(t))
				require.InDelta(t, 1, h.counter(t, "sidekick_events_received_total", "alerts", "result", ingest.ResultDuplicate), 0)
			},
		},
		{
			name:       "a recovery message is a resolved event with the alert's fingerprint",
			slack:      channelInfo,
			before:     []request{{file: "signoz-firing"}},
			send:       request{file: "signoz-resolved"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				events := h.events(t)
				require.Len(t, events, 2)
				require.Equal(t, string(event.KindResolved), events[1].Kind)
				require.Equal(t, events[0].Fingerprint, events[1].Fingerprint)
			},
		},
		{
			name:       "a person's message in a listener channel by ID is stored redacted and untrusted",
			send:       request{file: "ops-message"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, "ops", ev.Intake)
				require.Equal(t, string(event.KindMessage), ev.Kind)
				require.False(t, ev.Trusted)
				require.NotContains(t, *ev.Body, "SEEDED-ABCDEFGH")
				require.NotContains(t, string(ev.Raw), "SEEDED-ABCDEFGH")
				require.Contains(t, *ev.Body, "Checkout returns 502")
				require.Equal(t, event.Fingerprint("slack", "", map[string]string{"channel": "C01OPS", "ts": "1761472960.000500"}, []string{"channel", "ts"}), ev.Fingerprint)
			},
		},
		{
			name:       "a top-level mention investigates its own text",
			send:       request{file: "mention-top"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, config.SlackMentionIntake, ev.Intake)
				require.Equal(t, "Ev10MENTION", ev.Key)
				require.Equal(t, string(event.KindMention), ev.Kind)
				require.False(t, ev.Trusted)
				require.Equal(t, "why is checkout slow?", *ev.Body)

				got, err := store.EventOf(ev)
				require.NoError(t, err)
				require.Equal(t, &event.SlackRef{TeamID: "T01TEAM", Channel: "C01OPS", TS: "1761473000.000900", UserID: "U02ALICE"}, got.Slack)
			},
		},
		{
			name: "a mention in a thread investigates the thread's first message, with the listener's fingerprint",
			slack: func(s *fakehttp.Server) {
				s.Handle(http.MethodGet, "/"+methodReplies, fakehttp.Response{BodyFile: "fixture/slack/replies-signoz-firing.json"})
				channelInfo(s)
			},
			send:       request{file: "mention-thread"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, config.SlackMentionIntake, ev.Intake)
				require.Equal(t, string(event.KindMention), ev.Kind)
				require.False(t, ev.Trusted, "a mention is never trusted, whoever posted the message")
				require.Equal(t, "[FIRING] HighErrorRate", *ev.Title)
				require.Equal(t, "checkout", *ev.Service)
				require.Equal(t, event.Fingerprint("alerts", "HighErrorRate", map[string]string{"env": "prod"}, []string{"env"}), ev.Fingerprint)

				got, err := store.EventOf(ev)
				require.NoError(t, err)
				require.Equal(t, "1761472800.000100", got.Slack.TS, "the reply goes in the thread")

				replies := h.slack.Requests()[0]
				require.Equal(t, "/"+methodReplies, replies.Path)
				require.Equal(t, "1761472800.000100", replies.Query.Get("ts"))
			},
		},
		{
			name: "a mention in a thread that cannot be read is not acknowledged",
			slack: func(s *fakehttp.Server) {
				s.Handle(http.MethodGet, "/"+methodReplies, fakehttp.Response{BodyFile: "fixture/slack/replies-not-in-channel.json"})
			},
			send:       request{file: "mention-thread"},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error":"internal error"}`,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				requireNoEvents(t, h)
				require.InDelta(t, 1, h.counter(t, "sidekick_slack_api_errors_total", "", "error", "not_in_channel"), 0)
			},
		},
		{
			name: "a mention in a thread Sidekick started is left for follow-ups",
			slack: func(s *fakehttp.Server) {
				s.Handle(http.MethodGet, "/"+methodReplies, fakehttp.Response{BodyFile: "fixture/slack/replies-own-message.json"})
			},
			send:       request{file: "mention-own-thread"},
			wantStatus: http.StatusOK,
			check:      requireNoEvents,
		},
		{name: "Sidekick's own message is ignored", send: request{file: "own-message"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "an edit is ignored", send: request{file: "edit"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "a thread reply is ignored", send: request{file: "thread-reply"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "a message that mentions Sidekick is left to its app_mention", send: request{file: "mention-as-message"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "a person in a bots-only listener's channel is ignored", send: request{file: "person-in-alerts"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "other payload types are acknowledged", send: request{file: "app-rate-limited"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{
			name:       "a signed payload that is not JSON is rejected",
			send:       request{body: []byte("not json")},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"invalid payload"}`,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				requireNoEvents(t, h)
				require.InDelta(t, 1, h.counter(t, "sidekick_events_rejected_total", rejectedIntake, "reason", ingest.ReasonInvalidPayload), 0)
			},
		},
		{
			name:       "a body over 1 MiB is rejected",
			send:       request{body: bytes.Repeat([]byte(" "), MaxBodyBytes+1)},
			wantStatus: http.StatusRequestEntityTooLarge,
			check:      requireNoEvents,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			if tt.slack != nil {
				tt.slack(h.slack)
			}

			for _, r := range tt.before {
				status, _ := h.post(t, r.headers(t), r.content(t))
				require.Equal(t, http.StatusOK, status)
			}

			status, body := h.post(t, tt.send.headers(t), tt.send.content(t))
			require.Equal(t, tt.wantStatus, status, body)

			if tt.wantBody != "" {
				require.JSONEq(t, tt.wantBody, body)
			}

			tt.check(t, h)
		})
	}
}

// content returns the body of r.
func (r request) content(t *testing.T) []byte {
	t.Helper()

	if r.body != nil {
		return r.body
	}

	return readFixture(t, "requests/"+r.file+".json")
}

// headers returns the headers of r: its own, or a signature made now.
func (r request) headers(t *testing.T) http.Header {
	t.Helper()

	body := r.content(t)
	if r.header != nil {
		return r.header(body)
	}

	return signed(testSigningSecret, time.Now(), body)
}

// channelInfo makes the fake Slack say that C01ALERTS is #alerts, once.
func channelInfo(s *fakehttp.Server) {
	s.Handle(http.MethodGet, "/"+methodInfo, fakehttp.Response{BodyFile: "fixture/slack/info-alerts.json"})
}

// onlyEvent returns the one stored event.
func onlyEvent(t *testing.T, h *harness) db.Event {
	t.Helper()

	events := h.events(t)
	require.Len(t, events, 1)

	return events[0]
}

// requireNoEvents checks that nothing was stored.
func requireNoEvents(t *testing.T, h *harness) {
	t.Helper()

	require.Empty(t, h.events(t))
	require.Zero(t, h.routeJobs(t))
}
