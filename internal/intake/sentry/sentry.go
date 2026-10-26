// Package sentry is the webhook adapter for Sentry. It reads the webhooks
// of a Sentry internal integration: issue alerts, metric alerts and issue
// changes each become one event, and a resolved metric alert or issue a
// resolved event. Other webhooks, such as installation or comment ones,
// give no event. Sentry signs each delivery with the integration's client
// secret, which the adapter checks.
package sentry

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Adapter is the name config selects this adapter with, and the sender
// type in fingerprints.
const Adapter = "sentry"

// HeaderSignature holds Sentry's signature: the hex HMAC-SHA256 of the
// body, keyed with the integration's client secret.
const HeaderSignature = "Sentry-Hook-Signature"

// The actions of metric alert webhooks.
const (
	actionCritical = "critical"
	actionWarning  = "warning"
	actionResolved = "resolved"
)

// The actions of issue webhooks that become events. Sentry also sends
// assigned and archived, which give none.
const (
	actionCreated    = "created"
	actionUnresolved = "unresolved"
)

// The labels the adapter adds.
const (
	labelIssueID     = "issue_id"
	labelProject     = "project"
	labelProjectID   = "project_id"
	labelAlertRuleID = "alert_rule_id"
	labelEnvironment = "environment"
	labelService     = "service"
)

// The identities of the two kinds of alert in fingerprints. An issue alert
// and the changes of its issue share the issue's identity.
const (
	identityIssue       = "issue"
	identityMetricAlert = "metric_alert"
)

// The titles of the links the adapter adds.
const (
	linkEvent = "Event"
	linkAlert = "Alert"
	linkIssue = "Issue"
)

// tagSize is the number of elements of one tag: its name and its value.
const tagSize = 2

var (
	errNoSignature     = errors.New("missing signature")
	errBadSignature    = errors.New("wrong signature")
	errNoEventID       = errors.New("data.event.event_id: required")
	errNoMetricAlertID = errors.New("data.metric_alert.id: required")
	errNoIssueID       = errors.New("data.issue.id: required")
	errBadAction       = errors.New(`action: must be "critical", "warning" or "resolved"`)
	errBadTime         = errors.New("must be an RFC 3339 time")
)

func init() {
	registry.Intakes.Register(Adapter, registry.Typed(New))
}

// Config is the adapter's part of a webhook endpoint's config. The adapter
// has no settings of its own; the client secret is the endpoint's
// auth.hmac secret.
type Config struct{}

// Sentry maps a Sentry webhook payload to events and checks its signature.
type Sentry struct{}

var (
	_ intake.Adapter           = Sentry{}
	_ intake.SignatureVerifier = Sentry{}
)

// New builds the adapter.
func New(_ context.Context, _ string, _ Config, _ registry.Deps) (intake.Adapter, error) {
	return Sentry{}, nil
}

// payload is the part of a webhook body the adapter reads. Which member of
// data is set tells the kinds of webhook apart, so the body alone is
// enough to parse it again.
type payload struct {
	Action       string `json:"action"`
	Installation struct {
		UUID string `json:"uuid"`
	} `json:"installation"`
	Data struct {
		Event            *issueEvent  `json:"event"`
		TriggeredRule    string       `json:"triggered_rule"`
		MetricAlert      *metricAlert `json:"metric_alert"`
		DescriptionTitle string       `json:"description_title"`
		DescriptionText  string       `json:"description_text"`
		WebURL           string       `json:"web_url"`
		Issue            *issue       `json:"issue"`
	} `json:"data"`
}

// issueEvent is the error event that triggered an issue alert.
type issueEvent struct {
	EventID  string      `json:"event_id"`
	IssueID  string      `json:"issue_id"`
	Project  json.Number `json:"project"`
	Title    string      `json:"title"`
	Culprit  string      `json:"culprit"`
	Level    string      `json:"level"`
	Datetime string      `json:"datetime"`
	Tags     [][]string  `json:"tags"`
	WebURL   string      `json:"web_url"`
}

