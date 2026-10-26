// Package grafana is the webhook adapter for Grafana Alerting. It reads
// the default payload of a Grafana webhook contact point (version 1) and
// returns one event per alert in the delivery. Resolved alerts become
// resolved events. Grafana authenticates with a bearer token, which
// Sidekick checks itself, or, when the endpoint has a signature section,
// with its own HMAC signature, which the adapter checks.
package grafana

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
const Adapter = "grafana"

// defaultSignatureHeader is the header Grafana puts its signature in
// unless the contact point names another.
const defaultSignatureHeader = "X-Grafana-Alerting-Signature"

// defaultTolerance is how far a signed timestamp may be from the time the
// delivery arrived, unless the endpoint sets another.
const defaultTolerance = 5 * time.Minute

// payloadVersion is the webhook payload version this adapter reads.
const payloadVersion = "1"

// timestampSeparator joins the timestamp and the body in what Grafana signs.
const timestampSeparator = ":"

// The alert statuses Grafana sends.
const (
	statusFiring   = "firing"
	statusResolved = "resolved"
)

// The labels and annotations the adapter reads.
const (
	labelAlertName        = "alertname"
	labelService          = "service"
	labelSeverity         = "severity"
	annotationSummary     = "summary"
	annotationDescription = "description"
	annotationRunbook     = "runbook_url"
)

// The titles of the links the adapter adds.
const (
	linkSource    = "Source"
	linkRunbook   = "Runbook"
	linkDashboard = "Dashboard"
	linkPanel     = "Panel"
	linkImage     = "Image"
)

var (
	errNoAlerts        = errors.New("alerts: required")
	errNoFingerprint   = errors.New("fingerprint: required")
	errUnknownStatus   = errors.New(`status: must be "firing" or "resolved"`)
	errUnknownVersion  = errors.New(`version: only version "1" is supported; leave the contact point's custom payload empty`)
	errBadTime         = errors.New("must be an RFC 3339 time")
	errBadHeader       = errors.New("must be a header name, without spaces or colons")
	errBadTolerance    = errors.New("signature.tolerance: must not be negative")
	errNoSignature     = errors.New("missing signature")
	errBadSignature    = errors.New("wrong signature")
	errNoTimestamp     = errors.New("missing or unreadable timestamp")
	errTimestampTooFar = errors.New("timestamp outside tolerance")
)

func init() {
	registry.Intakes.Register(Adapter, registry.Typed(New))
}

// Config is the adapter's part of a webhook endpoint's config.
type Config struct {
	// Signature, when set, makes the adapter check Grafana's own HMAC
	// signature, with the secret the endpoint's auth names, instead of a
	// bearer token.
	Signature *Signature `yaml:"signature"`
}

// Signature is how the contact point signs deliveries: the hex
// HMAC-SHA256 of the body, or of the timestamp, a ":" and the body when
// the contact point sets a timestamp header.
type Signature struct {
	// Header holds the signature. The default is
	// X-Grafana-Alerting-Signature, Grafana's own default.
	Header string `yaml:"header"`
	// TimestampHeader holds the time of sending in Unix seconds. Without
	// it the signature does not cover the time, so it cannot stop a
	// delivery from being sent again later.
	TimestampHeader string `yaml:"timestampHeader"`
	// Tolerance is how far the timestamp may be from the time the delivery
	// arrived. The default is 5 minutes.
	Tolerance time.Duration `yaml:"tolerance"`
}

// Validate checks the header names and the tolerance.
func (c *Config) Validate() error {
	if c.Signature == nil {
		return nil
	}

	if !validHeader(c.Signature.Header) {
		return fmt.Errorf("signature.header: %w", errBadHeader)
	}

	if !validHeader(c.Signature.TimestampHeader) {
		return fmt.Errorf("signature.timestampHeader: %w", errBadHeader)
	}

	if c.Signature.Tolerance < 0 {
		return errBadTolerance
	}

	return nil
}

// Grafana maps a Grafana webhook payload to events.
type Grafana struct{}

// signed is Grafana that also checks Grafana's signature.
type signed struct {
	Grafana

	header          string
	timestampHeader string
	tolerance       time.Duration
}

var (
	_ intake.Adapter           = Grafana{}
	_ intake.SignatureVerifier = (*signed)(nil)
)

// New builds the adapter: without a signature section, one the endpoint's
// bearer token authenticates; with one, one that checks Grafana's
// signature.
func New(_ context.Context, _ string, cfg Config, _ registry.Deps) (intake.Adapter, error) {
	if cfg.Signature == nil {
		return Grafana{}, nil
	}

	return &signed{
		header:          cmp.Or(cfg.Signature.Header, defaultSignatureHeader),
		timestampHeader: cfg.Signature.TimestampHeader,
		tolerance:       cmp.Or(cfg.Signature.Tolerance, defaultTolerance),
	}, nil
}

// payload is the part of Grafana's webhook body the adapter reads.
type payload struct {
	Version     string            `json:"version"`
	GroupKey    string            `json:"groupKey"`
	Receiver    string            `json:"receiver"`
	ExternalURL string            `json:"externalURL"`
	Alerts      []json.RawMessage `json:"alerts"`
}

// alert is one alert in the payload.
type alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
	DashboardURL string            `json:"dashboardURL"`
	PanelURL     string            `json:"panelURL"`
	ImageURL     string            `json:"imageURL"`
}

