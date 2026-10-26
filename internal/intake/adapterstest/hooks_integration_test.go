//go:build integration

package adapterstest

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The recorded payloads of the adapters, kept in each adapter's fixture
// directory, relative to internal/intake.
const (
	signozAlerts      = "signoz/fixture/two-alerts.json"
	datadogTriggered  = "datadog/fixture/triggered.json"
	datadogRecovered  = "datadog/fixture/recovered.json"
	sentryIssueAlert  = "sentry/fixture/issue-alert.json"
	sentryMetric      = "sentry/fixture/metric-alert-critical.json"
	sentryResolved    = "sentry/fixture/issue-resolved.json"
	sentryAssigned    = "sentry/fixture/issue-assigned.json"
	grafanaFiring     = "grafana/fixture/two-firing.json"
	grafanaResolved   = "grafana/fixture/resolved.json"
	sentryNoEventBody = `{"action": "triggered", "data": {"event": {}}}`
)

func TestHooks(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name string
		// before are sent first; their answers must be 202.
		before       []delivery
		send         delivery
		wantStatus   int
		wantResponse string
		// wantKinds are the kinds of the stored events, oldest first.
		wantKinds []string
		// wantRouteJobs counts the route_event jobs: one per stored alert.
		wantRouteJobs int
		check         func(t *testing.T, events []db.Event)
	}{
		{
			name:          "SigNoz: a firing and a resolved alert",
			send:          bearer("signoz", signozToken, readPayload(t, signozAlerts)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "accepted-alert-and-resolved.json",
			wantKinds:     []string{"alert", "resolved"},
			wantRouteJobs: 1,
			check: func(t *testing.T, events []db.Event) {
				t.Helper()

				require.Equal(t, "checkout", *events[0].Service)
				require.Equal(t, "critical", *events[0].Severity)
				require.Contains(t, string(events[0].Links), "Related logs")
				require.NotEqual(t, events[0].Fingerprint, events[1].Fingerprint, "the two alerts are of other services")
			},
		},
		{
			name:          "SigNoz: a repeated delivery is a duplicate",
			before:        []delivery{bearer("signoz", signozToken, readPayload(t, signozAlerts))},
			send:          bearer("signoz", signozToken, readPayload(t, signozAlerts)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "duplicate-two.json",
			wantKinds:     []string{"alert", "resolved"},
			wantRouteJobs: 1,
		},
		{
			name:         "SigNoz: a wrong token",
			send:         bearer("signoz", "wrong", readPayload(t, signozAlerts)),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
		},
		{
			name:          "Datadog: a triggered monitor, then its recovery",
			before:        []delivery{bearer("datadog", datadogToken, readPayload(t, datadogTriggered))},
			send:          bearer("datadog", datadogToken, readPayload(t, datadogRecovered)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "accepted-resolved.json",
			wantKinds:     []string{"alert", "resolved"},
			wantRouteJobs: 1,
			check: func(t *testing.T, events []db.Event) {
				t.Helper()

				require.Equal(t, "7802335911728437215", events[0].Key)
				require.Equal(t, "Checkout error rate is high", *events[0].AlertName)
				require.Equal(t, "checkout", *events[0].Service)
				require.Equal(t, events[0].Fingerprint, events[1].Fingerprint, "the recovery is of the same monitor and scope")
			},
		},
		{
			name:          "Datadog: a repeated delivery is a duplicate",
			before:        []delivery{bearer("datadog", datadogToken, readPayload(t, datadogTriggered))},
			send:          bearer("datadog", datadogToken, readPayload(t, datadogTriggered)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "duplicate-one.json",
			wantKinds:     []string{"alert"},
			wantRouteJobs: 1,
		},
		{
			name:         "Datadog: no token",
			send:         delivery{endpoint: "datadog", body: readPayload(t, datadogTriggered)},
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
		},
		{
			name:          "Sentry: a signed issue alert",
			send:          sentrySigned(sentrySecret, readPayload(t, sentryIssueAlert)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "accepted-one.json",
			wantKinds:     []string{"alert"},
			wantRouteJobs: 1,
			check: func(t *testing.T, events []db.Event) {
				t.Helper()

				require.Equal(t, "event_alert:e4874d664c3540c1a32eab185f12c5ab:Very Important Alert!", events[0].Key)
				require.Equal(t, "ReferenceError: heck is not defined", *events[0].Title)
				require.False(t, events[0].Trusted)
			},
		},
		{
			name:          "Sentry: a metric alert and a resolved issue",
			before:        []delivery{sentrySigned(sentrySecret, readPayload(t, sentryMetric))},
			send:          sentrySigned(sentrySecret, readPayload(t, sentryResolved)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "accepted-resolved.json",
			wantKinds:     []string{"alert", "resolved"},
			wantRouteJobs: 1,
		},
		{
			name:         "Sentry: an assigned issue stores nothing",
			send:         sentrySigned(sentrySecret, readPayload(t, sentryAssigned)),
			wantStatus:   http.StatusAccepted,
			wantResponse: "no-events.json",
		},
		{
			name:         "Sentry: a signature with another secret",
			send:         sentrySigned("not-the-secret", readPayload(t, sentryIssueAlert)),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
		},
		{
			name:         "Sentry: no signature, even with the secret as a bearer token",
			send:         bearer("sentry", sentrySecret, readPayload(t, sentryIssueAlert)),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
		},
		{
			name:         "Sentry: a signed payload the adapter rejects",
			send:         sentrySigned(sentrySecret, []byte(sentryNoEventBody)),
			wantStatus:   http.StatusBadRequest,
			wantResponse: "sentry-no-event-id.json",
		},
		{
			name:          "Grafana: two firing alerts with a bearer token",
			send:          bearer("grafana", grafanaToken, readPayload(t, grafanaFiring)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "accepted-two.json",
			wantKinds:     []string{"alert", "alert"},
			wantRouteJobs: 2,
			check: func(t *testing.T, events []db.Event) {
				t.Helper()

				require.Equal(t, "High memory usage", *events[0].AlertName)
				require.Contains(t, string(events[0].Links), "https://myrunbook.com/runbook/1234")
			},
		},
		{
			name:          "Grafana: a signed resolved alert",
			send:          grafanaSigned(grafanaHMACSecret, now, readPayload(t, grafanaResolved)),
			wantStatus:    http.StatusAccepted,
			wantResponse:  "accepted-resolved.json",
			wantKinds:     []string{"resolved"},
			wantRouteJobs: 0,
			check: func(t *testing.T, events []db.Event) {
				t.Helper()

				require.Equal(t, "grafana-signed", events[0].Intake)
				require.Equal(t, time.Date(2026, 10, 26, 9, 10, 0, 0, time.UTC), events[0].EndsAt.UTC())
			},
		},
		{
			name:         "Grafana: a signature too old",
			send:         grafanaSigned(grafanaHMACSecret, now.Add(-time.Hour), readPayload(t, grafanaResolved)),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
		},
		{
			name:         "Grafana: a bearer token where the endpoint wants a signature",
			send:         bearer("grafana-signed", grafanaHMACSecret, readPayload(t, grafanaResolved)),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			for _, d := range tt.before {
				status, body := h.send(t, d)
				require.Equal(t, http.StatusAccepted, status, "%s", body)
			}

			status, body := h.send(t, tt.send)
			require.Equal(t, tt.wantStatus, status, "%s", body)
			requireResponse(t, tt.wantResponse, body)

			events := h.events(t)

			var kinds []string
			for _, ev := range events {
				kinds = append(kinds, ev.Kind)
			}

			require.Equal(t, tt.wantKinds, kinds)
			require.Equal(t, tt.wantRouteJobs, h.routeJobs(t))

			for _, ev := range events {
				h.requireParsesAgain(t, ev)
			}

			if tt.check != nil {
				tt.check(t, events)
			}
		})
	}
}

// requireParsesAgain checks that the stored raw payload of ev, parsed by
// the adapter of its endpoint, gives back an event with ev's key.
func (h *harness) requireParsesAgain(t *testing.T, ev db.Event) {
	t.Helper()

	again, err := h.adapters[ev.Intake].Parse(t.Context(), intake.Request{Endpoint: ev.Intake, Body: ev.Raw})
	require.NoError(t, err)
	require.Len(t, again, 1, "a stored raw payload holds one event")
	require.Equal(t, ev.Key, again[0].Key)
	require.Equal(t, ev.Kind, string(again[0].Kind))
}
