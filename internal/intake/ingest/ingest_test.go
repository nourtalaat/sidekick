package ingest

import (
	"encoding/json"
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

			got := w.storedKey(tt.key)
			if tt.wantKept {
				require.Equal(t, tt.key, got)

				return
			}

			require.True(t, strings.HasPrefix(got, hashedKeyPrefix))
			require.Len(t, got, len(hashedKeyPrefix)+64)
			require.Equal(t, got, w.storedKey(tt.key), "the same key gives the same hash")
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
			name: "the Slack reference is kept as it is while the text is redacted",
			d:    Delivery{Intake: "alerts"},
			ev: event.Event{Key: "k", Kind: event.KindMessage, Text: "token SEEDED-7", Slack: &event.SlackRef{
				TeamID: "T1", Channel: "C1", TS: "1700000000.123456", ThreadTS: "1700000000.000100", UserID: "SEEDED-8",
			}},
			check: func(t *testing.T, ev event.Event, got rowView) {
				t.Helper()
				require.Equal(t, "token "+redactor.Text("SEEDED-7").Text, got.body)
				require.NotEqual(t, ev.Slack.UserID, redactor.Text(ev.Slack.UserID).Text, "the user ID matches a rule")

				var stored event.SlackRef
				require.NoError(t, json.Unmarshal([]byte(got.slack), &stored))
				require.Equal(t, *ev.Slack, stored)
				require.Empty(t, got.teams)
			},
		},
		{
			name: "the Teams reference is kept as it is while the text is redacted",
			d:    Delivery{Intake: "alerts"},
			ev: event.Event{Key: "k", Kind: event.KindMessage, Text: "token SEEDED-10", Teams: &event.TeamsRef{
				TenantID:   "SEEDED-11",
				TeamID:     teamIDCardNumber,
				ChannelID:  "19:abc@thread.skype",
				MessageID:  "1700000000123",
				ThreadID:   "1700000000123",
				UserID:     "29:SEEDED-9",
				ServiceURL: "https://smba.trafficmanager.net/amer/",
			}},
			check: func(t *testing.T, ev event.Event, got rowView) {
				t.Helper()
				require.Equal(t, "token "+redactor.Text("SEEDED-10").Text, got.body)

				for _, id := range []string{ev.Teams.TenantID, ev.Teams.TeamID, ev.Teams.ChannelID, ev.Teams.UserID} {
					require.NotEqual(t, id, redactor.Text(id).Text, "%s matches a rule", id)
				}

				var stored event.TeamsRef
				require.NoError(t, json.Unmarshal([]byte(got.teams), &stored))
				require.Equal(t, *ev.Teams, stored)
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

func TestRedactedRef(t *testing.T) {
	t.Parallel()

	redactor := testRedactor(t)
	w := &Writer{redactor: redactor}

	// ref stands for a Slack or Teams reference that gained a field the
	// writer does not list as an ID.
	type ref struct {
		ID       string
		Note     string
		Password string
	}

	tests := []struct {
		name string
		ref  ref
		ids  []string
		want map[string]string
	}{
		{
			name: "a listed field is kept and any other is redacted",
			ref:  ref{ID: "SEEDED-1", Note: "SEEDED-2"},
			ids:  []string{"ID"},
			want: map[string]string{"ID": "SEEDED-1", "Note": redactor.Text("SEEDED-2").Text, "Password": ""},
		},
		{
			name: "the rules that look at a key apply to a field not listed",
			ref:  ref{ID: "U1", Password: "hunter2"},
			ids:  []string{"ID"},
			want: map[string]string{"ID": "U1", "Note": "", "Password": redactedMember(t, redactor, "Password", "hunter2")},
		},
		{
			name: "with no IDs listed, every field is redacted",
			ref:  ref{ID: "SEEDED-3"},
			want: map[string]string{"ID": redactor.Text("SEEDED-3").Text, "Note": "", "Password": ""},
		},
		{
			name: "a NUL character in a kept field is replaced, so jsonb takes it",
			ref:  ref{ID: "a\x00b"},
			ids:  []string{"ID"},
			want: map[string]string{"ID": "a�b", "Note": "", "Password": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := w.redactedRef(tt.ref, tt.ids)
			require.NoError(t, err)

			var fields map[string]string
			require.NoError(t, json.Unmarshal(got, &fields))
			require.Equal(t, tt.want, fields)
		})
	}
}

func TestRefIDsAreFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  any
		ids  []string
	}{
		{name: "Slack", ref: event.SlackRef{}, ids: slackRefIDs},
		{name: "Teams", ref: event.TeamsRef{}, ids: teamsRefIDs},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.ref)
			require.NoError(t, err)

			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields))

			for _, id := range tt.ids {
				require.Contains(t, fields, id, "a renamed or removed field would leave its new name redacted")
			}
		})
	}
}

// teamIDCardNumber is an all-digit ID that passes the Luhn check, so the
// card_numbers rule matches it.
const teamIDCardNumber = "4111111111111111"

// redactedMember returns value as the redactor stores it under key in a
// JSON object.
func redactedMember(t *testing.T, redactor *redact.Redactor, key, value string) string {
	t.Helper()

	raw, err := json.Marshal(map[string]string{key: value})
	require.NoError(t, err)

	out, _ := redactor.JSON(raw)

	var fields map[string]string
	require.NoError(t, json.Unmarshal(out, &fields))
	require.NotEqual(t, value, fields[key], "a key rule matches %s", key)

	return fields[key]
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
