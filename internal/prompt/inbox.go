package prompt

import (
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/event"
)

// maxInboxEvents is how many events one inbox message shows in full. The
// rest are only counted, so a burst of events cannot fill the model's
// context.
const maxInboxEvents = 20

// Inbox writes the message that tells the model what arrived since its last
// turn: events that joined the group, and recoveries, which are events of
// kind event.KindResolved. events are in the order they arrived, and at most
// maxInboxEvents of them are shown. What the sender wrote is wrapped as
// untrusted data; what Sidekick knows itself is not.
func Inbox(events []event.Event) string {
	var b strings.Builder

	b.WriteString("# New since your last turn\n\n")

	joined, recovered := 0, 0

	for _, e := range events {
		if e.Kind == event.KindResolved {
			recovered++
		} else {
			joined++
		}
	}

	if joined > 0 {
		fmt.Fprintf(&b, "%s joined the group.\n", plural(joined, "new event", "new events"))
	}

	if recovered > 0 {
		fmt.Fprintf(&b, "%s arrived. A recovery means the sender reports that the alert has cleared. "+
			"Weigh it as evidence; the investigation goes on until you submit your verdict.\n",
			plural(recovered, "recovery", "recoveries"))
	}

	shown := events[:min(len(events), maxInboxEvents)]

	for _, e := range shown {
		heading := "New event"
		if e.Kind == event.KindResolved {
			heading = "Recovery"
		}

		fmt.Fprintf(&b, "\n## %s\n\nIntake: %s\nKind: %s\n", heading, e.Intake, e.Kind)
		writeTime(&b, "Received", e.ReceivedAt)
		b.WriteString("\n")
		b.WriteString(WrapUntrusted(e.Intake, "", "", senderText(e)))
		b.WriteString("\n")
	}

	if hidden := len(events) - len(shown); hidden > 0 {
		fmt.Fprintf(&b, "\n%s not shown.\n", plural(hidden, "more event is", "more events are"))
	}

	return b.String()
}

// plural writes n with the singular or plural noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return fmt.Sprintf("%d %s", n, many)
}
