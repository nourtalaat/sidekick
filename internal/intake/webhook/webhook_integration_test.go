//go:build integration

package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestHooks(t *testing.T) {
	t.Parallel()

	generic := readFixture(t, "requests/generic-alert.json")
	alertmanager := readFixture(t, "requests/alertmanager-three-alerts.json")
	now := time.Now()

	tests := []struct {
		name  string
		seeds []string
		// before are sent first; their answers are not checked.
		before       []delivery
		send         delivery
		wantStatus   int
		wantResponse string
		check        func(t *testing.T, h *harness)
	}{
		{
			name:         "valid HMAC signature",
			send:         signed("ci", testHMACKey, now, generic),
			wantStatus:   http.StatusAccepted,
			wantResponse: "accepted-one.json",
			check: func(t *testing.T, h *harness) {
				t.Helper()

				events := h.events(t)
				require.Len(t, events, 1)

				ev := events[0]
				require.Equal(t, "ci", ev.Intake)
				require.Equal(t, "build-4711", ev.Key)
				require.Equal(t, "alert", ev.Kind)
				require.False(t, ev.Trusted)
				require.Equal(t, "Nightly build failed", *ev.Title)
				require.Equal(t, "The integration stage failed on main.", *ev.Body)
				require.Equal(t, "checkout", *ev.Service)
				require.Equal(t, "warning", *ev.Severity)
				require.JSONEq(t, `{"env": "prod", "service": "checkout"}`, string(ev.Labels))
				require.JSONEq(t, string(generic), string(ev.Raw))
				require.Equal(t, "received", ev.Status)
				require.Nil(t, ev.SkipReason)
				require.Len(t, ev.Fingerprint, 32)

				require.Equal(t, []jobRow{routeJob(ev)}, h.jobs(t))
			},
		},
		{
			name:         "wrong HMAC signature",
			send:         signed("ci", "not-the-key", now, generic),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
			check:        requireNothingStored,
		},
		{
			name:         "HMAC timestamp outside the tolerance",
			send:         signed("ci", testHMACKey, now.Add(-6*time.Minute), generic),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
			check:        requireNothingStored,
		},
		{
			name:         "HMAC signature of another body",
			send:         delivery{endpoint: "ci", header: signed("ci", testHMACKey, now, []byte(`{}`)).header, body: generic},
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
			check:        requireNothingStored,
		},
		{
			name:         "valid bearer token with several alerts and one resolved",
			send:         bearer("alertmanager", testBearerToken, alertmanager),
			wantStatus:   http.StatusAccepted,
			wantResponse: "accepted-alertmanager.json",
			check: func(t *testing.T, h *harness) {
				t.Helper()

				events := h.events(t)
				require.Len(t, events, 3)

				for _, ev := range events {
					require.Equal(t, "alertmanager", ev.Intake)
					require.True(t, ev.Trusted, "trust comes from the endpoint's config")
					require.Equal(t, "HighErrorRate", *ev.AlertName)
					require.NotNil(t, ev.StartsAt)
				}

				require.Equal(t, []string{"alert", "alert", "resolved"}, []string{events[0].Kind, events[1].Kind, events[2].Kind})
				require.Equal(t, []string{"received", "received", "skipped"}, []string{events[0].Status, events[1].Status, events[2].Status})
				require.Equal(t, "resolved events are linked in M1", *events[2].SkipReason)

				var raw map[string]any
				require.NoError(t, json.Unmarshal(events[2].Raw, &raw))
				require.Len(t, raw["alerts"], 1)

				require.Equal(t, []jobRow{routeJob(events[0]), routeJob(events[1])}, h.jobs(t), "the resolved alert gets no route_event job")
			},
		},
		{
			name:         "wrong bearer token",
			send:         bearer("alertmanager", testBearerToken+"x", alertmanager),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
			check:        requireNothingStored,
		},
		{
			name:         "no credentials",
			send:         delivery{endpoint: "alertmanager", body: alertmanager},
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
			check:        requireNothingStored,
		},
		{
			name:         "an empty secret accepts nothing",
			send:         bearer("empty-secret", "", generic),
			wantStatus:   http.StatusUnauthorized,
			wantResponse: "unauthenticated.json",
			check:        requireNothingStored,
		},
		{
			name:         "a secret that cannot be read",
			send:         bearer("missing-secret", "anything", generic),
			wantStatus:   http.StatusInternalServerError,
			wantResponse: "internal-error.json",
			check:        requireNothingStored,
		},
		{
			name:         "unknown endpoint",
			send:         bearer("nope", testBearerToken, alertmanager),
			wantStatus:   http.StatusNotFound,
			wantResponse: "not-found.json",
			check:        requireNothingStored,
		},
		{
			name:         "body over the limit",
			send:         bearer("alertmanager", testBearerToken, oversized()),
			wantStatus:   http.StatusRequestEntityTooLarge,
			wantResponse: "too-large.json",
			check:        requireNothingStored,
		},
		{
			name:         "payload the adapter rejects",
			send:         signed("ci", testHMACKey, now, readFixture(t, "requests/generic-missing-title.json")),
			wantStatus:   http.StatusBadRequest,
			wantResponse: "invalid-payload.json",
			check:        requireNothingStored,
		},
		{
			name:         "a repeated delivery is a duplicate",
			before:       []delivery{bearer("alertmanager", testBearerToken, alertmanager)},
			send:         bearer("alertmanager", testBearerToken, alertmanager),
			wantStatus:   http.StatusAccepted,
			wantResponse: "duplicate-alertmanager.json",
			check: func(t *testing.T, h *harness) {
				t.Helper()

				require.Len(t, h.events(t), 3)
				require.Len(t, h.jobs(t), 2)
			},
		},
		{
			name:         "an event stored before is a duplicate",
			seeds:        []string{"fixture/existing-ci-event.sql"},
			send:         signed("ci", testHMACKey, now, generic),
			wantStatus:   http.StatusAccepted,
			wantResponse: "duplicate-one.json",
			check: func(t *testing.T, h *harness) {
				t.Helper()

				events := h.events(t)
				require.Len(t, events, 1)
				require.Equal(t, "2026-10-26T09:00:00Z", events[0].ReceivedAt.UTC().Format(time.RFC3339), "the stored event is left as it is")
				require.Empty(t, h.jobs(t))
			},
		},
		{
			name:         "seeded secrets never reach the events table",
			send:         signed("ci", testHMACKey, now, readFixture(t, "requests/generic-with-secrets.json")),
			wantStatus:   http.StatusAccepted,
			wantResponse: "accepted-one.json",
			check: func(t *testing.T, h *harness) {
				t.Helper()

				stored := h.eventsAsText(t)
				for _, secret := range []string{"SEEDED-", "SeededPassword42", "SeededClientSecret77", "ops@example.com"} {
					require.NotContains(t, stored, secret)
				}

				require.Contains(t, stored, "[REDACTED:seeded:")

				events := h.events(t)
				require.Len(t, events, 1)
				require.True(t, strings.HasPrefix(events[0].Key, "sha256:"), "a key the redactor changes is stored as its hash")

				var raw struct {
					Debug struct {
						Note string `json:"note"`
					} `json:"debug"`
				}
				require.NoError(t, json.Unmarshal(events[0].Raw, &raw))
				require.Equal(t, "nul�byte", raw.Debug.Note)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.seeds...)

			for _, d := range tt.before {
				status, _ := h.send(t, d)
				require.Equal(t, http.StatusAccepted, status)
			}

			status, body := h.send(t, tt.send)
			require.Equal(t, tt.wantStatus, status, "body: %s", body)
			requireResponse(t, tt.wantResponse, body)
			tt.check(t, h)
		})
	}
}

// requireNothingStored checks that no event and no job was written.
func requireNothingStored(t *testing.T, h *harness) {
	t.Helper()

	require.Empty(t, h.events(t))
	require.Empty(t, h.jobs(t))
}

// routeJob is the route_event job that ev should have.
func routeJob(ev db.Event) jobRow {
	key := "route_event:" + ev.ID.String()

	return jobRow{Kind: queue.KindRouteEvent, EventID: ev.ID.String(), UniqueKey: &key}
}

// oversized is a JSON body one byte over MaxBodyBytes.
func oversized() []byte {
	prefix := []byte(`{"pad": "`)
	suffix := []byte(`"}`)

	return append(append(prefix, bytes.Repeat([]byte("x"), MaxBodyBytes+1-len(prefix)-len(suffix))...), suffix...)
}