// metricAlert is the incident of a metric alert.
type metricAlert struct {
	ID          string   `json:"id"`
	DateStarted string   `json:"date_started"`
	DateClosed  string   `json:"date_closed"`
	Projects    []string `json:"projects"`
	AlertRule   struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Environment string `json:"environment"`
	} `json:"alert_rule"`
}

// issue is the issue of an issue webhook.
type issue struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Level     string `json:"level"`
	WebURL    string `json:"web_url"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
	Project   struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"project"`
}

// Parse implements intake.Adapter. It returns at most one event. Each key
// names the kind of webhook, Sentry's ID of what it is about and what
// happened to it, so a delivery Sentry sends again maps to the same key.
func (Sentry) Parse(_ context.Context, req intake.Request) ([]event.Event, error) {
	var p payload
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return nil, fmt.Errorf("decode the Sentry payload: %w", err)
	}

	var (
		ev   event.Event
		ok   bool
		err  error
		kind string
	)

	switch {
	case p.Data.Event != nil:
		kind = "issue alert"
		ev, err = issueAlert(p)
		ok = true
	case p.Data.MetricAlert != nil:
		kind = "metric alert"
		ev, err = metricAlertEvent(p)
		ok = true
	case p.Data.Issue != nil:
		kind = "issue"
		ev, ok, err = issueChange(p)
	}

	if err != nil {
		return nil, fmt.Errorf("read the %s webhook: %w", kind, err)
	}

	if !ok {
		return []event.Event{}, nil
	}

	ev.Sender = event.Sender{Kind: event.SenderSystem, ID: p.Installation.UUID}
	ev.ReceivedAt = req.ReceivedAt
	ev.Raw = json.RawMessage(req.Body)

	return []event.Event{ev}, nil
}

// VerifySignature implements intake.SignatureVerifier: the
// Sentry-Hook-Signature header must be the hex HMAC-SHA256 of the body,
// keyed with secret, the integration's client secret.
func (Sentry) VerifySignature(req intake.Request, secret []byte) error {
	sig := req.Header.Get(HeaderSignature)
	if sig == "" {
		return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errNoSignature)
	}

	got, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("%w: decode signature: %w", intake.ErrUnauthenticated, err)
	}

	if !hmac.Equal(got, Sign(secret, req.Body)) {
		return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errBadSignature)
	}

	return nil
}

// Sign returns the signature Sentry sends for body.
func Sign(secret, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)

	return mac.Sum(nil)
}

// issueAlert maps the webhook of an issue alert rule that fired.
func issueAlert(p payload) (event.Event, error) {
	e := p.Data.Event
	if e.EventID == "" {
		return event.Event{}, errNoEventID
	}

	at, err := parseTime(e.Datetime)
	if err != nil {
		return event.Event{}, fmt.Errorf("data.event.datetime: %w", err)
	}

	labels := map[string]string{}

	for _, tag := range e.Tags {
		if len(tag) == tagSize {
			labels[tag[0]] = tag[1]
		}
	}

	setLabel(labels, labelIssueID, e.IssueID)
	setLabel(labels, labelProjectID, e.Project.String())

	return event.Event{
		Key:         "event_alert:" + e.EventID + ":" + p.Data.TriggeredRule,
		Kind:        event.KindAlert,
		AlertName:   p.Data.TriggeredRule,
		Title:       e.Title,
		Text:        e.Culprit,
		Service:     labels[labelService],
		Severity:    intake.ParseSeverity(e.Level),
		Labels:      labels,
		Fingerprint: event.Fingerprint(Adapter, identityIssue, labels, []string{labelIssueID}),
		StartsAt:    at,
		Links:       links(linkEvent, e.WebURL),
	}, nil
}

