package intake

import (
	"strings"

	"github.com/nourtalaat/sidekick/internal/event"
)

// severities maps the severity names senders commonly use, in lower case,
// to Sidekick's four levels.
var severities = map[string]event.Severity{
	"critical":      event.SeverityCritical,
	"crit":          event.SeverityCritical,
	"fatal":         event.SeverityCritical,
	"emergency":     event.SeverityCritical,
	"emerg":         event.SeverityCritical,
	"alert":         event.SeverityCritical,
	"page":          event.SeverityCritical,
	"high":          event.SeverityCritical,
	"error":         event.SeverityCritical,
	"err":           event.SeverityCritical,
	"warning":       event.SeverityWarning,
	"warn":          event.SeverityWarning,
	"medium":        event.SeverityWarning,
	"info":          event.SeverityInfo,
	"informational": event.SeverityInfo,
	"notice":        event.SeverityInfo,
	"low":           event.SeverityInfo,
	"none":          event.SeverityInfo,
}

// ParseSeverity normalizes a sender's severity name, ignoring case and
// spaces at either end. Names it does not know, and the empty string, give
// event.SeverityUnknown.
func ParseSeverity(s string) event.Severity {
	if sev, ok := severities[strings.ToLower(strings.TrimSpace(s))]; ok {
		return sev
	}

	return event.SeverityUnknown
}
