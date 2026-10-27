package slackapp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
)

// The messages the listener tests match.
var (
	signozAlert = message{Subtype: subtypeBot, BotID: "B01SIGNOZ", BotProfile: &botProfile{ID: "B01SIGNOZ", Name: "SigNoz"}, Text: "Alert: HighErrorRate firing"}
	personPost  = message{User: "U01ALICE", Text: "checkout looks slow"}
)

func TestListenerMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.Listener
		msg  message
		want bool
	}{
		{name: "an empty match takes every message", msg: personPost, want: true},
		{name: "bot kind takes a bot", cfg: listenerMatch("bot", nil, nil, nil, ""), msg: signozAlert, want: true},
		{name: "bot kind refuses a person", cfg: listenerMatch("bot", nil, nil, nil, ""), msg: personPost},
		{name: "human kind refuses a bot", cfg: listenerMatch("human", nil, nil, nil, ""), msg: signozAlert},
		{name: "a bot name, compared without case", cfg: listenerMatch("", []string{"signoz"}, nil, nil, ""), msg: signozAlert, want: true},
		{name: "another bot name", cfg: listenerMatch("", []string{"Datadog"}, nil, nil, ""), msg: signozAlert},
		{name: "a bot ID", cfg: listenerMatch("", nil, []string{"B01SIGNOZ"}, nil, ""), msg: signozAlert, want: true},
		{name: "a custom bot username", cfg: listenerMatch("", []string{"uptime"}, nil, nil, ""), msg: message{Subtype: subtypeBot, BotID: "B2", Username: "Uptime"}, want: true},
		{name: "a user ID", cfg: listenerMatch("", nil, nil, []string{"U01ALICE"}, ""), msg: personPost, want: true},
		{name: "a person not in users", cfg: listenerMatch("", nil, nil, []string{"U02BOB"}, ""), msg: personPost},
		{name: "a person is not matched by bot lists", cfg: listenerMatch("", []string{"SigNoz"}, []string{"B01SIGNOZ"}, nil, ""), msg: personPost},
		{name: "the text pattern", cfg: listenerMatch("", nil, nil, nil, "(?i)firing"), msg: signozAlert, want: true},
		{name: "the text pattern does not match", cfg: listenerMatch("", nil, nil, nil, "resolved"), msg: signozAlert},
		{
			name: "the text pattern sees attachment text",
			cfg:  listenerMatch("bot", nil, nil, nil, "p99"),
			msg:  message{BotID: "B1", Attachments: []attachment{{Fields: []attachmentField{{Title: "metric", Value: "p99 latency"}}}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := compileOne(t, tt.cfg)
			require.Equal(t, tt.want, l.matches(tt.msg, tt.msg.flatText()))
		})
	}
}

func TestListenerMatchesChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		channel string
		id      string
		chName  string
		want    bool
	}{
		{name: "by ID", channel: "C01ALERTS", id: "C01ALERTS", want: true},
		{name: "another ID", channel: "C01ALERTS", id: "C02OTHER"},
		{name: "by name", channel: "#alerts", id: "C01ALERTS", chName: "alerts", want: true},
		{name: "another name", channel: "#alerts", id: "C01ALERTS", chName: "alerts-test"},
		{name: "by name, with the name unknown", channel: "#alerts", id: "C01ALERTS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := compileOne(t, config.Listener{Channel: tt.channel})
			require.Equal(t, tt.want, l.matchesChannel(tt.id, tt.chName))
			require.Equal(t, tt.channel[0] == '#', l.byName())
		})
	}
}

func TestListenerFields(t *testing.T) {
	t.Parallel()

	extract := []string{
		`Alert: (?P<alert>[A-Za-z]+)`,
		`service=(?P<service>[a-z-]+)`,
		`(?i)severity: (?P<severity>[a-z]+)`,
		`env=(?P<env>[a-z]+)`,
		`(?P<service>later-[a-z]+) region=(?P<region>[a-z0-9-]*)`,
	}

	tests := []struct {
		name    string
		extract []string
		text    string
		want    fields
	}{
		{name: "no patterns", text: "Alert: Disk", want: fields{severity: event.SeverityUnknown}},
		{
			name:    "every field",
			extract: extract,
			text:    "Alert: HighErrorRate firing\nservice=checkout severity: CRIT env=prod",
			want:    fields{alert: "HighErrorRate", service: "checkout", severity: event.SeverityCritical, labels: map[string]string{"env": "prod"}},
		},
		{
			name:    "the first pattern that sets a field wins, and empty groups set nothing",
			extract: extract,
			text:    "service=checkout later-api region=",
			want:    fields{service: "checkout", severity: event.SeverityUnknown},
		},
		{
			name:    "an unknown severity name",
			extract: extract,
			text:    "severity: purple",
			want:    fields{severity: event.SeverityUnknown},
		},
		{name: "nothing matches", extract: extract, text: "hello", want: fields{severity: event.SeverityUnknown}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := compileOne(t, config.Listener{Extract: tt.extract})
			require.Equal(t, tt.want, l.fields(tt.text))
		})
	}
}

func TestListenerRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		recovery string
		text     string
		want     bool
	}{
		{name: "no pattern", text: "Alert resolved"},
		{name: "matches", recovery: "(?i)resolved|recovered", text: "[RESOLVED] HighErrorRate", want: true},
		{name: "does not match", recovery: "(?i)resolved|recovered", text: "[FIRING] HighErrorRate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := compileOne(t, config.Listener{Recovery: tt.recovery})
			require.Equal(t, tt.want, l.isRecovery(tt.text))
		})
	}
}

func TestCompileListeners(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       map[string]config.Listener
		wantNames []string
		wantErr   string
	}{
		{name: "none"},
		{name: "in name order", cfg: map[string]config.Listener{"zz": {}, "aa": {}, "mm": {}}, wantNames: []string{"aa", "mm", "zz"}},
		{
			name:      "Teams listeners are left out",
			cfg:       map[string]config.Listener{"slack": {}, "also-slack": {Source: config.ListenerSourceSlack}, "teams": {Source: config.ListenerSourceTeams}},
			wantNames: []string{"also-slack", "slack"},
		},
		{name: "a text pattern that does not parse", cfg: map[string]config.Listener{"a": {Match: config.ListenerMatch{Text: "("}}}, wantErr: "listener a: match.text"},
		{name: "a recovery pattern that does not parse", cfg: map[string]config.Listener{"a": {Recovery: "("}}, wantErr: "listener a: recovery"},
		{name: "an extract pattern that does not parse", cfg: map[string]config.Listener{"a": {Extract: []string{"("}}}, wantErr: "listener a: extract[0]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := compileListeners(tt.cfg)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			names := []string{}
			for _, l := range got {
				names = append(names, l.name)
			}

			require.Equal(t, append([]string{}, tt.wantNames...), names)
		})
	}
}

// listenerMatch returns a listener config with the given match.
func listenerMatch(kind string, names, bots, users []string, text string) config.Listener {
	return config.Listener{Match: config.ListenerMatch{
		Sender: config.ListenerSender{Kind: event.SenderKind(kind), Names: names, Bots: bots, Users: users},
		Text:   text,
	}}
}

// compileOne compiles cfg as a listener named alerts.
func compileOne(t *testing.T, cfg config.Listener) listener {
	t.Helper()

	ls, err := compileListeners(map[string]config.Listener{"alerts": cfg})
	require.NoError(t, err)

	return ls[0]
}