// metricAlertEvent maps the webhook of a metric alert that became critical
// or warning, or resolved.
func metricAlertEvent(p payload) (event.Event, error) {
	m := p.Data.MetricAlert
	if m.ID == "" {
		return event.Event{}, errNoMetricAlertID
	}

	kind := event.KindAlert

	switch p.Action {
	case actionCritical, actionWarning:
	case actionResolved:
		kind = event.KindResolved
	default:
		return event.Event{}, errBadAction
	}

	started, err := parseTime(m.DateStarted)
	if err != nil {
		return event.Event{}, fmt.Errorf("data.metric_alert.date_started: %w", err)
	}

	closed, err := parseTime(m.DateClosed)
	if err != nil {
		return event.Event{}, fmt.Errorf("data.metric_alert.date_closed: %w", err)
	}

	labels := map[string]string{}
	setLabel(labels, labelAlertRuleID, m.AlertRule.ID)
	setLabel(labels, labelEnvironment, m.AlertRule.Environment)

	if len(m.Projects) == 1 {
		labels[labelProject] = m.Projects[0]
	}

	return event.Event{
		Key:         "metric_alert:" + m.ID + ":" + p.Action,
		Kind:        kind,
		AlertName:   m.AlertRule.Name,
		Title:       cmp.Or(p.Data.DescriptionTitle, m.AlertRule.Name),
		Text:        p.Data.DescriptionText,
		Service:     labels[labelProject],
		Severity:    intake.ParseSeverity(p.Action),
		Labels:      labels,
		Fingerprint: event.Fingerprint(Adapter, identityMetricAlert, labels, []string{labelAlertRuleID}),
		StartsAt:    started,
		EndsAt:      closed,
		Links:       links(linkAlert, p.Data.WebURL),
	}, nil
}

// issueChange maps the webhook of an issue that was created, resolved or
// unresolved. Other actions give no event: ok is false.
func issueChange(p payload) (ev event.Event, ok bool, err error) {
	is := p.Data.Issue

	kind := event.KindAlert

	switch p.Action {
	case actionCreated, actionUnresolved:
	case actionResolved:
		kind = event.KindResolved
	default:
		return event.Event{}, false, nil
	}

	if is.ID == "" {
		return event.Event{}, false, errNoIssueID
	}

	firstSeen, err := parseTime(is.FirstSeen)
	if err != nil {
		return event.Event{}, false, fmt.Errorf("data.issue.firstSeen: %w", err)
	}

	lastSeen, err := parseTime(is.LastSeen)
	if err != nil {
		return event.Event{}, false, fmt.Errorf("data.issue.lastSeen: %w", err)
	}

	labels := map[string]string{labelIssueID: is.ID}
	setLabel(labels, labelProject, is.Project.Slug)
	setLabel(labels, labelProjectID, is.Project.ID)

	return event.Event{
		// lastSeen tells a resolution or a regression from an earlier one
		// of the same issue.
		Key:         "issue:" + is.ID + ":" + p.Action + ":" + lastSeen.UTC().Format(time.RFC3339Nano),
		Kind:        kind,
		AlertName:   is.ShortID,
		Title:       is.Title,
		Text:        is.Culprit,
		Service:     is.Project.Slug,
		Severity:    intake.ParseSeverity(is.Level),
		Labels:      labels,
		Fingerprint: event.Fingerprint(Adapter, identityIssue, labels, []string{labelIssueID}),
		StartsAt:    firstSeen,
		Links:       links(linkIssue, is.WebURL),
	}, true, nil
}

// parseTime reads an RFC 3339 time and returns it in UTC. An empty string
// is the zero time. The error never holds the value.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}

	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, errBadTime
	}

	return t.UTC(), nil
}

// setLabel sets the label name to value, unless value is empty.
func setLabel(labels map[string]string, name, value string) {
	if value != "" {
		labels[name] = value
	}
}

// links returns one link to url titled title, or none when url is empty.
func links(title, url string) []event.Link {
	if url == "" {
		return nil
	}

	return []event.Link{{Title: title, URL: url}}
}
