package sentry

import (
	"encoding/hex"
	"net/http"
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

	issueAlertLabels := map[string]string{
		"browser": "Chrome 75.0.3770", "browser.name": "Chrome", "handled": "no", "level": "error",
		"mechanism": "onerror", "os": "Mac OS X 10.14.0", "os.name": "Mac OS X", "user": "ip:162.217.75.90",
		"url": "https://null.jsbin.com/runner", "issue_id": "1117540176", "project_id": "1",
	}
	metricLabels := map[string]string{"alert_rule_id": "7", "project": "bar"}
	issueLabels := map[string]string{"issue_id": "1234567890", "project": "python", "project_id": "112313123123134"}
	issueSender := event.Sender{Kind: event.SenderSystem, ID: "24b397fc-a86e-43ef-9297-949e21b82480"}
	alertSender := event.Sender{Kind: event.SenderSystem, ID: "a8e5d37a-696c-4c54-adb5-b3f28d64c7de"}

	issueCreated := event.Event{
		Key:         "issue:1234567890:created:2025-11-10T20:56:00.738Z",
		Kind:        event.KindAlert,
		Sender:      issueSender,
		AlertName:   "PYTHON-Y",
		Title:       "Error generated with event_id: 495d375a-1df6-45c0-9890-34dae8e1b6a4(Priority: HIGH)",
		Text:        "test-transaction-0-41e49cd3-7252-441f-8d27-63a9ad697b0a",
		Service:     "python",
		Severity:    event.SeverityCritical,
		Labels:      issueLabels,
		Fingerprint: event.Fingerprint(Adapter, "issue", issueLabels, []string{"issue_id"}),
		StartsAt:    time.Date(2025, 11, 10, 20, 56, 0, 679_000_000, time.UTC),
		ReceivedAt:  receivedAt,
		Links:       []event.Link{{Title: "Issue", URL: "https://example-org.sentry.io/issues/1234567890/"}},
	}

	issueResolved := issueCreated
	issueResolved.Key = "issue:1234567890:resolved:2025-11-10T20:56:00.738Z"
	issueResolved.Kind = event.KindResolved

	metricCritical := event.Event{
		Key:         "metric_alert:4:critical",
		Kind:        event.KindAlert,
		Sender:      alertSender,
		AlertName:   "Too many errors",
		Title:       "Critical: Too many errors",
		Text:        "1000 events in the last 10 minutes\nFilter: level:error",
		Service:     "bar",
		Severity:    event.SeverityCritical,
		Labels:      metricLabels,
		Fingerprint: event.Fingerprint(Adapter, "metric_alert", metricLabels, []string{"alert_rule_id"}),
		StartsAt:    time.Date(2020, 9, 13, 12, 26, 40, 0, time.UTC),
		ReceivedAt:  receivedAt,
		Links:       []event.Link{{Title: "Alert", URL: "https://sentry.io/organizations/baz/alerts/1/"}},
	}

	metricResolved := metricCritical
	metricResolved.Key = "metric_alert:4:resolved"
	metricResolved.Kind = event.KindResolved
	metricResolved.Title = "Resolved: Too many errors"
	metricResolved.Severity = event.SeverityUnknown
	metricResolved.EndsAt = time.Date(2020, 9, 13, 12, 46, 40, 0, time.UTC)

	tests := []struct {
		name    string
		file    string
		body    string
		want    []event.Event
		wantMsg string
	}{
		{
			name: "a recorded issue alert",
			file: "fixture/issue-alert.json",
			want: []event.Event{{
				Key:         "event_alert:e4874d664c3540c1a32eab185f12c5ab:Very Important Alert!",
				Kind:        event.KindAlert,
				Sender:      alertSender,
				AlertName:   "Very Important Alert!",
				Title:       "ReferenceError: heck is not defined",
				Text:        "?(<anonymous>)",
				Severity:    event.SeverityCritical,
				Labels:      issueAlertLabels,
				Fingerprint: event.Fingerprint(Adapter, "issue", issueAlertLabels, []string{"issue_id"}),
				StartsAt:    time.Date(2019, 8, 19, 21, 6, 17, 677_000_000, time.UTC),
				ReceivedAt:  receivedAt,
				Links: []event.Link{{
					Title: "Event",
					URL:   "https://sentry.io/organizations/test-org/issues/1117540176/events/e4874d664c3540c1a32eab185f12c5ab/",
				}},
			}},
		},
		{name: "a recorded critical metric alert", file: "fixture/metric-alert-critical.json", want: []event.Event{metricCritical}},
		{name: "a recorded resolved metric alert", file: "fixture/metric-alert-resolved.json", want: []event.Event{metricResolved}},
		{name: "a recorded new issue", file: "fixture/issue-created.json", want: []event.Event{issueCreated}},
		{name: "a recorded resolved issue", file: "fixture/issue-resolved.json", want: []event.Event{issueResolved}},
		{name: "a recorded assigned issue gives no event", file: "fixture/issue-assigned.json", want: []event.Event{}},
		{
			name: "a regressed issue is an alert again",
			body: `{"action": "unresolved", "data": {"issue": {"id": "5", "lastSeen": "2026-10-26T09:00:00Z"}}}`,
			want: []event.Event{{
				Key:         "issue:5:unresolved:2026-10-26T09:00:00Z",
				Kind:        event.KindAlert,
				Sender:      event.Sender{Kind: event.SenderSystem},
				Severity:    event.SeverityUnknown,
				Labels:      map[string]string{"issue_id": "5"},
				Fingerprint: event.Fingerprint(Adapter, "issue", map[string]string{"issue_id": "5"}, []string{"issue_id"}),
				ReceivedAt:  receivedAt,
			}},
		},
		{
			name: "an issue alert with a service tag and tags of the wrong size",
			body: `{"action": "triggered", "data": {"triggered_rule": "R", "event": {"event_id": "e", "tags": [["service", "checkout"], ["lonely"], ["a", "b", "c"]]}}}`,
			want: []event.Event{{
				Key:         "event_alert:e:R",
				Kind:        event.KindAlert,
				Sender:      event.Sender{Kind: event.SenderSystem},
				AlertName:   "R",
				Service:     "checkout",
				Severity:    event.SeverityUnknown,
				Labels:      map[string]string{"service": "checkout"},
				Fingerprint: event.Fingerprint(Adapter, "issue", nil, []string{"issue_id"}),
				ReceivedAt:  receivedAt,
			}},
		},
		{
			name: "a metric alert without a description and with several projects",
			body: `{"action": "warning", "data": {"metric_alert": {"id": "9", "projects": ["a", "b"], "alert_rule": {"id": "3", "name": "Slow", "environment": "prod"}}}}`,
			want: []event.Event{{
				Key:         "metric_alert:9:warning",
				Kind:        event.KindAlert,
				Sender:      event.Sender{Kind: event.SenderSystem},
				AlertName:   "Slow",
				Title:       "Slow",
				Severity:    event.SeverityWarning,
				Labels:      map[string]string{"alert_rule_id": "3", "environment": "prod"},
				Fingerprint: event.Fingerprint(Adapter, "metric_alert", map[string]string{"alert_rule_id": "3"}, []string{"alert_rule_id"}),
				ReceivedAt:  receivedAt,
			}},
		},
		{name: "an installation webhook gives no event", body: `{"action": "created", "data": {"installation": {"uuid": "u"}}}`, want: []event.Event{}},
		{name: "not JSON", body: `action: created`, wantMsg: "decode the Sentry payload"},
		{name: "not an object", body: `[]`, wantMsg: "decode the Sentry payload"},
		{name: "an issue alert without an event ID", body: `{"action": "triggered", "data": {"event": {}}}`, wantMsg: "data.event.event_id: required"},
		{name: "an issue alert with a bad time", body: `{"action": "triggered", "data": {"event": {"event_id": "e", "datetime": "yesterday"}}}`, wantMsg: "data.event.datetime: must be an RFC 3339 time"},
		{name: "a tag that is not a string", body: `{"action": "triggered", "data": {"event": {"event_id": "e", "tags": [["a", 1]]}}}`, wantMsg: "decode the Sentry payload"},
		{name: "a metric alert without an ID", body: `{"action": "critical", "data": {"metric_alert": {}}}`, wantMsg: "data.metric_alert.id: required"},
		{name: "a metric alert with another action", body: `{"action": "triggered", "data": {"metric_alert": {"id": "1"}}}`, wantMsg: `action: must be "critical", "warning" or "resolved"`},
		{name: "a metric alert with a bad start", body: `{"action": "critical", "data": {"metric_alert": {"id": "1", "date_started": "soon"}}}`, wantMsg: "data.metric_alert.date_started: must be an RFC 3339 time"},
		{name: "a metric alert with a bad end", body: `{"action": "resolved", "data": {"metric_alert": {"id": "1", "date_closed": "soon"}}}`, wantMsg: "data.metric_alert.date_closed: must be an RFC 3339 time"},
		{name: "an issue without an ID", body: `{"action": "created", "data": {"issue": {}}}`, wantMsg: "data.issue.id: required"},
		{name: "an issue with a bad first time", body: `{"action": "created", "data": {"issue": {"id": "1", "firstSeen": "x"}}}`, wantMsg: "data.issue.firstSeen: must be an RFC 3339 time"},
		{name: "an issue with a bad last time", body: `{"action": "created", "data": {"issue": {"id": "1", "lastSeen": "x"}}}`, wantMsg: "data.issue.lastSeen: must be an RFC 3339 time"},
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

			a, err := New(t.Context(), "sentry", Config{}, registry.Deps{})
			require.NoError(t, err)

			events, err := a.Parse(t.Context(), intake.Request{Endpoint: "sentry", Body: body, ReceivedAt: receivedAt})
			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
				require.NotContains(t, err.Error(), "yesterday", "errors never hold payload values")

				return
			}

			require.NoError(t, err)
			require.Len(t, events, len(tt.want))

			for i := range events {
				require.JSONEq(t, string(body), string(events[i].Raw))

				events[i].Raw = nil
			}

			require.Equal(t, tt.want, events)
		})
	}
}

