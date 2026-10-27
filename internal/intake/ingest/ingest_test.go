package ingest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/redact"
)

func TestStoredKey(t *testing.T) {
	t.Parallel()

	w := &Writer{redactor: testRedactor(t)}

	tests := []struct {
		name     string
		key      string
		wantKept bool
	}{
		{name: "a plain key is kept", key: "build-4711", wantKept: true},
		{name: "a key at the length limit is kept", key: strings.Repeat("k", maxKeyBytes), wantKept: true},
		{name: "a longer key is hashed", key: strings.Repeat("k", maxKeyBytes+1)},
		{name: "a key the redactor changes is hashed", key: "deploy-SEEDED-1"},
		{name: "a key with a NUL character is hashed", key: "a\x00b"},
		{name: "a key that is not valid UTF-8 is hashed", key: "a\xffb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := w.StoredKey(tt.key)
			if tt.wantKept {
				require.Equal(t, tt.key, got)

				return
			}

			require.True(t, strings.HasPrefix(got, hashedKeyPrefix))
			require.Len(t, got, len(hashedKeyPrefix)+64)
			require.Equal(t, got, w.StoredKey(tt.key), "the same key gives the same hash")
		})
	}
}

func TestRedactedLinks(t *testing.T) {
	t.Parallel()

	redactor := testRedactor(t)
	w := &Writer{redactor: redactor}

	tests := []struct {
		name  string
		links []event.Link
		want  string
	}{
		{name: "no links", want: ""},
		{
			name:  "links are kept with their titles",
			links: []event.Link{{Title: "Runbook", URL: "https://runbooks.example.com/disk"}, {URL: "https://grafana.example.com/d/1"}},
			want:  `[{"title": "Runbook", "url": "https://runbooks.example.com/disk"}, {"title": "", "url": "https://grafana.example.com/d/1"}]`,
		},
		{
			name:  "a title is redacted",
			links: []event.Link{{Title: "Dashboard SEEDED-1", URL: "https://grafana.example.com/d/1"}},
			want:  `[{"title": "Dashboard ` + redactor.Text("SEEDED-1").Text + `", "url": "https://grafana.example.com/d/1"}]`,
		},
		{
			name: "a link whose URL redaction changes is dropped",
			links: []event.Link{
				{Title: "Leaky", URL: "https://example.com/?token=SEEDED-2"},
				{Title: "Kept", URL: "https://example.com/ok"},
			},
			want: `[{"title": "Kept", "url": "https://example.com/ok"}]`,
		},
		{
			name:  "links without a usable URL are dropped",
			links: []event.Link{{Title: "Empty"}, {Title: "NUL", URL: "https://example.com/\x00"}},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := w.redactedLinks(tt.links)
			require.NoError(t, err)

			if tt.want == "" {
				require.Nil(t, got)

				return
			}

			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestRow(t *testing.T) {
	t.Parallel()

	redactor := testRedactor(t)
	w := &Writer{redactor: redactor}

	tests := []struct {
		name  string
		d     Delivery
		ev    event.Event
		check func(t *testing.T, ev event.Event, got rowView)
	}{
		{
			name: "trust comes from the delivery, not the event",
			d:    Delivery{Intake: "alerts", Trusted: false},
			ev:   event.Event{Key: "k", Kind: event.KindAlert, Trust: event.Trusted},
			check: func(t *testing.T, _ event.Event, got rowView) {
				t.Helper()
				require.False(t, got.trusted)
				require.Equal(t, StatusReceived, got.status)
				require.Equal(t, "unknown", got.severity)
			},
		},
		{
			name: "an event's own fingerprint is kept",
			d:    Delivery{Intake: "alerts", Trusted: true},
			ev:   event.Event{Key: "k", Kind: event.KindAlert, Fingerprint: "abc"},
			check: func(t *testing.T, _ event.Event, got rowView) {
				t.Helper()
				require.True(t, got.trusted)
				require.Equal(t, "abc", got.fingerprint)
			},
		},
		{
			name: "without a fingerprint, it is built from the intake, alert name and labels",
			d:    Delivery{Intake: "alerts"},
			ev:   event.Event{Key: "k", Kind: event.KindAlert, AlertName: "Disk", Labels: map[string]string{"env": "prod"}},
			check: func(t *testing.T, ev event.Event, got rowView) {
				t.Helper()
				require.Equal(t, event.Fingerprint("alerts", "Disk", ev.Labels, []string{"env"}), got.fingerprint)
			},
		},
		{
			name: "a resolved event is received like any other",
			d:    Delivery{Intake: "alerts"},
			ev:   event.Event{Key: "k", Kind: event.KindResolved},
			check: func(t *testing.T, _ event.Event, got rowView) {
				t.Helper()
				require.Equal(t, StatusReceived, got.status)
				require.Empty(t, got.skipReason)
			},
		},
		{
			name: "the Slack reference and the text are redacted",
			d:    Delivery{Intake: "alerts"},
			ev:   event.Event{Key: "k", Kind: event.KindMessage, Text: "token SEEDED-7", Slack: &event.SlackRef{Channel: "C1", TS: "1.2", UserID: "SEEDED-8"}},
			check: func(t *testing.T, _ event.Event, got rowView) {
				t.Helper()
				require.Equal(t, "token "+redactor.Text("SEEDED-7").Text, got.body)
				require.NotContains(t, got.slack, "SEEDED-8")
				require.Contains(t, got.slack, `"Channel":"C1"`)
			},
		},
		{
			name: "the Teams reference is redacted",
			d:    Delivery{Intake: "alerts"},
			ev:   event.Event{Key: "k", Kind: event.KindMessage, Teams: &event.TeamsRef{ChannelID: "19:a@thread.tacv2", MessageID: "2", UserID: "SEEDED-9"}},
			check: func(t *testing.T, _ event.Event, got rowView) {
				t.Helper()
				require.NotContains(t, got.teams, "SEEDED-9")
				require.Contains(t, got.teams, `"ChannelID":"19:a@thread.tacv2"`)
				require.Empty(t, got.slack)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := w.row(tt.d, tt.ev)
			require.NoError(t, err)
			require.Equal(t, tt.d.Intake, p.Intake)

			got := rowView{trusted: p.Trusted, status: p.Status, fingerprint: p.Fingerprint, slack: string(p.Slack), teams: string(p.Teams)}
			if p.Severity != nil {
				got.severity = *p.Severity
			}

			if p.SkipReason != nil {
				got.skipReason = *p.SkipReason
			}

			if p.Body != nil {
				got.body = *p.Body
			}

			tt.check(t, tt.ev, got)
		})
	}
}

// rowView holds the fields of an events row that TestRow checks.
type rowView struct {
	trusted     bool
	status      string
	severity    string
	skipReason  string
	fingerprint string
	body        string
	slack       string
	teams       string
}

// testRedactor redacts SEEDED-<digits>.
func testRedactor(t *testing.T) *redact.Redactor {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("key"), Custom: []redact.Rule{{Name: "seeded", Pattern: "SEEDED-[0-9]+"}}})
	require.NoError(t, err)

	return redactor
}
