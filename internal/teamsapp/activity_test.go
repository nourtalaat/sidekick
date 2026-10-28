package teamsapp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/listen"
)

// The accounts of the unit tests.
var (
	botAccount    = account{ID: "28:00000000-1111-2222-3333-444444444444", Name: "Sidekick"}
	grafanaBot    = account{ID: "28:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "Grafana", Role: "bot"}
	alicePerson   = account{ID: "29:alice", Name: "Alice", AADObjectID: "ffffffff-0000-1111-2222-333333333333"}
	alertsChannel = "19:alerts@thread.tacv2"
)

func TestActivityThread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		a           activity
		wantChannel string
		wantThread  string
		wantReply   bool
	}{
		{
			name:        "a new post names itself in the conversation ID",
			a:           activity{ID: "100", Conversation: conversation{ID: alertsChannel + ";messageid=100"}},
			wantChannel: alertsChannel, wantThread: "100",
		},
		{
			name:        "a reply names the thread's first message",
			a:           activity{ID: "105", Conversation: conversation{ID: alertsChannel + ";messageid=100"}, ReplyToID: "100"},
			wantChannel: alertsChannel, wantThread: "100", wantReply: true,
		},
		{
			name:        "without the thread in the conversation ID, replyToId names it",
			a:           activity{ID: "105", Conversation: conversation{ID: alertsChannel}, ReplyToID: "100"},
			wantChannel: alertsChannel, wantThread: "100", wantReply: true,
		},
		{
			name:        "a post without either is its own thread",
			a:           activity{ID: "100", Conversation: conversation{ID: alertsChannel}},
			wantChannel: alertsChannel, wantThread: "100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantChannel, tt.a.channel())
			require.Equal(t, tt.wantThread, tt.a.thread())
			require.Equal(t, tt.wantReply, tt.a.isThreadReply())
		})
	}
}

func TestActivityFlatText(t *testing.T) {
	t.Parallel()

	mention := entity{Type: entityMention, Mentioned: &botAccount, Text: "<at>Sidekick</at>"}

	tests := []struct {
		name string
		a    activity
		want string
	}{
		{name: "plain text", a: activity{Text: "checkout is slow"}, want: "checkout is slow"},
		{
			name: "lines that repeat inside the text are kept",
			a:    activity{Text: "service=checkout\nstatus: firing\n\nservice=payments\nstatus: firing"},
			want: "service=checkout\nstatus: firing\nservice=payments\nstatus: firing",
		},
		{
			name: "a card part that repeats the text is left out, one that repeats only a line is kept",
			a: activity{Text: "[FIRING] Disk\nstatus: firing", Attachments: []attachment{{ContentType: contentAdaptiveCard, Content: json.RawMessage(`{
				"body": [{"text": "[FIRING] Disk\nstatus: firing"}, {"text": "status: firing"}]
			}`)}}},
			want: "[FIRING] Disk\nstatus: firing\nstatus: firing",
		},
		{
			name: "the bot's mention is left out, other mentions stay",
			a: activity{
				Recipient: botAccount, Text: "<at>Sidekick</at> ask <at>Bob</at> about it",
				Entities: []entity{mention, {Type: entityMention, Mentioned: &account{ID: "29:bob"}, Text: "<at>Bob</at>"}},
			},
			want: "ask Bob about it",
		},
		{
			name: "HTML becomes lines of plain text with entities decoded",
			a:    activity{Text: "<p>p99 &gt; 2s</p><p>on <b>checkout</b><br>since 09:00</p>"},
			want: "p99 > 2s\non checkout\nsince 09:00",
		},
		{
			name: "an Adaptive Card adds its body before its buttons, without repeats",
			a: activity{Attachments: []attachment{{ContentType: contentAdaptiveCard, Content: json.RawMessage(`{
				"type": "AdaptiveCard",
				"actions": [{"type": "Action.OpenUrl", "title": "Open", "url": "https://grafana.example.com/7"}],
				"body": [
					{"type": "TextBlock", "text": "[FIRING] HighErrorRate"},
					{"type": "FactSet", "facts": [{"title": "service", "value": "checkout"}]},
					{"type": "TextBlock", "text": "[FIRING] HighErrorRate"}
				]
			}`)}}},
			want: "[FIRING] HighErrorRate\nservice\ncheckout\nOpen",
		},
		{
			name: "an Office 365 connector card",
			a: activity{Attachments: []attachment{{ContentType: contentO365Connector, Content: json.RawMessage(`{
				"title": "Disk full", "sections": [{"activityTitle": "db-1", "facts": [{"name": "env", "value": "prod"}]}]
			}`)}}},
			want: "Disk full\ndb-1\nprod",
		},
		{
			name: "the HTML version is read only when the message has no text",
			a:    activity{Attachments: []attachment{{ContentType: contentHTML, Content: json.RawMessage(`"<div>from the HTML</div>"`)}}},
			want: "from the HTML",
		},
		{
			name: "the HTML version is skipped when the message has text",
			a:    activity{Text: "from the text", Attachments: []attachment{{ContentType: contentHTML, Content: json.RawMessage(`"<div>from the HTML</div>"`)}}},
			want: "from the text",
		},
		{
			name: "a card given as a JSON string, as Graph gives it",
			a:    activity{Attachments: []attachment{{ContentType: contentAdaptiveCard, Content: json.RawMessage(`"{\"body\":[{\"text\":\"from Graph\"}]}"`)}}},
			want: "from Graph",
		},
		{
			name: "other attachments and content that does not decode add nothing",
			a: activity{Text: "text", Attachments: []attachment{
				{ContentType: "image/png", Content: json.RawMessage(`{"text": "an image"}`)},
				{ContentType: contentAdaptiveCard, Content: json.RawMessage(`{`)},
			}},
			want: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.a.flatText())
		})
	}
}

