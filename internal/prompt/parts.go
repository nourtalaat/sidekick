package prompt

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// renderStrategy writes the strategy part. Preferred sources the model has
// no tools for are left out.
func renderStrategy(st strategy.Strategy, tools []model.ToolSpec) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Strategy: %s\n\n%s\n", st.Name, st.Description)

	writeList(&b, "Check first", st.First, true)
	writeList(&b, "Common causes", st.CommonCauses, false)
	writeList(&b, "Signs of a false positive", st.FalsePositiveSigns, false)
	writeList(&b, "Verdict hints", verdictHints(st.VerdictHints), false)

	if prefer := availableSources(st.Sources.Prefer, tools); len(prefer) > 0 {
		fmt.Fprintf(&b, "\n## Sources to try first\n\n%s\n", strings.Join(prefer, ", "))
	}

	return strings.TrimSpace(b.String())
}

// renderEvent writes the event group part. What the sender wrote is
// wrapped as untrusted data; what Sidekick knows itself is not.
func renderEvent(g Group, r RouteContext) string {
	e := g.First

	var b strings.Builder

	b.WriteString("# Event group\n\n")

	if count := max(g.Count, 1); count == 1 {
		b.WriteString("The group has 1 event, shown below.\n")
	} else {
		fmt.Fprintf(&b, "The group has %d events. The first one is shown below.\n", count)
	}

	fmt.Fprintf(&b, "\nRoute: %s\n", r.Name)

	for _, key := range slices.Sorted(maps.Keys(r.Details)) {
		fmt.Fprintf(&b, "%s: %s\n", key, r.Details[key])
	}

	fmt.Fprintf(&b, "\n## First event\n\nIntake: %s\nKind: %s\n", e.Intake, e.Kind)
	writeTime(&b, "Received", e.ReceivedAt)
	b.WriteString("\n")
	b.WriteString(WrapUntrusted(e.Intake, "", "", senderText(e)))

	return b.String()
}

// senderText writes the parts of an event that came from its sender.
func senderText(e event.Event) string {
	var b strings.Builder

	writeField(&b, "Alert name", e.AlertName)
	writeField(&b, "Title", e.Title)
	writeField(&b, "Service", e.Service)
	writeField(&b, "Severity", string(e.Severity))

	if e.Sender.Name != "" || e.Sender.ID != "" {
		fmt.Fprintf(&b, "Sender: %s (%s)\n", cmp.Or(e.Sender.Name, e.Sender.ID), e.Sender.Kind)
	}

	writeTime(&b, "Started", e.StartsAt)
	writeTime(&b, "Ended", e.EndsAt)

	if len(e.Labels) > 0 {
		b.WriteString("Labels:\n")

		for _, key := range slices.Sorted(maps.Keys(e.Labels)) {
			fmt.Fprintf(&b, "  %s: %s\n", key, e.Labels[key])
		}
	}

	if len(e.Links) > 0 {
		b.WriteString("Links:\n")

		for _, l := range e.Links {
			fmt.Fprintf(&b, "  - %s: %s\n", cmp.Or(l.Title, "link"), l.URL)
		}
	}

	if e.Text != "" {
		fmt.Fprintf(&b, "Text:\n%s\n", e.Text)
	}

	return b.String()
}

// verdictHints lists the hints that are set, each named by the verdict
// value submit_verdict takes.
func verdictHints(h strategy.VerdictHints) []string {
	var out []string

	for _, hint := range []struct {
		verdict report.Verdict
		text    string
	}{
		{report.TruePositive, h.TruePositive},
		{report.FalsePositive, h.FalsePositive},
		{report.Inconclusive, h.Inconclusive},
		{report.Duplicate, h.Duplicate},
		{report.KnownIssue, h.KnownIssue},
		{report.Flapping, h.Flapping},
	} {
		if hint.text != "" {
			out = append(out, fmt.Sprintf("%s: %s", hint.verdict, hint.text))
		}
	}

	return out
}

// availableSources returns the names in prefer that have at least one tool.
func availableSources(prefer []string, tools []model.ToolSpec) []string {
	var out []string

	for _, name := range prefer {
		if slices.ContainsFunc(tools, func(t model.ToolSpec) bool {
			return strings.HasPrefix(t.Name, name+source.Separator)
		}) {
			out = append(out, name)
		}
	}

	return out
}

// writeList writes a section with one item per line, numbered or as
// bullets, or nothing when items is empty.
func writeList(b *strings.Builder, title string, items []string, numbered bool) {
	if len(items) == 0 {
		return
	}

	fmt.Fprintf(b, "\n## %s\n\n", title)

	for i, item := range items {
		if numbered {
			fmt.Fprintf(b, "%d. %s\n", i+1, item)
		} else {
			fmt.Fprintf(b, "- %s\n", item)
		}
	}
}

// writeField writes "name: value", or nothing when value is empty.
func writeField(b *strings.Builder, name, value string) {
	if value != "" {
		fmt.Fprintf(b, "%s: %s\n", name, value)
	}
}

// writeTime writes "name: time" in UTC, or nothing for the zero time.
func writeTime(b *strings.Builder, name string, t time.Time) {
	if !t.IsZero() {
		fmt.Fprintf(b, "%s: %s\n", name, t.UTC().Format(time.RFC3339))
	}
}
