// Package signoz is the webhook adapter for SigNoz alerts. SigNoz's
// webhook channel sends the payload of Prometheus Alertmanager (version 4),
// so the adapter reads it with the alertmanager adapter and then adds what
// SigNoz keeps in its own labels and annotations: the service, links to the
// alert rule and to the related logs and traces, and a fingerprint that
// stays the same when only the alert's severity or threshold changes.
package signoz

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/alertmanager"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Adapter is the name config selects this adapter with, and the sender
// type in fingerprints.
const Adapter = "signoz"

// The labels and annotations the adapter reads. service.name comes from
// the rule's group-by; service_name is the name of the same label in
// queries that do not allow dots. ruleSource is the URL of the rule.
const (
	labelAlertName      = "alertname"
	labelServiceName    = "service.name"
	labelServiceNameAlt = "service_name"
	labelRuleSource     = "ruleSource"
	annotationRelLogs   = "related_logs"
	annotationRelTraces = "related_traces"
)

// The titles of the links the adapter adds.
const (
	linkRule          = "Rule"
	linkRelatedLogs   = "Related logs"
	linkRelatedTraces = "Related traces"
)

// unstableLabels are labels SigNoz changes while the same alert keeps
// firing, such as when it crosses from the warning to the critical
// threshold. The fingerprint leaves them out.
var unstableLabels = []string{"severity", "threshold.name", "is_recovering", "lastSeen"}

func init() {
	registry.Intakes.Register(Adapter, registry.Typed(New))
}

// Config is the adapter's part of a webhook endpoint's config. The adapter
// has no settings of its own.
type Config struct{}

// SigNoz maps a SigNoz webhook payload to events.
type SigNoz struct{}

var _ intake.Adapter = SigNoz{}

// New builds the adapter.
func New(_ context.Context, _ string, _ Config, _ registry.Deps) (intake.Adapter, error) {
	return SigNoz{}, nil
}

// rawAlert is the part of an event's Raw the adapter reads again: the one
// alert the alertmanager adapter left in it.
type rawAlert struct {
	Alerts []struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"alerts"`
}

// Parse implements intake.Adapter. Keys, kinds, titles, texts, severities,
// times and Raw are those of the alertmanager adapter, so a notification
// that SigNoz repeats maps to the same key and a resolved alert gives a
// resolved event.
func (SigNoz) Parse(ctx context.Context, req intake.Request) ([]event.Event, error) {
	events, err := alertmanager.Alertmanager{}.Parse(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("read the SigNoz payload: %w", err)
	}

	for i := range events {
		if err := enrich(&events[i]); err != nil {
			return nil, fmt.Errorf("alerts[%d]: %w", i, err)
		}
	}

	return events, nil
}

// enrich adds SigNoz's service, links and fingerprint to an event the
// alertmanager adapter made.
func enrich(ev *event.Event) error {
	var raw rawAlert
	if err := json.Unmarshal(ev.Raw, &raw); err != nil {
		return fmt.Errorf("decode the alert's annotations: %w", err)
	}

	var annotations map[string]string
	if len(raw.Alerts) > 0 {
		annotations = raw.Alerts[0].Annotations
	}

	ev.Service = cmp.Or(ev.Labels[labelServiceName], ev.Labels[labelServiceNameAlt], ev.Service)
	ev.Links = addLink(ev.Links, linkRule, ev.Labels[labelRuleSource])
	ev.Links = addLink(ev.Links, linkRelatedLogs, annotations[annotationRelLogs])
	ev.Links = addLink(ev.Links, linkRelatedTraces, annotations[annotationRelTraces])
	ev.Fingerprint = fingerprint(ev.Labels)

	return nil
}

// fingerprint covers the alert name and every label SigNoz keeps the same
// while the alert fires.
func fingerprint(labels map[string]string) string {
	keys := slices.DeleteFunc(slices.Collect(maps.Keys(labels)), func(k string) bool {
		return k == labelAlertName || slices.Contains(unstableLabels, k)
	})

	return event.Fingerprint(Adapter, labels[labelAlertName], labels, keys)
}

// addLink appends a link to url, unless url is empty or already linked.
func addLink(links []event.Link, title, url string) []event.Link {
	if url == "" || slices.ContainsFunc(links, func(l event.Link) bool { return l.URL == url }) {
		return links
	}

	return append(links, event.Link{Title: title, URL: url})
}