func TestActivityLinks(t *testing.T) {
	t.Parallel()

	many := make([]any, 0, maxLinks+2)
	for i := range maxLinks + 2 {
		many = append(many, map[string]any{"type": actionOpenURL, "title": "link", "url": "https://example.com/" + string(rune('a'+i))})
	}

	manyCard, err := json.Marshal(map[string]any{"actions": many})
	require.NoError(t, err)

	tests := []struct {
		name string
		a    activity
		want []link
	}{
		{
			name: "anchors in the text, only http and https",
			a:    activity{Text: `<p><a href="https://logs.example.com/1">the logs</a> and <a href="javascript:alert(1)">no</a></p>`},
			want: []link{{url: "https://logs.example.com/1", title: "the logs"}},
		},
		{
			name: "card actions of each kind, without repeats",
			a: activity{
				Text: `<a href="https://grafana.example.com/7">alert</a>`,
				Attachments: []attachment{
					{ContentType: contentAdaptiveCard, Content: json.RawMessage(`{"actions": [{"type": "Action.OpenUrl", "title": "Open", "url": "https://grafana.example.com/7"}]}`)},
					{ContentType: contentHeroCard, Content: json.RawMessage(`{"buttons": [{"type": "openUrl", "title": "Runbook", "value": "https://runbooks.example.com/disk"}]}`)},
					{ContentType: contentO365Connector, Content: json.RawMessage(`{"potentialAction": [{"@type": "OpenUri", "name": "Dashboard", "targets": [{"os": "default", "uri": "https://dash.example.com"}]}]}`)},
				},
			},
			want: []link{
				{url: "https://grafana.example.com/7", title: "alert"},
				{url: "https://runbooks.example.com/disk", title: "Runbook"},
				{url: "https://dash.example.com", title: "Dashboard"},
			},
		},
		{
			name: "at most maxLinks",
			a:    activity{Attachments: []attachment{{ContentType: contentAdaptiveCard, Content: manyCard}}},
			want: func() []link {
				out := make([]link, 0, maxLinks)
				for i := range maxLinks {
					out = append(out, link{url: "https://example.com/" + string(rune('a'+i)), title: "link"})
				}

				return out
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.a.links())
		})
	}
}

