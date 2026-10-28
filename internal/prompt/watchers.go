package prompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/output"
)

// watcherSource names the untrusted data of a watcher's last check.
const watcherSource = "watcher"

// stopReasons say why a watcher stopped, by its stop reason.
var stopReasons = map[string]string{
	"group_resolved": "every alert of the group recovered",
	"responder":      "a responder stopped it",
	"config_changed": "the config no longer allows its check",
}

// WatcherNote is what the inbox says about one watcher: its state after a
// change, or why it did not start.
type WatcherNote struct {
	State output.WatcherState
	// NotStarted says why a route or strategy watcher did not start;
	// State then holds only its name and check type.
	NotStarted string
}

// Watchers writes the part of the inbox message about watchers: each
// change of a watcher's state since the model's last turn and each
// watcher that did not start, in the order they happened. It tells the
// model to weigh what watchers report as evidence: a watcher never
// decides the verdict. The result of a watcher's last check comes from
// the checked service, so it is wrapped as untrusted data.
func Watchers(notes []WatcherNote) string {
	var b strings.Builder

	b.WriteString("# Watchers since your last turn\n\n")
	b.WriteString("Watchers check on their own schedule whether the service is back up. Weigh what they report as " +
		"evidence, together with the rest; a watcher never decides the verdict, and the investigation goes on until " +
		"you submit it.\n")

	for _, n := range notes {
		b.WriteString("\n- " + watcherLine(n) + "\n")

		if n.State.LastResult != "" && n.NotStarted == "" {
			b.WriteString("  Last check:\n")
			b.WriteString(WrapUntrusted(watcherSource, "", "", n.State.LastResult))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// watcherLine is one line about a watcher, such as "checkout-health
// (http check) recovered at 2026-10-26T10:42:00Z: 2 passing checks in a
// row, 9 checks in all."
func watcherLine(n WatcherNote) string {
	s := n.State
	name := fmt.Sprintf("%s (%s check)", s.Name, s.CheckType)

	if n.NotStarted != "" {
		return name + " did not start: " + n.NotStarted + "."
	}

	switch s.Status {
	case output.WatcherRunning:
		return fmt.Sprintf("%s started at %s. It checks every %s and recovers after %s in a row.",
			name, utc(s.StartedAt), time.Duration(s.IntervalSeconds)*time.Second, plural(s.PassesToRecover, "passing check", "passing checks"))
	case output.WatcherRecovered:
		return fmt.Sprintf("%s recovered at %s: %s in a row, %s in all.",
			name, utc(s.EndedAt), plural(s.PassesInARow, "passing check", "passing checks"), plural(s.Checks, "check", "checks"))
	case output.WatcherTimedOut:
		return fmt.Sprintf("%s timed out at %s after %s without recovering.", name, utc(s.EndedAt), plural(s.Checks, "check", "checks"))
	case output.WatcherStopped:
		reason := stopReasons[s.StopReason]
		if reason == "" {
			reason = "it was stopped"
		}

		return fmt.Sprintf("%s stopped at %s after %s: %s.", name, utc(s.EndedAt), plural(s.Checks, "check", "checks"), reason)
	default:
		return fmt.Sprintf("%s is %s.", name, s.Status)
	}
}

// utc writes t in UTC as RFC 3339.
func utc(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
