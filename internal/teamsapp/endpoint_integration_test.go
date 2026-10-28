//go:build integration

package teamsapp

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/intake/ingest"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// request is one activity sent to the endpoint: a fixture under
// fixture/activities, or body, with a valid token unless auth is set.
type request struct {
	file string
	body []byte
	auth func(t *testing.T) string
}

func TestMessagesEndpoint(t *testing.T) {
	t.Parallel()

	otherKey, err := rsa.GenerateKey(rand.Reader, testRSABits)
	require.NoError(t, err)

	tests := []struct {
		name string
		// microsoft adds the fake Microsoft's token and Graph answers.
		microsoft func(t *testing.T, h *harness)
		// before are sent first; their answers must be 200.
		before     []request
		send       request
		wantStatus int
		wantBody   string
		check      func(t *testing.T, h *harness)
	}{
		{
			name:       "a trusted bot's card in a listener channel is a trusted alert with its route_event job",
			send:       request{file: "grafana-firing"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, "alerts", ev.Intake)
				require.Equal(t, "19:alerts@thread.tacv2/1761642000001", ev.Key)
				require.Equal(t, string(event.KindAlert), ev.Kind)
				require.True(t, ev.Trusted)
				require.Equal(t, "HighErrorRate", *ev.AlertName)
				require.Equal(t, "[FIRING] HighErrorRate", *ev.Title)
				require.Equal(t, "checkout", *ev.Service)
				require.Equal(t, string(event.SeverityCritical), *ev.Severity)
				require.JSONEq(t, `{"env": "prod"}`, string(ev.Labels))
				require.Equal(t, event.Fingerprint("alerts", "HighErrorRate", map[string]string{"env": "prod"}, []string{"env"}), ev.Fingerprint)
				require.Equal(t, ingest.StatusReceived, ev.Status)
				require.JSONEq(t, `[{"title": "View in Grafana", "url": "https://grafana.example.com/alerting/7"}]`, string(ev.Links))
				require.Equal(t, 1, h.routeJobs(t))

				got, err := store.EventOf(ev)
				require.NoError(t, err)
				require.Equal(t, &event.TeamsRef{
					TenantID: testTenantID, TeamID: testTeamID, ChannelID: "19:alerts@thread.tacv2",
					MessageID: "1761642000001", ThreadID: "1761642000001",
					UserID: "28:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", ServiceURL: testServiceURL,
				}, got.Teams)
				require.Equal(t, event.Sender{Kind: event.SenderBot, ID: "28:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "Grafana"}, got.Sender)

				require.InDelta(t, 1, h.counter(t, "sidekick_events_received_total", "alerts", "result", ingest.ResultAccepted), 0)

				spans := h.spans.Ended()
				require.Equal(t, SpanName, spans[len(spans)-1].Name())
			},
		},
		{
			name:       "a bot that is not in trustedBots gives an untrusted alert",
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
			name:       "an activity sent again stores one event",
			before:     []request{{file: "grafana-firing"}},
			send:       request{file: "grafana-firing"},
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
			before:     []request{{file: "grafana-firing"}},
			send:       request{file: "grafana-resolved"},
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
			name:       "a person's message is stored redacted and untrusted, as plain text",
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
				require.Contains(t, *ev.Body, "Checkout returns 502 for some users")
				require.NotContains(t, *ev.Body, "<p>")
				require.Equal(t, "ffffffff-0000-1111-2222-333333333333", *ev.SenderID)
				require.JSONEq(t, `[{"title": "the logs", "url": "https://logs.example.com/q/1"}]`, string(ev.Links))
				require.Equal(t, event.Fingerprint("teams", "", map[string]string{"channel": "19:ops@thread.tacv2", "message": "1761642000004"}, []string{"channel", "message"}), ev.Fingerprint)
			},
		},
		{
			name:       "a top-level mention investigates its own text",
			send:       request{file: "mention-top"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, config.TeamsMentionIntake, ev.Intake)
				require.Equal(t, "19:ops@thread.tacv2/1761642000005", ev.Key)
				require.Equal(t, string(event.KindMention), ev.Kind)
				require.False(t, ev.Trusted)
				require.Equal(t, "why is checkout slow?", *ev.Body)
			},
		},
		{
			name:       "a mention in a thread whose first message a listener stored uses that event, without Graph",
			before:     []request{{file: "grafana-firing"}},
			send:       request{file: "mention-thread"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				events := h.events(t)
				require.Len(t, events, 2)

				ev := events[1]
				require.Equal(t, config.TeamsMentionIntake, ev.Intake)
				require.Equal(t, "19:alerts@thread.tacv2/1761642000006", ev.Key)
				require.Equal(t, string(event.KindMention), ev.Kind)
				require.False(t, ev.Trusted, "a mention is never trusted, whoever posted the message")
				require.Equal(t, "[FIRING] HighErrorRate", *ev.Title)
				require.Equal(t, "checkout", *ev.Service)
				require.Equal(t, events[0].Fingerprint, ev.Fingerprint)

				got, err := store.EventOf(ev)
				require.NoError(t, err)
				require.Equal(t, "1761642000001", got.Teams.ThreadID, "the reply goes in the thread")

				for _, req := range h.microsoft.Requests() {
					require.NotContains(t, req.Path, graphPrefix)
				}
			},
		},
		{
			name: "a mention in a thread whose first message is not stored reads it through Graph",
			microsoft: func(t *testing.T, h *harness) {
				t.Helper()

				h.microsoft.Handle(http.MethodPost, tokenPath, fakehttp.Response{BodyFile: "fixture/graph/token.json"})
				h.microsoft.Handle(http.MethodGet, graphMessagePath("1761642000009"), fakehttp.Response{BodyFile: "fixture/graph/grafana-firing.json"})
				h.microsoft.ExpectAllCalled()
			},
			send:       request{file: "mention-thread-unstored"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, config.TeamsMentionIntake, ev.Intake)
				require.Equal(t, string(event.KindMention), ev.Kind)
				require.False(t, ev.Trusted)
				require.Equal(t, "[FIRING] LatencyHigh", *ev.Title)
				require.Equal(t, "search", *ev.Service)
				require.Equal(t, event.Fingerprint("alerts", "LatencyHigh", map[string]string{"env": "prod"}, []string{"env"}), ev.Fingerprint)

				got, err := store.EventOf(ev)
				require.NoError(t, err)
				require.Equal(t, "1761642000009", got.Teams.ThreadID)

				var graph, token *fakehttp.Request

				for _, req := range h.microsoft.Requests() {
					switch req.Path {
					case tokenPath:
						token = &req
					case graphMessagePath("1761642000009"):
						graph = &req
					}
				}

				require.NotNil(t, graph)
				require.Equal(t, "Bearer graph-test-token", graph.Header.Get("Authorization"))

				require.NotNil(t, token)

				form, err := url.ParseQuery(string(token.Body))
				require.NoError(t, err)
				require.Equal(t, "https://graph.microsoft.com/.default", form.Get("scope"))
				require.Equal(t, testAppID, form.Get("client_id"))
				require.Equal(t, "test-client-secret", form.Get("client_secret"))
			},
		},
		{
			name: "a mention in a thread Graph may not read investigates the mention's own text",
			microsoft: func(t *testing.T, h *harness) {
				t.Helper()

				h.microsoft.Handle(http.MethodPost, tokenPath, fakehttp.Response{BodyFile: "fixture/graph/token.json"})
				h.microsoft.Handle(http.MethodGet, graphMessagePath("1761642000009"), fakehttp.Response{Status: http.StatusForbidden, BodyFile: "fixture/graph/forbidden.json"})
			},
			send:       request{file: "mention-thread-unstored"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				ev := onlyEvent(t, h)
				require.Equal(t, config.TeamsMentionIntake, ev.Intake)
				require.Equal(t, "and this one?", *ev.Body)
			},
		},
		{
			name: "a mention in a thread the bot started is left for follow-ups",
			microsoft: func(t *testing.T, h *harness) {
				t.Helper()

				h.microsoft.Handle(http.MethodPost, tokenPath, fakehttp.Response{BodyFile: "fixture/graph/token.json"})
				h.microsoft.Handle(http.MethodGet, graphMessagePath("1761642000009"), fakehttp.Response{BodyFile: "fixture/graph/own-message.json"})
			},
			send:       request{file: "mention-thread-unstored"},
			wantStatus: http.StatusOK,
			check:      requireNoEvents,
		},
		{
			name: "a mention in a thread is not answered with 200 when Graph fails",
			microsoft: func(t *testing.T, h *harness) {
				t.Helper()

				h.microsoft.Handle(http.MethodPost, tokenPath, fakehttp.Response{BodyFile: "fixture/graph/token.json"})
				h.microsoft.Handle(http.MethodGet, graphMessagePath("1761642000009"), fakehttp.Response{Status: http.StatusServiceUnavailable})
			},
			send:       request{file: "mention-thread-unstored"},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"error":"internal error"}`,
			check:      requireNoEvents,
		},
		{
			name:       "an activity from another tenant is refused",
			send:       request{file: "other-tenant"},
			wantStatus: http.StatusForbidden,
			wantBody:   `{"error":"forbidden"}`,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				requireNoEvents(t, h)
				require.InDelta(t, 1, h.counter(t, "sidekick_events_rejected_total", rejectedIntake, "reason", ingest.ReasonUnauthenticated), 0)
			},
		},
		{name: "an activity whose channel data names another tenant is refused", send: request{file: "mixed-tenant"}, wantStatus: http.StatusForbidden, check: requireNoEvents},
		{name: "an activity without a tenant is refused", send: request{file: "no-tenant"}, wantStatus: http.StatusForbidden, check: requireNoEvents},
		{name: "an activity for another bot is refused", send: request{file: "other-recipient"}, wantStatus: http.StatusForbidden, check: requireNoEvents},
		{
			name:       "a token signed with another key is refused",
			send:       request{file: "grafana-firing", auth: func(t *testing.T) string { t.Helper(); return bearer(t, otherKey, nil) }},
			wantStatus: http.StatusUnauthorized,
			wantBody:   `{"error":"unauthenticated"}`,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				requireNoEvents(t, h)
				require.InDelta(t, 1, h.counter(t, "sidekick_events_rejected_total", rejectedIntake, "reason", ingest.ReasonUnauthenticated), 0)
			},
		},
		{
			name: "a token for another service URL is refused",
			send: request{file: "grafana-firing", auth: func(t *testing.T) string {
				t.Helper()

				return bearer(t, signingKey(), func(c *tokenClaims) { c.ServiceURL = "https://attacker.example.com/" })
			}},
			wantStatus: http.StatusUnauthorized,
			check:      requireNoEvents,
		},
		{name: "an activity without a token is refused", send: request{file: "grafana-firing", auth: func(*testing.T) string { return "" }}, wantStatus: http.StatusUnauthorized, check: requireNoEvents},
		{
			name: "an activity is not answered with 200 when the signing keys cannot be read",
			microsoft: func(_ *testing.T, h *harness) {
				h.setKeysDown(true)
			},
			send:       request{file: "grafana-firing"},
			wantStatus: http.StatusInternalServerError,
			check:      requireNoEvents,
		},
		{name: "a thread reply without a mention is ignored", send: request{file: "thread-reply"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "the bot's own message is ignored", send: request{file: "own-message"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "an edit is ignored", send: request{file: "edit"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "a conversation update is ignored", send: request{file: "conversation-update"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{name: "a group chat message is ignored", send: request{file: "group-chat"}, wantStatus: http.StatusOK, check: requireNoEvents},
		{
			name:       "a body that is not JSON is rejected",
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
			if tt.microsoft != nil {
				tt.microsoft(t, h)
			}

			for _, r := range tt.before {
				status, body := h.post(t, r.authorization(t), r.content(t))
				require.Equal(t, http.StatusOK, status, body)
			}

			status, body := h.post(t, tt.send.authorization(t), tt.send.content(t))
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

	return readFixture(t, "activities/"+r.file+".json")
}

// authorization returns the Authorization header of r: its own, or a
// valid token.
func (r request) authorization(t *testing.T) string {
	t.Helper()

	if r.auth != nil {
		return r.auth(t)
	}

	return bearer(t, signingKey(), nil)
}

// graphMessagePath is the Graph path of message id in the alerts channel
// of the test team.
func graphMessagePath(id string) string {
	return graphPrefix + "teams/" + testTeamID + "/channels/19:alerts@thread.tacv2/messages/" + id
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