func TestListenerMatches(t *testing.T) {
	t.Parallel()

	grafanaAlert := activity{From: grafanaBot, Conversation: conversation{ID: alertsChannel + ";messageid=1"}, Text: "[FIRING] HighErrorRate"}
	alicePost := activity{From: alicePerson, Conversation: conversation{ID: alertsChannel + ";messageid=2"}, Text: "checkout looks slow"}

	sender := func(kind string, names, bots, users []string) config.ListenerMatch {
		return config.ListenerMatch{Sender: config.ListenerSender{Kind: event.SenderKind(kind), Names: names, Bots: bots, Users: users}}
	}

	tests := []struct {
		name    string
		channel string
		match   config.ListenerMatch
		a       activity
		want    bool
	}{
		{name: "an empty match takes every message in the channel", a: alicePost, want: true},
		{name: "another channel", channel: "19:other@thread.tacv2", a: alicePost},
		{name: "bot kind takes a bot", match: sender("bot", nil, nil, nil), a: grafanaAlert, want: true},
		{name: "bot kind refuses a person", match: sender("bot", nil, nil, nil), a: alicePost},
		{name: "human kind refuses a bot", match: sender("human", nil, nil, nil), a: grafanaAlert},
		{name: "a bot name, compared without case", match: sender("", []string{"grafana"}, nil, nil), a: grafanaAlert, want: true},
		{name: "a bot ID, compared without case", match: sender("", nil, []string{"28:AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}, nil), a: grafanaAlert, want: true},
		{name: "another bot ID", match: sender("", nil, []string{"28:00000000-0000-0000-0000-000000000000"}, nil), a: grafanaAlert},
		{name: "a person by Entra object ID", match: sender("", nil, nil, []string{"FFFFFFFF-0000-1111-2222-333333333333"}), a: alicePost, want: true},
		{name: "a person not in users", match: sender("", nil, nil, []string{"00000000-0000-0000-0000-000000000000"}), a: alicePost},
		{name: "a person is not matched by bot lists", match: sender("", []string{"Alice"}, []string{"29:alice"}, nil), a: alicePost},
		{name: "the text pattern", match: config.ListenerMatch{Text: "(?i)firing"}, a: grafanaAlert, want: true},
		{name: "the text pattern does not match", match: config.ListenerMatch{Text: "resolved"}, a: grafanaAlert},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			channel := tt.channel
			if channel == "" {
				channel = alertsChannel
			}

			r, err := listen.Compile("alerts", config.Listener{Source: config.ListenerSourceTeams, Channel: channel, Match: tt.match})
			require.NoError(t, err)

			l := listener{Rules: r}
			require.Equal(t, tt.want, l.matches(tt.a, tt.a.flatText()))
		})
	}
}

func TestBuildEvent(t *testing.T) {
	t.Parallel()

	r, err := listen.Compile("alerts", config.Listener{Extract: []string{`\[[A-Z]+\] (?P<alert>[A-Za-z]+)`, `env=(?P<env>[a-z]+)`}})
	require.NoError(t, err)

	alert := activity{
		ID: "105", ServiceURL: "https://smba.trafficmanager.net/emea/", From: grafanaBot, Recipient: botAccount,
		Conversation: conversation{ID: alertsChannel + ";messageid=100", TenantID: "t1"},
		ChannelData:  channelData{Team: &teamRef{AADGroupID: "g1"}},
	}

	person := alert
	person.From = alicePerson

	tests := []struct {
		name     string
		a        activity
		text     string
		listener *listener
		check    func(t *testing.T, ev event.Event)
	}{
		{
			name:     "a bot's message is an alert with the listener's fields and fingerprint",
			a:        alert,
			text:     "[FIRING] HighErrorRate env=prod\nmore",
			listener: &listener{Rules: r},
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.Equal(t, event.KindAlert, ev.Kind)
				require.Equal(t, "[FIRING] HighErrorRate env=prod", ev.Title)
				require.Equal(t, "HighErrorRate", ev.AlertName)
				require.Equal(t, event.Sender{Kind: event.SenderBot, ID: grafanaBot.ID, Name: "Grafana"}, ev.Sender)
				require.Equal(t, event.Fingerprint("alerts", "HighErrorRate", map[string]string{"env": "prod"}, []string{"env"}), ev.Fingerprint)
				require.Equal(t, &event.TeamsRef{
					TenantID: "t1", TeamID: "g1", ChannelID: alertsChannel, MessageID: "105", ThreadID: "100",
					UserID: grafanaBot.ID, ServiceURL: "https://smba.trafficmanager.net/emea/",
				}, ev.Teams)
			},
		},
		{
			name: "a person's message without a listener has the message's own fingerprint",
			a:    person,
			text: "checkout looks slow",
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.Equal(t, event.KindMessage, ev.Kind)
				require.Equal(t, event.Sender{Kind: event.SenderHuman, ID: alicePerson.AADObjectID}, ev.Sender)
				require.Equal(t, event.Fingerprint("teams", "", map[string]string{"channel": alertsChannel, "message": "105"}, []string{"channel", "message"}), ev.Fingerprint)
				require.Equal(t, event.SeverityUnknown, ev.Severity)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ev := buildEvent("key", tt.a, tt.text, tt.listener, json.RawMessage(`{}`))
			require.Equal(t, "key", ev.Key)
			tt.check(t, ev)
		})
	}
}
