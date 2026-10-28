//go:build integration

package slackapp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/slackroles"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// followUpInvestigation is the investigation in fixture/investigation.sql.
var followUpInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000001c1")

func TestFollowUpQuestions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// anyReply sets slack.followUps to any.
		anyReply bool
		send     []string
		// want are the texts of the questions in the inbox, oldest first.
		want []string
		// wantAuthor is the Slack user of the questions.
		wantAuthor string
		// roles finds the askers' roles; nil lets everyone ask.
		roles func(t *testing.T, refreshes *int) roles.Lookup
		// wantRefused expects the note that refuses the question, and
		// wantRefreshes how often the role was read again.
		wantRefused   bool
		wantRefreshes int
	}{
		{
			name: "a viewer's question is refused with a note only they see",
			roles: func(t *testing.T, _ *int) roles.Lookup {
				t.Helper()

				lookup, err := slackroles.New(slackroles.Options{Slack: &config.Slack{RoleUsers: map[string]config.RoleName{"U03BOB": config.RoleViewer}}})
				require.NoError(t, err)

				return lookup
			},
			send:        []string{"follow-up-mention"},
			wantRefused: true,
		},
		{
			name: "a viewer whose role is read again as responder asks",
			roles: func(_ *testing.T, n *int) roles.Lookup {
				return refreshingRoles{role: roles.Viewer, refreshed: roles.Responder, refreshes: n}
			},
			send:          []string{"follow-up-mention"},
			want:          []string{"did the deploy cause it?"},
			wantAuthor:    "U03BOB",
			wantRefreshes: 1,
		},
		{
			name: "a responder asks without the role being read again",
			roles: func(_ *testing.T, n *int) roles.Lookup {
				return refreshingRoles{role: roles.Responder, refreshed: roles.Viewer, refreshes: n}
			},
			send:       []string{"follow-up-mention"},
			want:       []string{"did the deploy cause it?"},
			wantAuthor: "U03BOB",
		},
		{
			name:       "a reply that mentions Sidekick is a follow-up question, without the mention and redacted",
			send:       []string{"follow-up-mention"},
			want:       []string{"did the deploy cause it? The token was [REDACTED:seeded:"},
			wantAuthor: "U03BOB",
		},
		{
			name:       "the same reply sent again is asked once",
			send:       []string{"follow-up-mention", "follow-up-mention"},
			want:       []string{"did the deploy cause it? The token was [REDACTED:seeded:"},
			wantAuthor: "U03BOB",
		},
		{name: "a mention with nothing to ask asks nothing", send: []string{"follow-up-bare-mention"}},
		{name: "with followUps mention, a reply without a mention is not a question", send: []string{"thread-reply"}},
		{
			name:       "with followUps any, every reply of a person is a question",
			anyReply:   true,
			send:       []string{"thread-reply"},
			want:       []string{"any update?"},
			wantAuthor: "U02ALICE",
		},
		{name: "a bot's reply is not a question", anyReply: true, send: []string{"follow-up-bot-reply"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			storetest.LoadFixture(t, h.pool, "fixture/investigation.sql")
			h.handler.followUps.anyReply = tt.anyReply

			refreshes := 0
			if tt.roles != nil {
				h.handler.followUps.roles = tt.roles(t, &refreshes)
			}

			if tt.wantRefused {
				h.slack.Handle(http.MethodGet, "/"+methodEphemeral, fakehttp.Response{Body: []byte(`{"ok":true}`)})
				h.slack.ExpectAllCalled()
			}

			for _, file := range tt.send {
				status, body := h.post(t, request{file: file}.headers(t), request{file: file}.content(t))
				require.Equal(t, http.StatusOK, status, body)
			}

			items := followUpItems(t, h)
			require.Len(t, items, len(tt.want))

			for i, want := range tt.want {
				var body struct {
					Text     string `json:"text"`
					AuthorID string `json:"author_id"`
				}
				require.NoError(t, json.Unmarshal(items[i].Body, &body))
				require.Contains(t, body.Text, want)
				require.NotContains(t, body.Text, "SEEDED-ABCD1234")
				require.NotContains(t, body.Text, "<@U01SIDEKICK>")
				require.Equal(t, tt.wantAuthor, body.AuthorID)
				require.NotNil(t, items[i].Key)
			}

			require.Len(t, h.events(t), 1, "a reply in the thread of an investigation is no new event")
			require.Zero(t, h.routeJobs(t))
			require.Equal(t, min(len(tt.want), 1), followUpJobs(t, h), "an investigation that has ended gets one follow_up job")
			require.Equal(t, tt.wantRefreshes, refreshes)

			if !tt.wantRefused {
				require.Empty(t, h.slack.Requests(), "the thread's first message is not fetched")

				return
			}

			calls := h.slack.Requests()
			require.Len(t, calls, 1)
			require.Equal(t, "/"+methodEphemeral, calls[0].Path)
			require.Equal(t, "C01OPS", calls[0].Query.Get("channel"))
			require.Equal(t, "U03BOB", calls[0].Query.Get("user"))
			require.Equal(t, "1761472960.000500", calls[0].Query.Get("thread_ts"))
			require.Equal(t, noteQuestionDenied, calls[0].Query.Get("text"))
		})
	}
}

// followUpItems returns the follow-up questions in the inbox of
// followUpInvestigation, oldest first.
func followUpItems(t *testing.T, h *harness) []db.InvestigationInbox {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM investigation_inbox WHERE investigation_id = $1 AND kind = $2 ORDER BY id",
		followUpInvestigation, investigator.InboxFollowUp)
	require.NoError(t, err)

	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.InvestigationInbox])
	require.NoError(t, err)

	return items
}

// followUpJobs counts the follow_up jobs.
func followUpJobs(t *testing.T, h *harness) int {
	t.Helper()

	var n int
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'follow_up'").Scan(&n))

	return n
}
