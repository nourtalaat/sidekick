package router

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestMatcher(t *testing.T) {
	t.Parallel()

	payments := candidate{
		intake:   "alertmanager",
		labels:   map[string]string{"team": "payments", "env": "prod"},
		service:  "checkout-api",
		severity: event.SeverityCritical,
		text:     "High p99 latency\nCheckout is slow.",
	}

	all := &config.Match{
		Intake:   []string{"alertmanager", "grafana"},
		Labels:   map[string]string{"team": "payments"},
		Service:  "checkout-*",
		Severity: []event.Severity{event.SeverityWarning, event.SeverityCritical},
		Text:     "(?i)latency|p99",
	}

	tests := []struct {
		name  string
		match *config.Match
		event candidate
		want  bool
	}{
		{name: "no match matches every event", match: nil, event: candidate{}, want: true},
		{name: "an empty match matches every event", match: &config.Match{}, event: payments, want: true},
		{name: "intake listed", match: &config.Match{Intake: []string{"grafana", "alertmanager"}}, event: payments, want: true},
		{name: "intake not listed", match: &config.Match{Intake: []string{"grafana"}}, event: payments, want: false},
		{name: "every label present", match: &config.Match{Labels: map[string]string{"team": "payments", "env": "prod"}}, event: payments, want: true},
		{name: "a label with another value", match: &config.Match{Labels: map[string]string{"team": "billing"}}, event: payments, want: false},
		{name: "a label missing", match: &config.Match{Labels: map[string]string{"region": ""}}, event: payments, want: false},
		{name: "service glob matches", match: &config.Match{Service: "checkout-*"}, event: payments, want: true},
		{name: "service glob does not match", match: &config.Match{Service: "billing-*"}, event: payments, want: false},
		{name: "service glob and no service", match: &config.Match{Service: "*"}, event: candidate{}, want: true},
		{name: "service glob that needs a service", match: &config.Match{Service: "?*"}, event: candidate{}, want: false},
		{name: "severity listed", match: &config.Match{Severity: []event.Severity{event.SeverityCritical}}, event: payments, want: true},
		{name: "severity not listed", match: &config.Match{Severity: []event.Severity{event.SeverityInfo}}, event: payments, want: false},
		{name: "text pattern in the title", match: &config.Match{Text: "p99"}, event: payments, want: true},
		{name: "text pattern in the body", match: &config.Match{Text: "is slow"}, event: payments, want: true},
		{name: "text pattern across the title and the body", match: &config.Match{Text: "latency\nCheckout"}, event: payments, want: true},
		{name: "text pattern is case sensitive by default", match: &config.Match{Text: "LATENCY"}, event: payments, want: false},
		{name: "text pattern does not match", match: &config.Match{Text: "disk"}, event: payments, want: false},
		{name: "every field matches", match: all, event: payments, want: true},
		{name: "every field but one matches", match: all, event: withSeverity(payments, event.SeverityInfo), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, err := newMatcher(tt.match)
			require.NoError(t, err)
			require.Equal(t, tt.want, m.matches(tt.event))
		})
	}
}

func TestNewMatcherRejectsBadPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		match   config.Match
		wantErr string
	}{
		{name: "bad glob", match: config.Match{Service: "["}, wantErr: "service: invalid glob"},
		{name: "bad regular expression", match: config.Match{Text: "("}, wantErr: "text: error parsing regexp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newMatcher(&tt.match)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCandidateOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		event   db.Event
		want    candidate
		wantErr bool
	}{
		{
			name: "every field set",
			event: db.Event{
				Intake:   "alertmanager",
				Labels:   json.RawMessage(`{"team": "payments"}`),
				Service:  ptr("checkout-api"),
				Severity: ptr("critical"),
				Title:    ptr("High latency"),
				Body:     ptr("Checkout is slow."),
			},
			want: candidate{
				intake:   "alertmanager",
				labels:   map[string]string{"team": "payments"},
				service:  "checkout-api",
				severity: event.SeverityCritical,
				text:     "High latency\nCheckout is slow.",
			},
		},
		{
			name:  "only a body",
			event: db.Event{Intake: "grafana", Labels: json.RawMessage(`{}`), Title: ptr(""), Body: ptr("Disk full")},
			want:  candidate{intake: "grafana", labels: map[string]string{}, text: "Disk full"},
		},
		{
			name:  "no labels and no text",
			event: db.Event{Intake: "grafana"},
			want:  candidate{intake: "grafana", labels: map[string]string{}},
		},
		{
			name:    "labels that are not an object of text",
			event:   db.Event{Labels: json.RawMessage(`{"team": 1}`)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := candidateOf(tt.event)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func withSeverity(c candidate, s event.Severity) candidate {
	c.severity = s

	return c
}

func ptr(s string) *string {
	return &s
}