// Parse implements intake.Adapter. It returns the alerts in the order of
// the payload. Each event's key is a hash of the group key, the alert's
// fingerprint, its status and its start time, so a notification Grafana
// repeats maps to the same key, while the alert resolving gives a new one.
// Each event's Raw is the payload with only its own alert in alerts.
func (Grafana) Parse(_ context.Context, req intake.Request) ([]event.Event, error) {
	var p payload
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return nil, fmt.Errorf("decode the Grafana payload: %w", err)
	}

	if p.Version != payloadVersion {
		return nil, errUnknownVersion
	}

	if p.Alerts == nil {
		return nil, errNoAlerts
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &top); err != nil {
		return nil, fmt.Errorf("decode the Grafana payload: %w", err)
	}

	events := make([]event.Event, 0, len(p.Alerts))

	for i, raw := range p.Alerts {
		ev, err := toEvent(p, raw, req.ReceivedAt)
		if err != nil {
			return nil, fmt.Errorf("alerts[%d]: %w", i, err)
		}

		if ev.Raw, err = rawWithAlert(top, raw); err != nil {
			return nil, fmt.Errorf("alerts[%d]: %w", i, err)
		}

		events = append(events, ev)
	}

	return events, nil
}

// VerifySignature implements intake.SignatureVerifier. The signature
// header must hold the hex HMAC-SHA256, keyed with secret, of the body, or
// of the timestamp, a ":" and the body when a timestamp header is set.
// The timestamp must then be within the tolerance of the time the
// delivery arrived.
func (s *signed) VerifySignature(req intake.Request, secret []byte) error {
	sig := req.Header.Get(s.header)
	if sig == "" {
		return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errNoSignature)
	}

	got, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("%w: decode signature: %w", intake.ErrUnauthenticated, err)
	}

	mac := hmac.New(sha256.New, secret)

	if s.timestampHeader != "" {
		ts := req.Header.Get(s.timestampHeader)

		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errNoTimestamp)
		}

		if age := req.ReceivedAt.Sub(time.Unix(sec, 0)); age > s.tolerance || age < -s.tolerance {
			return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errTimestampTooFar)
		}

		mac.Write([]byte(ts + timestampSeparator))
	}

	mac.Write(req.Body)

	if !hmac.Equal(got, mac.Sum(nil)) {
		return fmt.Errorf("%w: %w", intake.ErrUnauthenticated, errBadSignature)
	}

	return nil
}

// toEvent maps one alert of the payload to an event, without Raw.
func toEvent(p payload, raw json.RawMessage, receivedAt time.Time) (event.Event, error) {
	var a alert
	if err := json.Unmarshal(raw, &a); err != nil {
		return event.Event{}, fmt.Errorf("decode alert: %w", err)
	}

	if a.Fingerprint == "" {
		return event.Event{}, errNoFingerprint
	}

	kind := event.KindAlert

	switch a.Status {
	case statusFiring:
	case statusResolved:
		kind = event.KindResolved
	default:
		return event.Event{}, errUnknownStatus
	}

	startsAt, err := parseTime(a.StartsAt)
	if err != nil {
		return event.Event{}, fmt.Errorf("startsAt: %w", err)
	}

	endsAt, err := parseTime(a.EndsAt)
	if err != nil {
		return event.Event{}, fmt.Errorf("endsAt: %w", err)
	}

	labels := a.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	alertName := labels[labelAlertName]

	return event.Event{
		Key:         key(p.GroupKey, a, startsAt),
		Kind:        kind,
		Sender:      event.Sender{Kind: event.SenderSystem, ID: p.ExternalURL, Name: p.Receiver},
		AlertName:   alertName,
		Title:       cmp.Or(a.Annotations[annotationSummary], alertName),
		Text:        a.Annotations[annotationDescription],
		Service:     labels[labelService],
		Severity:    intake.ParseSeverity(labels[labelSeverity]),
		Labels:      labels,
		Fingerprint: event.Fingerprint(Adapter, alertName, labels, slices.Collect(maps.Keys(labels))),
		StartsAt:    startsAt,
		EndsAt:      endsAt,
		ReceivedAt:  receivedAt,
		Links:       links(a),
	}, nil
}

// key hashes what identifies one notification of one alert.
func key(groupKey string, a alert, startsAt time.Time) string {
	h := sha256.New()

	for _, part := range []string{groupKey, a.Fingerprint, a.Status, startsAt.UTC().Format(time.RFC3339Nano)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil))
}

// links returns the alert's rule, runbook, dashboard, panel and image,
// when it has them.
func links(a alert) []event.Link {
	var out []event.Link

	for _, l := range []event.Link{
		{Title: linkSource, URL: a.GeneratorURL},
		{Title: linkRunbook, URL: a.Annotations[annotationRunbook]},
		{Title: linkDashboard, URL: a.DashboardURL},
		{Title: linkPanel, URL: a.PanelURL},
		{Title: linkImage, URL: a.ImageURL},
	} {
		if l.URL != "" {
			out = append(out, l)
		}
	}

	return out
}

// rawWithAlert returns the payload's top-level members with alerts holding
// only the one alert.
func rawWithAlert(top map[string]json.RawMessage, alert json.RawMessage) (json.RawMessage, error) {
	one := maps.Clone(top)
	one["alerts"] = json.RawMessage("[" + string(alert) + "]")

	out, err := json.Marshal(one)
	if err != nil {
		return nil, fmt.Errorf("encode the alert's payload: %w", err)
	}

	return out, nil
}

// parseTime reads an RFC 3339 time and returns it in UTC. An empty string
// is the zero time, and so is Grafana's "0001-01-01T00:00:00Z" for an
// alert that has not ended. The error never holds the value.
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

// validHeader reports whether name can be a header name. The empty string
// counts, as it means the default.
func validHeader(name string) bool {
	return !strings.ContainsAny(name, " \t\r\n:")
}
