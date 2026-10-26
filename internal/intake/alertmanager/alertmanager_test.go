package alertmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const groupKey = `{}:{alertname="HighErrorRate"}`

var receivedAt = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	t.Parallel()

	recorded, err := os.ReadFile("fixture/three-alerts.json")
	require.NoError(t, err)

	sender := event.Sender{Kind: event.SenderSystem, ID: "http://alertmanager.example.com:9093", Name: "sidekick"}
	generator := "http://prometheus.example.com/graph?g0.expr=job%3Acheckout_errors%3Arate5m+%3E+0.05"
	labels := func(instance, severity string) map[string]string {
		return map[string]string{
			"alertname": "HighErrorRate", "env": "prod", "instance": instance,
			"job": "checkout", "service": "checkout", "severity": severity,
		}
	}

	first := labels("checkout-0:8080", "critical")
	second := labels("checkout-1:8080", "warning")
	third := labels("checkout-2:8080", "critical")

	tests := []struct {
		name    string
		body    string
		want    []event.Event
		wantMsg string
	}{
		{
			name: "a recorded delivery with two firing alerts and one resolved alert",
			body: string(recorded),
			want: []event.Event{
				{
					Key:         wantKey(groupKey, "3f2a9c1e5b7d8a01", "firing", "2026-10-26T09:12:30.123Z"),
					Kind:        event.KindAlert,
					Sender:      sender,
					AlertName:   "HighErrorRate",
					Title:       "Checkout error rate above 5%",
					Text:        "5xx rate is 7.2% over the last 5 minutes.",
					Service:     "checkout",
					Severity:    event.SeverityCritical,
					Labels:      first,
					Fingerprint: event.Fingerprint(Adapter, "HighErrorRate", first, slices.Collect(maps.Keys(first))),
					StartsAt:    time.Date(2026, 10, 26, 9, 12, 30, 123_000_000, time.UTC),
					ReceivedAt:  receivedAt,
					Links: []event.Link{
						{Title: "Source", URL: generator},
						{Title: "Runbook", URL: "https://runbooks.example.com/high-error-rate"},
					},
				},
				{
					Key:         wantKey(groupKey, "8b6e0d4c2a1f9e37", "firing", "2026-10-26T09:13:00Z"),
					Kind:        event.KindAlert,
					Sender:      sender,
					AlertName:   "HighErrorRate",
					Title:       "HighErrorRate",
					Text:        "5xx rate is 5.4% over the last 5 minutes.",
					Service:     "checkout",
					Severity:    event.SeverityWarning,
					Labels:      second,
					Fingerprint: event.Fingerprint(Adapter, "HighErrorRate", second, slices.Collect(maps.Keys(second))),
					StartsAt:    time.Date(2026, 10, 26, 9, 13, 0, 0, time.UTC),
					ReceivedAt:  receivedAt,
				},
				{
					Key:         wantKey(groupKey, "c47d1a2b9e0f3365", "resolved", "2026-10-26T08:50:00Z"),
					Kind:        event.KindResolved,
					Sender:      sender,
					AlertName:   "HighErrorRate",
					Title:       "Checkout error rate above 5%",
					Service:     "checkout",
					Severity:    event.SeverityCritical,
					Labels:      third,
					Fingerprint: event.Fingerprint(Adapter, "HighErrorRate", third, slices.Collect(maps.Keys(third))),
					StartsAt:    time.Date(2026, 10, 26, 8, 50, 0, 0, time.UTC),
					EndsAt:      time.Date(2026, 10, 26, 9, 10, 0, 0, time.UTC),
					ReceivedAt:  receivedAt,
					Links:       []event.Link{{Title: "Source", URL: generator}},
				},
			},
		},
		{
			name: "an alert without labels or annotations",
			body: `{"version": "4", "groupKey": "g", "alerts": [{"status": "firing", "fingerprint": "f", "startsAt": "2026-10-26T09:00:00+02:00"}]}`,
			want: []event.Event{{
				Key:         wantKey("g", "f", "firing", "2026-10-26T07:00:00Z"),
				Kind:        event.KindAlert,
				Sender:      event.Sender{Kind: event.SenderSystem},
				Severity:    event.SeverityUnknown,
				Labels:      map[string]string{},
				Fingerprint: event.Fingerprint(Adapter, "", nil, nil),
				StartsAt:    time.Date(2026, 10, 26, 9, 0, 0, 0, time.FixedZone("", 2*60*60)),
				ReceivedAt:  receivedAt,
			}},
		},
		{
			name: "no alerts gives no events",
			body: `{"version": "4", "groupKey": "g", "alerts": []}`,
			want: []event.Event{},
		},
		{name: "not JSON", body: `version: 4`, wantMsg: "decode the Alertmanager payload"},
		{name: "not an object", body: `[]`, wantMsg: "decode the Alertmanager payload"},
		{name: "another version", body: `{"version": "3", "alerts": []}`, wantMsg: `version: only version "4" is supported`},
		{name: "no version", body: `{"alerts": []}`, wantMsg: `version: only version "4"`},
		{name: "no alerts member", body: `{"version": "4"}`, wantMsg: "alerts: required"},
		{name: "alert without fingerprint", body: `{"version": "4", "alerts": [{"status": "firing"}]}`, wantMsg: "alerts[0]: fingerprint: required"},
		{name: "unknown status", body: `{"version": "4", "alerts": [{"status": "firing", "fingerprint": "a"}, {"status": "pending", "fingerprint": "b"}]}`, wantMsg: `alerts[1]: status: must be "firing" or "resolved"`},
		{name: "label of the wrong type", body: `{"version": "4", "alerts": [{"status": "firing", "fingerprint": "a", "labels": {"x": 1}}]}`, wantMsg: "alerts[0]: decode alert"},
		{name: "bad start time", body: `{"version": "4", "alerts": [{"status": "firing", "fingerprint": "a", "startsAt": "yesterday"}]}`, wantMsg: "alerts[0]: decode alert"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := New(t.Context(), "alertmanager", Config{}, registry.Deps{})
			require.NoError(t, err)

			events, err := a.Parse(t.Context(), intake.Request{Endpoint: "alertmanager", Body: []byte(tt.body), ReceivedAt: receivedAt})
			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)

				return
			}

			require.NoError(t, err)
			require.Len(t, events, len(tt.want))

			for i := range events {
				requireRawHoldsOnlyAlert(t, tt.body, i, events[i].Raw)

				events[i].Raw = nil
			}

			require.Equal(t, tt.want, events)
		})
	}
}

