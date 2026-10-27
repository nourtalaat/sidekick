package slackapp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFlatText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "plain text", msg: `{"text": "Disk almost full on db-1"}`, want: "Disk almost full on db-1"},
		{
			name: "attachment parts in order, with fields",
			msg: `{"text": "Alert fired", "attachments": [{
				"pretext": "From SigNoz", "author_name": "signoz", "title": "HighErrorRate",
				"text": "Error rate 7%", "fields": [{"title": "service", "value": "checkout"}],
				"footer": "prod", "fallback": "unused"}]}`,
			want: "Alert fired\nFrom SigNoz\nsignoz\nHighErrorRate\nError rate 7%\nservice\ncheckout\nprod",
		},
		{
			name: "an attachment's fallback when it has nothing else",
			msg:  `{"attachments": [{"fallback": "[FIRING] Disk"}]}`,
			want: "[FIRING] Disk",
		},
		{
			name: "section, header and context blocks",
			msg: `{"blocks": [
				{"type": "header", "text": {"type": "plain_text", "text": "Latency alert"}},
				{"type": "section", "text": {"type": "mrkdwn", "text": "p99 is *2.3s*"},
				 "fields": [{"type": "mrkdwn", "text": "env=prod"}]},
				{"type": "context", "elements": [{"type": "mrkdwn", "text": "service=api"}]}]}`,
			want: "Latency alert\np99 is *2.3s*\nenv=prod\nservice=api",
		},
		{
			name: "rich text runs on within a section",
			msg: `{"blocks": [{"type": "rich_text", "elements": [
				{"type": "rich_text_section", "elements": [
					{"type": "text", "text": "Checkout is "}, {"type": "link", "url": "https://x", "text": "down"}]},
				{"type": "rich_text_section", "elements": [{"type": "text", "text": "since 10:02"}]}]}]}`,
			want: "Checkout is down\nsince 10:02",
		},
		{
			name: "blocks that repeat the text are left out",
			msg:  `{"text": "Disk full", "blocks": [{"type": "section", "text": {"type": "mrkdwn", "text": "Disk full"}}]}`,
			want: "Disk full",
		},
		{
			name: "blocks inside an attachment",
			msg:  `{"attachments": [{"blocks": [{"type": "section", "text": {"type": "mrkdwn", "text": "inside"}}]}]}`,
			want: "inside",
		},
		{name: "blocks that do not decode add nothing", msg: `{"text": "t", "blocks": "nope"}`, want: "t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var m message
			require.NoError(t, json.Unmarshal([]byte(tt.msg), &m))
			require.Equal(t, tt.want, m.flatText())
		})
	}
}

func TestMessageKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		msg         message
		wantPost    bool
		wantReply   bool
		wantBot     bool
		wantFromApp bool
	}{
		{name: "a person's post", msg: message{User: "U1", TS: "1.1"}, wantPost: true},
		{name: "a bot's post", msg: message{Subtype: subtypeBot, BotID: "B1", TS: "1.1"}, wantPost: true, wantBot: true},
		{name: "a bot posting with a user", msg: message{User: "U2", BotID: "B1", TS: "1.1"}, wantPost: true, wantBot: true},
		{name: "an edit", msg: message{Subtype: "message_changed", TS: "1.1"}},
		{name: "a reply", msg: message{User: "U1", TS: "1.2", ThreadTS: "1.1"}, wantPost: true, wantReply: true},
		{name: "a thread's first message", msg: message{User: "U1", TS: "1.1", ThreadTS: "1.1"}, wantPost: true},
		{name: "a reply sent to the channel too", msg: message{Subtype: subtypeBroadcast, TS: "1.2", ThreadTS: "1.1"}, wantPost: true, wantReply: true},
		{name: "posted by this app", msg: message{BotID: "B9", AppID: "A1", TS: "1"}, wantPost: true, wantBot: true, wantFromApp: true},
		{name: "posted by this app, named in the bot profile", msg: message{BotID: "B9", BotProfile: &botProfile{AppID: "A1"}, TS: "1"}, wantPost: true, wantBot: true, wantFromApp: true},
		{name: "posted as the bot user", msg: message{User: "UBOT", TS: "1"}, wantPost: true, wantFromApp: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantPost, tt.msg.isPost(), "isPost")
			require.Equal(t, tt.wantReply, tt.msg.isThreadReply(), "isThreadReply")
			require.Equal(t, tt.wantBot, tt.msg.isBot(), "isBot")
			require.Equal(t, tt.wantFromApp, tt.msg.fromApp("A1", "UBOT"), "fromApp")
		})
	}
}

func TestLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  message
		want []link
	}{
		{name: "none", msg: message{Text: "plain"}},
		{
			name: "attachment title links, then links in the text, without repeats",
			msg: message{
				Text:        "see <https://grafana.example.com/d/1|dashboard> and <https://runbooks.example.com/disk> and <https://grafana.example.com/d/1>",
				Attachments: []attachment{{Title: "HighErrorRate", TitleLink: "https://signoz.example.com/alerts/7"}},
			},
			want: []link{
				{url: "https://signoz.example.com/alerts/7", title: "HighErrorRate"},
				{url: "https://grafana.example.com/d/1", title: "dashboard"},
				{url: "https://runbooks.example.com/disk"},
			},
		},
		{name: "mentions and channel links are not links", msg: message{Text: "<@U1> in <#C1|alerts> <mailto:a@b.c|mail>"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.msg.links(tt.msg.flatText()))
		})
	}
}
