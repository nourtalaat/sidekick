//go:build integration

package slackapp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator"
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
	}{
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
			require.Empty(t, h.slack.Requests(), "the thread's first message is not fetched")
			require.Equal(t, min(len(tt.want), 1), followUpJobs(t, h), "an investigation that has ended gets one follow_up job")
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
