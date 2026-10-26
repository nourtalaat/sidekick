package generic

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/jsonpath"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var receivedAt = time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	t.Parallel()

	full := Fields{
		Title:    path(t, "$.alert.title"),
		Text:     path(t, "$.alert.description"),
		Service:  path(t, "$.labels.service"),
		Severity: path(t, "$.alert.level"),
		Key:      path(t, "$.alert.id"),
		Labels:   path(t, "$.labels"),
	}

	tests := []struct {
		name    string
		fields  Fields
		file    string
		body    string
		want    func(body []byte) event.Event
		wantMsg []string
	}{
		{
			name:   "every field from a recorded payload",
			fields: full,
			file:   "fixture/ci-alert.json",
			want: func(body []byte) event.Event {
				labels := map[string]string{"service": "checkout", "env": "prod", "pipeline": "nightly"}

				return event.Event{
					Key:         "build-4711",
					Kind:        event.KindAlert,
					Sender:      event.Sender{Kind: event.SenderSystem},
					Title:       "Nightly build failed",
					Text:        "The integration stage failed on main after 3 retries.",
					Service:     "checkout",
					Severity:    event.SeverityCritical,
					Labels:      labels,
					Fingerprint: event.Fingerprint(Adapter, "Nightly build failed", labels, []string{"env", "pipeline", "service"}),
					ReceivedAt:  receivedAt,
					Raw:         body,
				}
			},
		},
		{
			name:   "numbers and booleans are taken as text",
			fields: Fields{Title: path(t, "$.alert.title"), Text: path(t, "$.alert.flaky"), Key: path(t, "$.alert.attempts"), Severity: path(t, "$.steps[1].status")},
			file:   "fixture/ci-alert.json",
			want: func(body []byte) event.Event {
				return event.Event{
					Key:         "3",
					Kind:        event.KindAlert,
					Sender:      event.Sender{Kind: event.SenderSystem},
					Title:       "Nightly build failed",
					Text:        "false",
					Severity:    event.SeverityUnknown,
					Labels:      map[string]string{},
					Fingerprint: event.Fingerprint(Adapter, "Nightly build failed", nil, nil),
					ReceivedAt:  receivedAt,
					Raw:         body,
				}
			},
		},
		{
			name:   "optional fields that find nothing stay empty and the key is the body's hash",
			fields: Fields{Title: path(t, "$.alert.title"), Text: path(t, "$.alert.description"), Service: path(t, "$.labels.service"), Severity: path(t, "$.alert.level"), Labels: path(t, "$.labels")},
			file:   "fixture/minimal.json",
			want: func(body []byte) event.Event {
				sum := sha256.Sum256(body)

				return event.Event{
					Key:         hex.EncodeToString(sum[:]),
					Kind:        event.KindAlert,
					Sender:      event.Sender{Kind: event.SenderSystem},
					Title:       "Queue is backing up",
					Severity:    event.SeverityUnknown,
					Labels:      map[string]string{},
					Fingerprint: event.Fingerprint(Adapter, "Queue is backing up", nil, nil),
					ReceivedAt:  receivedAt,
					Raw:         body,
				}
			},
		},
		{
			name:   "the service joins the fingerprint when no label has its name",
			fields: Fields{Title: path(t, "$.alert.title"), Service: path(t, "$.alert.service")},
			body:   `{"alert": {"title": "Down", "service": "cart"}}`,
			want: func(body []byte) event.Event {
				sum := sha256.Sum256(body)

				return event.Event{
					Key:         hex.EncodeToString(sum[:]),
					Kind:        event.KindAlert,
					Sender:      event.Sender{Kind: event.SenderSystem},
					Title:       "Down",
					Service:     "cart",
					Severity:    event.SeverityUnknown,
					Labels:      map[string]string{},
					Fingerprint: event.Fingerprint(Adapter, "Down", map[string]string{"service": "cart"}, []string{"service"}),
					ReceivedAt:  receivedAt,
					Raw:         body,
				}
			},
		},
		{
			name:    "a missing title is an error",
			fields:  full,
			body:    `{"alert": {"id": "1"}}`,
			wantMsg: []string{"fields.title ($.alert.title): finds nothing in the payload"},
		},
		{
			name:    "a null title counts as missing",
			fields:  full,
			body:    `{"alert": {"id": "1", "title": null}}`,
			wantMsg: []string{"fields.title ($.alert.title): finds nothing"},
		},
		{
			name:    "a configured key must be found",
			fields:  full,
			file:    "fixture/minimal.json",
			wantMsg: []string{"fields.key ($.alert.id): finds nothing in the payload"},
		},
		{
			name:    "an object where text is expected is an error",
			fields:  Fields{Title: path(t, "$.alert"), Text: path(t, "$.steps")},
			file:    "fixture/ci-alert.json",
			wantMsg: []string{"fields.title ($.alert): expected a string, a number or true or false", "fields.text ($.steps): expected a string"},
		},
		{
			name:    "a path that finds several values is an error",
			fields:  Fields{Title: path(t, "$.steps[*].name")},
			file:    "fixture/ci-alert.json",
			wantMsg: []string{"fields.title ($.steps[*].name): finds more than one value"},
		},
		{
			name:    "labels must be an object",
			fields:  Fields{Title: path(t, "$.alert.title"), Labels: path(t, "$.steps")},
			file:    "fixture/ci-alert.json",
			wantMsg: []string{"fields.labels ($.steps): expected an object"},
		},
		{
			name:    "label values must be text",
			fields:  Fields{Title: path(t, "$.alert.title"), Labels: path(t, "$")},
			file:    "fixture/ci-alert.json",
			wantMsg: []string{`fields.labels ($): label "alert": expected a string`, `label "labels"`, `label "steps"`},
		},
		{
			name:    "labels found more than once",
			fields:  Fields{Title: path(t, "$.alert.title"), Labels: path(t, "$.steps[*]")},
			file:    "fixture/ci-alert.json",
			wantMsg: []string{"fields.labels ($.steps[*]): finds more than one value"},
		},
		{
			name:    "not JSON",
			fields:  full,
			body:    `alert: disk full`,
			wantMsg: []string{"decode the payload as JSON"},
		},
		{
			name:    "not an object",
			fields:  full,
			body:    `["alert"]`,
			wantMsg: []string{"the payload must be a JSON object"},
		},
		{
			name:    "more than one JSON value",
			fields:  full,
			body:    `{"alert": {"title": "a", "id": "1"}} {}`,
			wantMsg: []string{"more than one JSON value"},
		},
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

			a, err := New(t.Context(), "ci", Config{Fields: tt.fields}, registry.Deps{})
			require.NoError(t, err)

			events, err := a.Parse(t.Context(), intake.Request{Endpoint: "ci", Body: body, ReceivedAt: receivedAt})
			if tt.wantMsg != nil {
				require.Error(t, err)

				for _, msg := range tt.wantMsg {
					require.ErrorContains(t, err, msg)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, []event.Event{tt.want(body)}, events)
		})
	}
}

func TestConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		want    Fields
		wantMsg string
	}{
		{
			name: "every field",
			yaml: "fields: {title: $.a, text: $.b, service: $.c, severity: $.d, key: $.e, labels: $.f}",
			want: Fields{
				Title: path(t, "$.a"), Text: path(t, "$.b"), Service: path(t, "$.c"),
				Severity: path(t, "$.d"), Key: path(t, "$.e"), Labels: path(t, "$.f"),
			},
		},
		{name: "title only", yaml: "fields: {title: '$[\"x.y\"]'}", want: Fields{Title: path(t, `$["x.y"]`)}},
		{name: "no title", yaml: "fields: {text: $.b}", wantMsg: "fields.title: required"},
		{name: "no fields", yaml: "{}", wantMsg: "fields.title: required"},
		{name: "bad path", yaml: "fields: {title: alert.title}", wantMsg: "must start with $"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &node))

			cfg, err := registry.Typed(New).Check(t.Context(), "ci", node.Content[0])
			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.(*Config).Fields)
		})
	}
}

func path(t *testing.T, s string) jsonpath.Path {
	t.Helper()

	p, err := jsonpath.Parse(s)
	require.NoError(t, err)

	return p
}
