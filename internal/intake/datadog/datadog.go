// Package datadog is the webhook adapter for Datadog monitor
// notifications. Datadog's webhooks integration posts a JSON payload that
// the user writes from template variables; the setup docs give the payload
// this adapter reads, and Datadog's default payload works too, with fewer
// fields. Each delivery becomes one event, and a Recovered notification a
// resolved event.
package datadog

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Adapter is the name config selects this adapter with, and the sender
// type in fingerprints.
const Adapter = "datadog"

// The values of $ALERT_TRANSITION and $ALERT_TYPE that mean the monitor
// recovered.
const (
	transitionRecovered = "Recovered"
	typeSuccess         = "success"
)

// labelService is the tag that names the service.
const labelService = "service"

// The titles of the links the adapter adds.
const (
	linkEvent    = "Event"
	linkSnapshot = "Snapshot"
)

// prioritySeverities maps monitor priorities to severities, for payloads
// whose alert type does not give one.
var prioritySeverities = map[string]event.Severity{
	"P1": event.SeverityCritical,
	"P2": event.SeverityCritical,
	"P3": event.SeverityWarning,
	"P4": event.SeverityInfo,
	"P5": event.SeverityInfo,
}

// titlePrefix matches the bracketed parts Datadog puts before a monitor's
// name in its titles, such as "[P1] [Triggered on {host:a}] ".
var titlePrefix = regexp.MustCompile(`^(\[[^\]]*\]\s*)+`)

var (
	errNoID    = errors.New("id: required")
	errNoTitle = errors.New("title or alert_title: required")
	errBadDate = errors.New("date: must be a number of milliseconds since 1970")
	errNotText = errors.New("id, date, alert_id and org.id must each be a string or a number")
)

func init() {
	registry.Intakes.Register(Adapter, registry.Typed(New))
}

// Config is the adapter's part of a webhook endpoint's config. The adapter
// has no settings of its own.
type Config struct{}

// Datadog maps a Datadog webhook payload to an event.
type Datadog struct{}

var _ intake.Adapter = Datadog{}

// New builds the adapter.
func New(_ context.Context, _ string, _ Config, _ registry.Deps) (intake.Adapter, error) {
	return Datadog{}, nil
}

// payload is the webhook body, with the Datadog template variable each
// member is meant to hold. Every member may be missing.
type payload struct {
	ID              text   `json:"id"`               // $ID
	Title           string `json:"title"`            // $EVENT_TITLE
	Body            string `json:"body"`             // $TEXT_ONLY_MSG or $EVENT_MSG
	Date            text   `json:"date"`             // $DATE
	Org             org    `json:"org"`              // $ORG_ID and $ORG_NAME
	AlertID         text   `json:"alert_id"`         // $ALERT_ID
	AlertTitle      string `json:"alert_title"`      // $ALERT_TITLE
	AlertTransition string `json:"alert_transition"` // $ALERT_TRANSITION
	AlertType       string `json:"alert_type"`       // $ALERT_TYPE
	AlertPriority   string `json:"alert_priority"`   // $ALERT_PRIORITY
	AlertScope      string `json:"alert_scope"`      // $ALERT_SCOPE
	Tags            string `json:"tags"`             // $TAGS
	Link            string `json:"link"`             // $LINK
	Snapshot        string `json:"snapshot"`         // $SNAPSHOT
}

// org is the organization the monitor belongs to.
type org struct {
	ID   text   `json:"id"`
	Name string `json:"name"`
}

// text is a member that holds a string or a number, so a payload works
// whether its template quotes a numeric variable such as $ID or not.
type text string

// UnmarshalJSON implements json.Unmarshaler.
func (t *text) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		return nil
	}

	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = text(s)

		return nil
	}

	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return errNotText
	}

	*t = text(n)

	return nil
}

// Parse implements intake.Adapter. The key is Datadog's event ID, which a
// delivery Datadog sends again keeps, while each new notification of a
// monitor, a renotification included, gets a new one. Errors name the
// member that is wrong, never its value.
func (Datadog) Parse(_ context.Context, req intake.Request) ([]event.Event, error) {
	var p payload
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return nil, fmt.Errorf("decode the Datadog payload: %w", err)
	}

	if strings.TrimSpace(string(p.ID)) == "" {
		return nil, errNoID
	}

	title := cmp.Or(p.Title, p.AlertTitle)
	if title == "" {
		return nil, errNoTitle
	}

	at, err := parseMillis(string(p.Date))
	if err != nil {
		return nil, err
	}

	alertName := monitorName(cmp.Or(p.AlertTitle, title))
	scope := parseTags(p.AlertScope)

	labels := parseTags(p.Tags)
	maps.Copy(labels, scope)

	ev := event.Event{
		Key:         string(p.ID),
		Kind:        event.KindAlert,
		Sender:      event.Sender{Kind: event.SenderSystem, ID: string(p.Org.ID), Name: p.Org.Name},
		AlertName:   alertName,
		Title:       title,
		Text:        p.Body,
		Service:     labels[labelService],
		Severity:    severity(p),
		Labels:      labels,
		Fingerprint: event.Fingerprint(Adapter, cmp.Or(string(p.AlertID), alertName), scope, slices.Collect(maps.Keys(scope))),
		StartsAt:    at,
		ReceivedAt:  req.ReceivedAt,
		Links:       links(p),
		Raw:         json.RawMessage(req.Body),
	}

	if p.AlertTransition == transitionRecovered || p.AlertType == typeSuccess {
		ev.Kind = event.KindResolved
		ev.StartsAt, ev.EndsAt = time.Time{}, at
	}

	return []event.Event{ev}, nil
}

// severity reads the alert type, then the monitor's priority.
func severity(p payload) event.Severity {
	if sev := intake.ParseSeverity(p.AlertType); sev != event.SeverityUnknown {
		return sev
	}

	if sev, ok := prioritySeverities[strings.ToUpper(strings.TrimSpace(p.AlertPriority))]; ok {
		return sev
	}

	return event.SeverityUnknown
}

// links returns the event's page and the graph snapshot, when the payload
// has them.
func links(p payload) []event.Link {
	var out []event.Link

	if p.Link != "" {
		out = append(out, event.Link{Title: linkEvent, URL: p.Link})
	}

	if p.Snapshot != "" {
		out = append(out, event.Link{Title: linkSnapshot, URL: p.Snapshot})
	}

	return out
}

// parseMillis reads a time in milliseconds since 1970. An empty string is
// the zero time.
func parseMillis(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}

	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, errBadDate
	}

	return time.UnixMilli(ms).UTC(), nil
}

// parseTags reads a comma-separated list of tags such as
// "env:prod, service:checkout, monitor". A tag's name ends at its first
// colon; a tag without one has an empty value.
func parseTags(s string) map[string]string {
	tags := map[string]string{}

	for tag := range strings.SplitSeq(s, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}

		name, value, _ := strings.Cut(tag, ":")
		tags[name] = value
	}

	return tags
}

// monitorName removes the bracketed parts Datadog puts before the name of
// the monitor in its titles.
func monitorName(title string) string {
	return strings.TrimSpace(titlePrefix.ReplaceAllString(title, ""))
}
