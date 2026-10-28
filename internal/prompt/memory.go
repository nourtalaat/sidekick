package prompt

import (
	"fmt"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// memorySource is the source name the memory part's untrusted blocks carry.
const memorySource = "memory"

// maxExampleText is the most bytes of an example's summary or cause the
// prompt shows.
const maxExampleText = 1000

// memoryIntro tells the model what the examples are and how to use them.
const memoryIntro = "People confirmed the verdicts of these past investigations of similar alerts. " +
	"Use them as hints about where to look and what went wrong before, not as evidence about this alert: " +
	"check the systems with tools before you rely on them."

// MemoryMatch says why a past investigation was chosen as an example.
type MemoryMatch string

// Memory matches, in the order examples are chosen.
const (
	// MatchFingerprint: the alert had the same fingerprint.
	MatchFingerprint MemoryMatch = "fingerprint"
	// MatchServiceAlert: the alert had the same service and alert name.
	MatchServiceAlert MemoryMatch = "service_alert"
	// MatchText: the alert's words were like this one's.
	MatchText MemoryMatch = "text"
)

// matchText is how the prompt describes each match.
var matchText = map[MemoryMatch]string{
	MatchFingerprint:  "the same alert fingerprint",
	MatchServiceAlert: "the same service and alert name",
	MatchText:         "similar words",
}

// Example is a confirmed past investigation the prompt shows as an
// example, from memory.
type Example struct {
	InvestigationID string
	Match           MemoryMatch
	// Verdict is the confirmed verdict.
	Verdict   string
	Service   string
	AlertName string
	Summary   string
	// Cause is the confirmed cause.
	Cause       string
	ConfirmedAt time.Time
}

// renderMemory writes the memory part, or nothing when there are no
// examples. What Sidekick knows itself, such as the verdict and why the
// example matched, is shown as is; the text, which came from an alert and
// the model, is wrapped as untrusted data.
func renderMemory(examples []Example) []string {
	if len(examples) == 0 {
		return nil
	}

	var b strings.Builder

	fmt.Fprintf(&b, "# Similar past investigations\n\n%s\n", memoryIntro)

	for i, ex := range examples {
		// TODO: [SK-35] link each example to its investigation page
		fmt.Fprintf(&b, "\n## Example %d: %s\n\nInvestigation: %s\n", i+1, ex.Verdict, ex.InvestigationID)
		writeField(&b, "Matched by", matchText[ex.Match])
		writeTime(&b, "Confirmed", ex.ConfirmedAt)
		b.WriteString("\n")
		b.WriteString(WrapUntrusted(memorySource, "", "", exampleText(ex)))
		b.WriteString("\n")
	}

	return []string{b.String()}
}

// exampleText writes the parts of an example that came from an alert or
// from the model.
func exampleText(ex Example) string {
	var b strings.Builder

	writeField(&b, "Service", ex.Service)
	writeField(&b, "Alert name", ex.AlertName)
	writeField(&b, "Summary", source.CutText(ex.Summary, maxExampleText))
	writeField(&b, "Cause", source.CutText(ex.Cause, maxExampleText))

	return b.String()
}