func TestKeys(t *testing.T) {
	t.Parallel()

	const firing = `{"version": "4", "groupKey": "g", "alerts": [{"status": "firing", "fingerprint": "f", "startsAt": "2026-10-26T09:00:00Z"}]}`

	tests := []struct {
		name     string
		other    string
		wantSame bool
	}{
		{name: "a repeated notification has the same key", other: firing, wantSame: true},
		{name: "other members do not change the key", other: strings.Replace(firing, `"groupKey"`, `"receiver": "other", "groupKey"`, 1), wantSame: true},
		{name: "resolving changes the key", other: strings.Replace(firing, `"firing"`, `"resolved"`, 1)},
		{name: "firing again later changes the key", other: strings.Replace(firing, "09:00:00", "10:00:00", 1)},
		{name: "another fingerprint changes the key", other: strings.Replace(firing, `"f"`, `"f2"`, 1)},
		{name: "another group changes the key", other: strings.Replace(firing, `"g"`, `"g2"`, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := Alertmanager{}

			base, err := a.Parse(t.Context(), intake.Request{Body: []byte(firing)})
			require.NoError(t, err)

			other, err := a.Parse(t.Context(), intake.Request{Body: []byte(tt.other)})
			require.NoError(t, err)

			if tt.wantSame {
				require.Equal(t, base[0].Key, other[0].Key)
			} else {
				require.NotEqual(t, base[0].Key, other[0].Key)
			}
		})
	}
}

// requireRawHoldsOnlyAlert checks that raw is the delivery with only alert
// i in alerts.
func requireRawHoldsOnlyAlert(t *testing.T, body string, i int, raw json.RawMessage) {
	t.Helper()

	var want, got map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &want))
	require.NoError(t, json.Unmarshal(raw, &got))

	want["alerts"] = []any{want["alerts"].([]any)[i]}
	require.Equal(t, want, got)
}

func wantKey(groupKey, fingerprint, status, startsAt string) string {
	sum := sha256.Sum256([]byte(groupKey + "\x00" + fingerprint + "\x00" + status + "\x00" + startsAt + "\x00"))

	return hex.EncodeToString(sum[:])
}
