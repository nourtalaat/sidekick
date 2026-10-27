package slackapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
)

func TestBuildEvent(t *testing.T) {
	t.Parallel()

	alerts := config.Listener{
		Extract:  []string{`(?P<alert>HighErrorRate|DiskFull)`, `service=(?P<service>[a-z]+)`, `env=(?P<env>[a-z]+)`},
		Recovery: `(?i)resolved`,
	}

	firing := message{Channel: "C1", TS: "1.1", BotID: "B1", BotProfile: &botProfile{Name: "SigNoz"}, Text: "[FIRING] HighErrorRate service=checkout env=prod"}
	resolved := message{Channel: "C1", TS: "1.9", BotID: "B1", BotProfile: &botProfile{Name: "SigNoz"}, Text: "[RESOLVED] HighErrorRate service=checkout env=prod"}
	otherEnv := message{Channel: "C1", TS: "2.1", BotID: "B1", Text: "[FIRING] HighErrorRate env=staging"}
	plain := message{Channel: "C1", TS: "3.1", User: "U1", Team: "T2", Text: "\n  checkout is slow\nsince 10:00"}

	tests := []struct {
		name     string
		msg      message
		listener *config.Listener
		check    func(t *testing.T, ev event.Event)
	}{
		{
			name:     "a bot alert with extracted fields",
			msg:      firing,
			listener: &alerts,
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.Equal(t, event.KindAlert, ev.Kind)
				require.Equal(t, "Ev1", ev.Key)
				require.Equal(t, event.Sender{Kind: event.SenderBot, ID: "B1", Name: "SigNoz"}, ev.Sender)
				require.Equal(t, "HighErrorRate", ev.AlertName)
				require.Equal(t, "checkout", ev.Service)
				require.Equal(t, map[string]string{"env": "prod"}, ev.Labels)
				require.Equal(t, firing.Text, ev.Title)
				require.Equal(t, &event.SlackRef{TeamID: "T1", Channel: "C1", TS: "1.1"}, ev.Slack)
				require.JSONEq(t, `{"k": 1}`, string(ev.Raw))
				require.Equal(t, event.Fingerprint("alerts", "HighErrorRate", map[string]string{"env": "prod"}, []string{"env"}), ev.Fingerprint)
			},
		},
		{
			name:     "a recovery message shares the alert's fingerprint",
			msg:      resolved,
			listener: &alerts,
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.Equal(t, event.KindResolved, ev.Kind)
				require.Equal(t, buildEvent("Ev0", "T1", firing, firing.flatText(), listenerPtr(t, alerts), nil).Fingerprint, ev.Fingerprint)
			},
		},
		{
			name:     "other label values give another fingerprint",
			msg:      otherEnv,
			listener: &alerts,
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.NotEqual(t, buildEvent("Ev0", "T1", firing, firing.flatText(), listenerPtr(t, alerts), nil).Fingerprint, ev.Fingerprint)
			},
		},
		{
			name: "without a listener, a person's message is keyed by where it is",
			msg:  plain,
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.Equal(t, event.KindMessage, ev.Kind)
				require.Equal(t, event.Sender{Kind: event.SenderHuman, ID: "U1"}, ev.Sender)
				require.Equal(t, "checkout is slow", ev.Title)
				require.Equal(t, event.SeverityUnknown, ev.Severity)
				require.Equal(t, "T2", ev.Slack.TeamID, "the message's own team wins")
				require.Equal(t, event.Fingerprint("slack", "", map[string]string{"channel": "C1", "ts": "3.1"}, []string{"channel", "ts"}), ev.Fingerprint)
			},
		},
		{
			name:     "a listener that extracts nothing keeps the message's own fingerprint",
			msg:      plain,
			listener: &config.Listener{Extract: []string{`(?P<alert>Nope)`}},
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.Equal(t, buildEvent("Ev0", "T1", plain, plain.flatText(), nil, nil).Fingerprint, ev.Fingerprint)
			},
		},
		{
			name: "a long first line is cut, and links are kept",
			msg:  message{Channel: "C1", TS: "4.1", User: "U1", Text: strings.Repeat("x", 300) + " <https://grafana.example.com/d/1|board>"},
			check: func(t *testing.T, ev event.Event) {
				t.Helper()
				require.Len(t, ev.Title, maxTitleBytes)
				require.Equal(t, []event.Link{{Title: "board", URL: "https://grafana.example.com/d/1"}}, ev.Links)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var l *listener
			if tt.listener != nil {
				l = listenerPtr(t, *tt.listener)
			}

			tt.check(t, buildEvent("Ev1", "T1", tt.msg, tt.msg.flatText(), l, json.RawMessage(`{"k": 1}`)))
		})
	}
}

// listenerPtr compiles cfg as a listener named alerts.
func listenerPtr(t *testing.T, cfg config.Listener) *listener {
	t.Helper()

	l := compileOne(t, cfg)

	return &l
}
