//go:build integration

package slackcmd_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// messageLink links to the message in fixture/slack/replies-human.json.
const messageLink = "https://acme.slack.com/archives/C01RANDOM/p1761572520123456"

func TestCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user string
		text string
		// slack is the response file of conversations.replies, if the
		// command reads a message.
		slack        string
		wantContains []string
		wantAudit    []string
		wantMentions int
	}{
		{
			name:         "help",
			user:         viewer,
			text:         "help",
			wantContains: []string{"`/sidekick listen #channel", "`/sidekick route set <name> <field> <value>`"},
		},
		{
			name:         "no subcommand shows the help",
			user:         viewer,
			text:         "",
			wantContains: []string{"*/sidekick* commands:"},
		},
		{
			name:         "an unknown subcommand",
			user:         admin,
			text:         "lissen #payments",
			wantContains: []string{"There is no `/sidekick lissen` command."},
		},
		{
			name: "listen",
			user: responder,
			text: `listen <#C01PAYMENTS|payments> bot:Grafana pattern:"(?i)firing now"`,
			wantContains: []string{
				"Listening in <#C01PAYMENTS> as listener `payments`: messages from a bot, sent by `Grafana`, matching `(?i)firing now`.",
			},
			wantAudit: []string{"listener.created listener:payments by " + responder},
		},
		{
			name:         "listen with the name of a file listener",
			user:         responder,
			text:         "listen #alerts",
			wantContains: []string{"A listener named `alerts` exists already."},
		},
		{
			name:         "listen with a pattern that does not parse",
			user:         responder,
			text:         "listen #payments pattern:(firing",
			wantContains: []string{"That does not pass the config checks:", "listeners.payments.match.text: invalid regular expression"},
		},
		{
			name:         "listen needs the responder role",
			user:         viewer,
			text:         "listen #payments",
			wantContains: []string{"This needs the responder role, and you are a viewer."},
		},
		{
			name:         "unlisten",
			user:         responder,
			text:         "unlisten billing",
			wantContains: []string{"Listener `billing` removed."},
			wantAudit:    []string{"listener.deleted listener:billing by " + responder},
		},
		{
			name:         "unlisten a file listener",
			user:         responder,
			text:         "unlisten alerts",
			wantContains: []string{"Listener `alerts` is set in the Sidekick config file"},
		},
		{
			name: "listeners",
			user: viewer,
			text: "listeners",
			wantContains: []string{
				"• `alerts` (config file) in #alerts: messages from a bot, sent by `SigNoz`",
				"• `billing` (added from Slack) in <#C01BILLING>: messages from a bot, sent by `Grafana`",
			},
		},
		{
			name: "routes",
			user: viewer,
			text: "routes",
			wantContains: []string{
				"1. `checkout` (added from Slack, priority 20): matches service `checkout-*`",
				"2. `payments` (config file, priority 50): matches service `payments-*`",
				"3. `default` (config file, priority 100): matches every event",
			},
		},
		{
			name:         "route set adds a route",
			user:         admin,
			text:         "route set search service search-*",
			wantContains: []string{"Route `search` added: service is now `search-*`."},
			wantAudit:    []string{"route.created route:search by " + admin},
		},
		{
			name:         "route set changes a route",
			user:         admin,
			text:         "route set checkout priority 5",
			wantContains: []string{"Route `checkout` updated: priority is now `5`."},
			wantAudit:    []string{"route.updated route:checkout by " + admin},
		},
		{
			name:         "route set needs the admin role",
			user:         responder,
			text:         "route set checkout priority 5",
			wantContains: []string{"This needs the admin role, and you are a responder."},
		},
		{
			name:         "route set with an unknown field",
			user:         admin,
			text:         "route set checkout colour blue",
			wantContains: []string{"`colour` is not a field Slack can set.", "`maxToolCalls`"},
		},
		{
			name: "route set with a budget above the file ceiling",
			user: admin,
			text: "route set checkout maxToolCalls 100",
			wantContains: []string{
				"That does not pass the config checks:\n• routes.checkout.budgets.maxToolCalls: 100 is above the global ceiling budgets.perInvestigation.maxToolCalls 40",
			},
		},
		{
			name:         "route set with a mode other than read-only",
			user:         admin,
			text:         "route set checkout mode approved",
			wantContains: []string{"Slack can set only mode `read-only` for now"},
		},
		{
			name:         "route set on a file route",
			user:         admin,
			text:         "route set payments priority 1",
			wantContains: []string{"Route `payments` is set in the Sidekick config file"},
		},
		{
			name:         "investigate",
			user:         responder,
			text:         "investigate " + messageLink,
			slack:        "fixture/slack/replies-human.json",
			wantContains: []string{"Sidekick is looking into that message and replies in its thread."},
			wantMentions: 1,
		},
		{
			name:         "investigate a message Sidekick cannot read",
			user:         responder,
			text:         "investigate " + messageLink,
			slack:        "fixture/slack/replies-not-in-channel.json",
			wantContains: []string{"Sidekick cannot read that message. Invite Sidekick to its channel"},
		},
		{
			name:         "investigate something that is not a link",
			user:         responder,
			text:         "investigate checkout",
			wantContains: []string{"`checkout` is not a link to a Slack message."},
		},
		{
			name:         "investigate needs the responder role",
			user:         viewer,
			text:         "investigate " + messageLink,
			wantContains: []string{"This needs the responder role, and you are a viewer."},
		},
		{
			name: "status",
			user: admin,
			text: "status",
			wantContains: []string{
				"*Sidekick* v0.0.0-test",
				"Your role: admin.",
				"Listeners: 2 (1 from the config file, 1 added from Slack).",
				"Routes: 3 (2 from the config file, 1 added from Slack).",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			if tt.slack != "" {
				h.slack.Handle(http.MethodGet, "/conversations.replies", fakehttp.Response{BodyFile: tt.slack, Headers: map[string]string{"Content-Type": "application/json"}})
				h.slack.ExpectAllCalled()
			}

			status, reply := h.command(t, tt.user, tt.text)
			require.Equal(t, http.StatusOK, status, reply)

			for _, want := range tt.wantContains {
				require.Contains(t, reply, want)
			}

			require.Equal(t, tt.wantAudit, h.auditActions(t), "each change writes exactly one audit row")
			require.Equal(t, tt.wantMentions, h.mentionEvents(t))
		})
	}
}

func TestCommandsReachOtherReplicas(t *testing.T) {
	t.Parallel()

	h := setup(t)

	_, reply := h.command(t, admin, "route set search service search-*")
	require.Contains(t, reply, "Route `search` added")

	// The harness cache does not run, so it sees the change on its next
	// reload, as a replica does on the notification.
	require.NoError(t, h.cache.Reload(t.Context()))

	_, reply = h.command(t, viewer, "routes")
	require.Contains(t, reply, "`search` (added from Slack, priority 100): matches service `search-*`")
}

func TestCommandRequestChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		header     func(h *harness, body []byte) http.Header
		wantStatus int
	}{
		{
			name: "a wrong signature",
			header: func(h *harness, body []byte) http.Header {
				header := h.sign(time.Now(), body)
				header.Set("X-Slack-Signature", "v0=00")

				return header
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "an old timestamp",
			header: func(h *harness, body []byte) http.Header {
				return h.sign(time.Now().Add(-time.Hour), body)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no signature",
			header:     func(*harness, []byte) http.Header { return http.Header{} },
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			body := h.form(admin, "route set search service search-*")

			status, _ := h.post(t, tt.header(h, body), body)
			require.Equal(t, tt.wantStatus, status)
			require.Empty(t, h.auditActions(t))
		})
	}
}
