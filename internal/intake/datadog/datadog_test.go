package datadog

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var receivedAt = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	t.Parallel()

	sender := event.Sender{Kind: event.SenderSystem, ID: "11023", Name: "Example Corp"}
	labels := map[string]string{"env": "prod", "monitor": "", "service": "checkout", "team": "payments"}
	scope := map[string]string{"env": "prod", "service": "checkout"}

	tests := []struct {
		name    string
		file    string
		body    string
		want    event.Event
		wantMsg string
	}{
		{
			name: "a recorded Triggered notification",
			file: "fixture/triggered.json",
			want: event.Event{
				Key:         "7802335911728437215",
				Kind:        event.KindAlert,
				Sender:      sender,
				AlertName:   "Checkout error rate is high",
				Title:       "[P2] [Triggered on {env:prod,service:checkout}] Checkout error rate is high",
				Text:        "The 5xx rate of checkout is above 5% over the last 5 minutes. @webhook-sidekick",
				Service:     "checkout",
				Severity:    event.SeverityCritical,
				Labels:      labels,
				Fingerprint: event.Fingerprint(Adapter, "48213977", scope, []string{"env", "service"}),
				StartsAt:    time.UnixMilli(1793004750123).UTC(),
				ReceivedAt:  receivedAt,
				Links: []event.Link{
					{Title: "Event", URL: "https://app.datadoghq.com/event/jump_to?event_id=7802335911728437215"},
					{Title: "Snapshot", URL: "https://p.datadoghq.com/snapshot/view/dd-snapshots-prod/org_11023/2026-10-26/3e1f0c2b9a.png"},
				},
			},
		},
		{
			name: "a recorded Recovered notification",
			file: "fixture/recovered.json",
			want: event.Event{
				Key:         "7802339120517790321",
				Kind:        event.KindResolved,
				Sender:      sender,
				AlertName:   "Checkout error rate is high",
				Title:       "[P2] [Recovered on {env:prod,service:checkout}] Checkout error rate is high",
				Text:        "The 5xx rate of checkout is back under 5%. @webhook-sidekick",
				Service:     "checkout",
				Severity:    event.SeverityCritical,
				Labels:      labels,
				Fingerprint: event.Fingerprint(Adapter, "48213977", scope, []string{"env", "service"}),
				EndsAt:      time.UnixMilli(1793006430000).UTC(),
				ReceivedAt:  receivedAt,
				Links:       []event.Link{{Title: "Event", URL: "https://app.datadoghq.com/event/jump_to?event_id=7802339120517790321"}},
			},
		},
		{
			name: "Datadog's default payload",
			file: "fixture/default-payload.json",
			want: event.Event{
				Key:         "7802335911728437300",
				Kind:        event.KindAlert,
				Sender:      sender,
				AlertName:   "Disk almost full",
				Title:       "[Warn on {host:db-1}] Disk almost full",
				Text:        "%%%\nDisk usage on db-1 is above 90%.\n%%%",
				Severity:    event.SeverityUnknown,
				Labels:      map[string]string{},
				Fingerprint: event.Fingerprint(Adapter, "Disk almost full", map[string]string{}, []string{}),
				StartsAt:    time.UnixMilli(1793004750000).UTC(),
				ReceivedAt:  receivedAt,
			},
		},
		{
			name: "unquoted numbers, the alert title alone, and a priority instead of an alert type",
			body: `{"id": 42, "date": 1793004750000, "org": {"id": 7}, "alert_id": 9, "alert_title": "Queue backlog", "alert_priority": "p3", "alert_scope": "queue:jobs", "tags": "queue:other, ,team:infra"}`,
			want: event.Event{
				Key:         "42",
				Kind:        event.KindAlert,
				Sender:      event.Sender{Kind: event.SenderSystem, ID: "7"},
				AlertName:   "Queue backlog",
				Title:       "Queue backlog",
				Severity:    event.SeverityWarning,
				Labels:      map[string]string{"queue": "jobs", "team": "infra"},
				Fingerprint: event.Fingerprint(Adapter, "9", map[string]string{"queue": "jobs"}, []string{"queue"}),
				StartsAt:    time.UnixMilli(1793004750000).UTC(),
				ReceivedAt:  receivedAt,
			},
		},
		{
			name: "an alert type of success without a transition is resolved",
			body: `{"id": "1", "title": "T", "alert_type": "success", "date": ""}`,
			want: event.Event{
				Key:         "1",
				Kind:        event.KindResolved,
				Sender:      event.Sender{Kind: event.SenderSystem},
				AlertName:   "T",
				Title:       "T",
				Severity:    event.SeverityUnknown,
				Labels:      map[string]string{},
				Fingerprint: event.Fingerprint(Adapter, "T", map[string]string{}, []string{}),
				ReceivedAt:  receivedAt,
			},
		},
		{name: "not JSON", body: `id: 1`, wantMsg: "decode the Datadog payload"},
		{name: "not an object", body: `[]`, wantMsg: "decode the Datadog payload"},
		{name: "null", body: `null`, wantMsg: "id: required"},
		{name: "no id", body: `{"title": "T"}`, wantMsg: "id: required"},
		{name: "an id of spaces", body: `{"id": " ", "title": "T"}`, wantMsg: "id: required"},
		{name: "no title", body: `{"id": "1"}`, wantMsg: "title or alert_title: required"},
		{name: "an id that is an object", body: `{"id": {}, "title": "T"}`, wantMsg: "id, date, alert_id and org.id must each be a string or a number"},
		{name: "a title that is a number", body: `{"id": "1", "title": 1}`, wantMsg: "decode the Datadog payload"},
		{name: "a date that is not a number", body: `{"id": "1", "title": "T", "date": "yesterday"}`, wantMsg: "date: must be a number of milliseconds since 1970"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(tt.body)

			if tt.file != "" {
				var err error

				body, err = os.ReadFile(tt.file)
				require.NoError(t, err)
			}

			a, err := New(t.Context(), "datadog", Config{}, registry.Deps{})
			require.NoError(t, err)

			events, err := a.Parse(t.Context(), intake.Request{Endpoint: "datadog", Body: body, ReceivedAt: receivedAt})
			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)

				return
			}

			require.NoError(t, err)
			require.Len(t, events, 1)
			require.JSONEq(t, string(body), string(events[0].Raw))

			events[0].Raw = nil
			require.Equal(t, tt.want, events[0])
		})
	}
}

func TestSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		alertType string
		priority  string
		want      event.Severity
	}{
		{alertType: "error", want: event.SeverityCritical},
		{alertType: "warning", priority: "P1", want: event.SeverityWarning},
		{alertType: "info", want: event.SeverityInfo},
		{alertType: "success", priority: "P4", want: event.SeverityInfo},
		{priority: "P1", want: event.SeverityCritical},
		{priority: "P2", want: event.SeverityCritical},
		{priority: " p3 ", want: event.SeverityWarning},
		{priority: "P5", want: event.SeverityInfo},
		{priority: "normal", want: event.SeverityUnknown},
		{want: event.SeverityUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.alertType+"/"+tt.priority, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, severity(payload{AlertType: tt.alertType, AlertPriority: tt.priority}))
		})
	}
}

func TestKeysAndFingerprints(t *testing.T) {
	t.Parallel()

	triggered, err := os.ReadFile("fixture/triggered.json")
	require.NoError(t, err)

	recovered, err := os.ReadFile("fixture/recovered.json")
	require.NoError(t, err)

	base := string(triggered)

	tests := []struct {
		name            string
		other           string
		wantSameKey     bool
		wantSameGroupBy bool
	}{
		{name: "a repeated delivery has the same key", other: base, wantSameKey: true, wantSameGroupBy: true},
		{name: "other members do not change the key", other: strings.Replace(base, `"P2"`, `"P1"`, 1), wantSameKey: true, wantSameGroupBy: true},
		{name: "the recovery has a new key and the same fingerprint", other: string(recovered), wantSameGroupBy: true},
		{name: "a renotification has a new key and the same fingerprint", other: strings.Replace(base, `"id": "7802335911728437215"`, `"id": "7802335911728437999"`, 1), wantSameGroupBy: true},
		{name: "another scope changes the fingerprint", other: strings.Replace(base, `"alert_scope": "env:prod,service:checkout"`, `"alert_scope": "env:prod,service:cart"`, 1), wantSameKey: true},
		{name: "another monitor changes the fingerprint", other: strings.Replace(base, `"48213977"`, `"48213978"`, 1), wantSameKey: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			first, err := Datadog{}.Parse(t.Context(), intake.Request{Body: triggered})
			require.NoError(t, err)

			second, err := Datadog{}.Parse(t.Context(), intake.Request{Body: []byte(tt.other)})
			require.NoError(t, err)

			require.Equal(t, tt.wantSameKey, first[0].Key == second[0].Key, "same key")
			require.Equal(t, tt.wantSameGroupBy, first[0].Fingerprint == second[0].Fingerprint, "same fingerprint")
		})
	}
}