func TestKeys(t *testing.T) {
	t.Parallel()

	created, err := os.ReadFile("fixture/issue-created.json")
	require.NoError(t, err)

	critical, err := os.ReadFile("fixture/metric-alert-critical.json")
	require.NoError(t, err)

	alert, err := os.ReadFile("fixture/issue-alert.json")
	require.NoError(t, err)

	tests := []struct {
		name     string
		base     string
		other    string
		wantSame bool
	}{
		{name: "a repeated issue webhook has the same key", base: string(created), other: string(created), wantSame: true},
		{name: "the same action with a later lastSeen has a new key", base: string(created), other: strings.Replace(string(created), "20:56:00.738000", "21:30:00.000000", 1)},
		{name: "a metric alert that becomes warning has a new key", base: string(critical), other: strings.Replace(string(critical), `"action": "critical"`, `"action": "warning"`, 1)},
		{name: "another incident of the rule has a new key", base: string(critical), other: strings.Replace(string(critical), `"id": "4"`, `"id": "5"`, 1)},
		{name: "a repeated issue alert has the same key", base: string(alert), other: string(alert), wantSame: true},
		{name: "another rule firing for the event has a new key", base: string(alert), other: strings.Replace(string(alert), `"triggered_rule": "Very Important Alert!"`, `"triggered_rule": "Other"`, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base, err := Sentry{}.Parse(t.Context(), intake.Request{Body: []byte(tt.base)})
			require.NoError(t, err)

			other, err := Sentry{}.Parse(t.Context(), intake.Request{Body: []byte(tt.other)})
			require.NoError(t, err)

			if tt.wantSame {
				require.Equal(t, base[0].Key, other[0].Key)
			} else {
				require.NotEqual(t, base[0].Key, other[0].Key)
			}
		})
	}
}

func TestIssueAlertAndIssueShareFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		alert string
		issue string
	}{
		{
			name:  "the same issue ID",
			alert: `{"action": "triggered", "data": {"triggered_rule": "R", "event": {"event_id": "e", "issue_id": "77", "project": 3}}}`,
			issue: `{"action": "created", "data": {"issue": {"id": "77", "project": {"id": "3", "slug": "web"}}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			alert, err := Sentry{}.Parse(t.Context(), intake.Request{Body: []byte(tt.alert)})
			require.NoError(t, err)

			issue, err := Sentry{}.Parse(t.Context(), intake.Request{Body: []byte(tt.issue)})
			require.NoError(t, err)
			require.Equal(t, alert[0].Fingerprint, issue[0].Fingerprint)
		})
	}
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	secret := []byte("sentry-client-secret")
	body := []byte(`{"action":"created","data":{"issue":{"id":"1"}}}`)
	valid := hex.EncodeToString(Sign(secret, body))

	tests := []struct {
		name      string
		signature string
		body      []byte
		wantMsg   string
	}{
		{name: "a right signature", signature: valid, body: body},
		{name: "a right signature in upper case", signature: strings.ToUpper(valid), body: body},
		{name: "no signature", body: body, wantMsg: "missing signature"},
		{name: "a signature that is not hex", signature: "zz", body: body, wantMsg: "decode signature"},
		{name: "a wrong signature", signature: hex.EncodeToString(Sign([]byte("other"), body)), body: body, wantMsg: "wrong signature"},
		{name: "a signature of another body", signature: valid, body: []byte(`{}`), wantMsg: "wrong signature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			header := http.Header{}
			if tt.signature != "" {
				header.Set(HeaderSignature, tt.signature)
			}

			err := Sentry{}.VerifySignature(intake.Request{Header: header, Body: tt.body}, secret)
			if tt.wantMsg == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantMsg)
			require.ErrorIs(t, err, intake.ErrUnauthenticated)
		})
	}
}
