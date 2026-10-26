package signoz

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/alertmanager"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var receivedAt = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	t.Parallel()

	recorded, err := os.ReadFile("fixture/two-alerts.json")
	require.NoError(t, err)

	const (
		rule   = "https://signoz.example.com/alerts/edit?ruleId=01929c4e-5b7d-7a01-9f3e-2b6c8d0e4a11"
		logs   = "https://signoz.example.com/logs/logs-explorer?compositeQuery=%7B%22service.name%22%3A%22checkout%22%7D"
		traces = "https://signoz.example.com/traces-explorer?compositeQuery=%7B%22service.name%22%3A%22checkout%22%7D"
	)

	tests := []struct {
		name string
		body string
		// want holds the fields this adapter sets; the others are checked
		// to be those of the alertmanager adapter.
		want    []want
		wantMsg string
	}{
		{
			name: "a recorded delivery with a firing and a resolved alert",
			body: string(recorded),
			want: []want{
				{
					kind:     event.KindAlert,
					service:  "checkout",
					severity: event.SeverityCritical,
					title:    "Checkout p99 latency is above 800ms",
					links: []event.Link{
						{Title: "Source", URL: rule},
						{Title: "Related logs", URL: logs},
						{Title: "Related traces", URL: traces},
					},
					fingerprintLabels: map[string]string{
						"deployment.environment": "prod",
						"ruleId":                 "01929c4e-5b7d-7a01-9f3e-2b6c8d0e4a11",
						"ruleSource":             rule,
						"service.name":           "checkout",
					},
				},
				{
					kind:     event.KindResolved,
					service:  "payments",
					severity: event.SeverityWarning,
					title:    "Checkout p99 latency is above 500ms",
					links:    []event.Link{{Title: "Rule", URL: rule}},
					fingerprintLabels: map[string]string{
						"deployment.environment": "prod",
						"ruleId":                 "01929c4e-5b7d-7a01-9f3e-2b6c8d0e4a11",
						"ruleSource":             rule,
						"service.name":           "payments",
					},
				},
			},
		},
		{
			name: "service_name, then service, when service.name is missing",
			body: `{"version": "4", "groupKey": "g", "alerts": [
				{"status": "firing", "fingerprint": "a", "labels": {"alertname": "A", "service_name": "cart", "service": "other"}},
				{"status": "firing", "fingerprint": "b", "labels": {"alertname": "B", "service": "search"}}]}`,
			want: []want{
				{kind: event.KindAlert, service: "cart", severity: event.SeverityUnknown, title: "A", fingerprintLabels: map[string]string{"service_name": "cart", "service": "other"}},
				{kind: event.KindAlert, service: "search", severity: event.SeverityUnknown, title: "B", fingerprintLabels: map[string]string{"service": "search"}},
			},
		},
		{
			name: "an alert without labels or annotations",
			body: `{"version": "4", "groupKey": "g", "alerts": [{"status": "firing", "fingerprint": "f"}]}`,
			want: []want{{kind: event.KindAlert, severity: event.SeverityUnknown, fingerprintLabels: map[string]string{}}},
		},
		{name: "not JSON", body: `version: 4`, wantMsg: "read the SigNoz payload: decode the Alertmanager payload"},
		{name: "another version", body: `{"version": "3", "alerts": []}`, wantMsg: `read the SigNoz payload: version: only version "4" is supported`},
		{name: "alert without fingerprint", body: `{"version": "4", "alerts": [{"status": "firing"}]}`, wantMsg: "read the SigNoz payload: alerts[0]: fingerprint: required"},
		{name: "annotation of the wrong type", body: `{"version": "4", "alerts": [{"status": "firing", "fingerprint": "a", "annotations": {"related_logs": 1}}]}`, wantMsg: "alerts[0]: decode alert"},
		{name: "bad start time", body: `{"version": "4", "alerts": [{"status": "firing", "fingerprint": "a", "startsAt": "yesterday"}]}`, wantMsg: "read the SigNoz payload: alerts[0]: startsAt: must be an RFC 3339 time"},
		{name: "bad end time", body: `{"version": "4", "alerts": [{"status": "resolved", "fingerprint": "a", "endsAt": "yesterday"}]}`, wantMsg: "read the SigNoz payload: alerts[0]: endsAt: must be an RFC 3339 time"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := New(t.Context(), "signoz", Config{}, registry.Deps{})
			require.NoError(t, err)

			req := intake.Request{Endpoint: "signoz", Body: []byte(tt.body), ReceivedAt: receivedAt}

			events, err := a.Parse(t.Context(), req)
			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
				require.NotContains(t, err.Error(), "yesterday", "errors never hold payload values")

				return
			}

			require.NoError(t, err)

			base, err := alertmanager.Alertmanager{}.Parse(t.Context(), req)
			require.NoError(t, err)
			require.Len(t, events, len(tt.want))

			for i, w := range tt.want {
				expected := base[i]
				expected.Service = w.service
				expected.Links = w.links
				expected.Fingerprint = event.Fingerprint(Adapter, expected.AlertName, w.fingerprintLabels, keysOf(w.fingerprintLabels))

				require.Equal(t, expected, events[i])
				require.Equal(t, w.kind, events[i].Kind)
				require.Equal(t, w.severity, events[i].Severity)
				require.Equal(t, w.title, events[i].Title)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	const firing = `{"version": "4", "groupKey": "g", "alerts": [{"status": "firing", "fingerprint": "f",
		"labels": {"alertname": "Latency", "service.name": "checkout", "severity": "warning", "threshold.name": "warning"}}]}`

	tests := []struct {
		name     string
		other    string
		wantSame bool
	}{
		{name: "the same alert has the same fingerprint", other: firing, wantSame: true},
		{name: "a severity change keeps the fingerprint", other: strings.Replace(firing, `"severity": "warning"`, `"severity": "critical"`, 1), wantSame: true},
		{name: "a threshold change keeps the fingerprint", other: strings.Replace(firing, `"threshold.name": "warning"`, `"threshold.name": "critical"`, 1), wantSame: true},
		{name: "resolving keeps the fingerprint", other: strings.Replace(firing, `"status": "firing"`, `"status": "resolved"`, 1), wantSame: true},
		{name: "another service changes the fingerprint", other: strings.Replace(firing, `"checkout"`, `"payments"`, 1)},
		{name: "another alert name changes the fingerprint", other: strings.Replace(firing, `"Latency"`, `"Errors"`, 1)},
		{name: "an extra label changes the fingerprint", other: strings.Replace(firing, `"alertname"`, `"pod": "checkout-1", "alertname"`, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base, err := SigNoz{}.Parse(t.Context(), intake.Request{Body: []byte(firing)})
			require.NoError(t, err)

			other, err := SigNoz{}.Parse(t.Context(), intake.Request{Body: []byte(tt.other)})
			require.NoError(t, err)

			if tt.wantSame {
				require.Equal(t, base[0].Fingerprint, other[0].Fingerprint)
			} else {
				require.NotEqual(t, base[0].Fingerprint, other[0].Fingerprint)
			}
		})
	}
}

// want is what the adapter sets on one event, beside what the
// alertmanager adapter sets.
type want struct {
	kind              event.Kind
	service           string
	severity          event.Severity
	title             string
	links             []event.Link
	fingerprintLabels map[string]string
}

func keysOf(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}

	return keys
}
