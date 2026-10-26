// Package alertmanager is the webhook adapter for Prometheus Alertmanager.
// It reads Alertmanager's webhook payload (version 4) and returns one event
// per alert in the delivery. Resolved alerts become resolved events.
package alertmanager

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Adapter is the name config selects this adapter with, and the sender
// type in fingerprints.
const Adapter = "alertmanager"

// payloadVersion is the webhook payload version this adapter reads.
const payloadVersion = "4"

// The alert statuses Alertmanager sends.
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
	linkSource  = "Source"
	linkRunbook = "Runbook"
)

var (
	errNoAlerts       = errors.New("alerts: required")
	errNoFingerprint  = errors.New("fingerprint: required")
	errUnknownStatus  = errors.New(`status: must be "firing" or "resolved"`)
	errUnknownVersion = errors.New(`version: only version "4" is supported`)
)

func init() {
	registry.Intakes.Register(Adapter, registry.Typed(New))
}

// Config is the adapter's part of a webhook endpoint's config. The adapter
// has no settings of its own.
type Config struct{}

// Alertmanager maps an Alertmanager webhook payload to events.
type Alertmanager struct{}

var _ intake.Adapter = Alertmanager{}

// New builds the adapter.
func New(_ context.Context, _ string, _ Config, _ registry.Deps) (intake.Adapter, error) {
	return Alertmanager{}, nil
}

// payload is the part of Alertmanager's webhook body the adapter reads.
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
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Parse implements intake.Adapter. It returns the alerts in the order of
// the payload. Each event's key is a hash of the group key, the alert's
// fingerprint, its status and its start time, so a notification that
// Alertmanager repeats maps to the same key, while the alert resolving
// gives a new one. Each event's Raw is the payload with only its own alert
// in alerts.
func (Alertmanager) Parse(_ context.Context, req intake.Request) ([]event.Event, error) {
	var p payload
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return nil, fmt.Errorf("decode the Alertmanager payload: %w", err)
	}

	if p.Version != payloadVersion {
		return nil, errUnknownVersion
	}

	if p.Alerts == nil {
		return nil, errNoAlerts
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &top); err != nil {
		return nil, fmt.Errorf("decode the Alertmanager payload: %w", err)
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

	labels := a.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	alertName := labels[labelAlertName]

	return event.Event{
		Key:         key(p.GroupKey, a),
		Kind:        kind,
		Sender:      event.Sender{Kind: event.SenderSystem, ID: p.ExternalURL, Name: p.Receiver},
		AlertName:   alertName,
		Title:       cmp.Or(a.Annotations[annotationSummary], alertName),
		Text:        a.Annotations[annotationDescription],
		Service:     labels[labelService],
		Severity:    intake.ParseSeverity(labels[labelSeverity]),
		Labels:      labels,
		Fingerprint: event.Fingerprint(Adapter, alertName, labels, slices.Collect(maps.Keys(labels))),
		StartsAt:    a.StartsAt,
		EndsAt:      a.EndsAt,
		ReceivedAt:  receivedAt,
		Links:       links(a),
	}, nil
}

// key hashes what identifies one notification of one alert.
func key(groupKey string, a alert) string {
	h := sha256.New()

	for _, part := range []string{groupKey, a.Fingerprint, a.Status, a.StartsAt.UTC().Format(time.RFC3339Nano)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil))
}

// links returns the alert's generator URL and runbook, when it has them.
func links(a alert) []event.Link {
	var out []event.Link

	if a.GeneratorURL != "" {
		out = append(out, event.Link{Title: linkSource, URL: a.GeneratorURL})
	}

	if url := a.Annotations[annotationRunbook]; url != "" {
		out = append(out, event.Link{Title: linkRunbook, URL: url})
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
