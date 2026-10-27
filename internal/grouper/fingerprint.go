package grouper

import (
	"github.com/nourtalaat/sidekick/internal/event"
)

// fingerprint returns the fingerprint ev is grouped by under a route with
// the fingerprint labels keys. An alert that has at least one of the labels
// gets a fingerprint made from its intake, its alert name and the values of
// those labels, a missing one counted as empty. Any other event keeps the
// fingerprint the intake stored, because a fingerprint made only from the
// intake and the alert name would put unrelated events in one group.
func fingerprint(ev event.Event, keys []string) string {
	if ev.Kind != event.KindAlert || !hasAny(ev.Labels, keys) {
		return ev.Fingerprint
	}

	return event.Fingerprint(ev.Intake, ev.AlertName, ev.Labels, keys)
}

// hasAny reports whether labels holds at least one of keys.
func hasAny(labels map[string]string, keys []string) bool {
	for _, k := range keys {
		if _, ok := labels[k]; ok {
			return true
		}
	}

	return false
}
