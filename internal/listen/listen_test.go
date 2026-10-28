package listen

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
)

func TestFields(t *testing.T) {
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
		want    Fields
	}{
		{name: "no patterns", text: "Alert: Disk", want: Fields{Severity: event.SeverityUnknown}},
		{
			name:    "every field",
			extract: extract,
			text:    "Alert: HighErrorRate firing\nservice=checkout severity: CRIT env=prod",
			want:    Fields{Alert: "HighErrorRate", Service: "checkout", Severity: event.SeverityCritical, Labels: map[string]string{"env": "prod"}},
		},
		{
			name:    "the first pattern that sets a field wins, and empty groups set nothing",
			extract: extract,
			text:    "service=checkout later-api region=",
			want:    Fields{Service: "checkout", Severity: event.SeverityUnknown},
		},
		{
			name:    "an unknown severity name",
			extract: extract,
			text:    "severity: purple",
			want:    Fields{Severity: event.SeverityUnknown},
		},
		{name: "nothing matches", extract: extract, text: "hello", want: Fields{Severity: event.SeverityUnknown}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := Compile("alerts", config.Listener{Extract: tt.extract})
			require.NoError(t, err)
			require.Equal(t, tt.want, r.Fields(tt.text))
		})
	}
}

func TestIsRecovery(t *testing.T) {
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

			r, err := Compile("alerts", config.Listener{Recovery: tt.recovery})
			require.NoError(t, err)
			require.Equal(t, tt.want, r.IsRecovery(tt.text))
		})
	}
}

func TestApply(t *testing.T) {
	t.Parallel()

	r, err := Compile("alerts", config.Listener{
		Extract:  []string{`Alert: (?P<alert>[A-Za-z]+)`, `env=(?P<env>[a-z]+)`},
		Recovery: "(?i)resolved",
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		text string
		want event.Event
	}{
		{
			name: "extracted fields set the fingerprint",
			text: "Alert: Disk firing env=prod",
			want: event.Event{
				Kind: event.KindAlert, AlertName: "Disk", Severity: event.SeverityUnknown, Labels: map[string]string{"env": "prod"},
				Fingerprint: event.Fingerprint("alerts", "Disk", map[string]string{"env": "prod"}, []string{"env"}),
			},
		},
		{
			name: "a recovery keeps the alert's fingerprint",
			text: "Alert: Disk resolved env=prod",
			want: event.Event{
				Kind: event.KindResolved, AlertName: "Disk", Severity: event.SeverityUnknown, Labels: map[string]string{"env": "prod"},
				Fingerprint: event.Fingerprint("alerts", "Disk", map[string]string{"env": "prod"}, []string{"env"}),
			},
		},
		{
			name: "without fields the message's own fingerprint stays",
			text: "checkout is slow",
			want: event.Event{Kind: event.KindAlert, Severity: event.SeverityUnknown, Fingerprint: "own"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ev := event.Event{Kind: event.KindAlert, Fingerprint: "own"}
			r.Apply(&ev, tt.text)
			require.Equal(t, tt.want, ev)
		})
	}
}

func TestCompileAll(t *testing.T) {
	t.Parallel()

	cfg := map[string]config.Listener{
		"zz":    {},
		"aa":    {Source: config.ListenerSourceSlack},
		"teams": {Source: config.ListenerSourceTeams, Channel: "19:a@thread.tacv2", Trusted: true},
	}

	tests := []struct {
		source    config.ListenerSource
		wantNames []string
	}{
		{source: config.ListenerSourceSlack, wantNames: []string{"aa", "zz"}},
		{source: config.ListenerSourceTeams, wantNames: []string{"teams"}},
	}

	for _, tt := range tests {
		t.Run(string(tt.source), func(t *testing.T) {
			t.Parallel()

			got, err := CompileAll(cfg, tt.source)
			require.NoError(t, err)

			names := []string{}
			for _, r := range got {
				names = append(names, r.Name)
			}

			require.Equal(t, tt.wantNames, names)
		})
	}
}
