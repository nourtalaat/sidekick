package prompt

import (
	"cmp"
	_ "embed" // Embeds triage.tmpl.
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// SubmitTriageTool is the name of the tool that ends triage.
const SubmitTriageTool = "submit_triage"

//go:embed triage.tmpl
var triageTemplate string

// triageSystemPrompt is the rendered triage system prompt. It is the same
// for every triage, so providers can cache it.
var triageSystemPrompt = renderTemplate("triage.tmpl", triageTemplate, map[string]string{
	"Tag":          untrustedTag,
	"SubmitTriage": SubmitTriageTool,
	"Duplicate":    string(report.Duplicate),
	"KnownIssue":   string(report.KnownIssue),
	"Flapping":     string(report.Flapping),
})

// TriageInput is what the request of one triage is built from.
type TriageInput struct {
	// Strategies are the strategies triage chooses from.
	Strategies []strategy.Strategy
	// RouteStrategy is the name of the route's own strategy, which a full
	// investigation follows unless triage chooses another.
	RouteStrategy string
	// Tool is the submit_triage tool.
	Tool model.ToolSpec
	// Group is the event group to triage.
	Group Group
	// Route is the route that matched the group.
	Route RouteContext
}

// BuildTriage assembles the request of a triage: the triage system prompt
// and the list of strategies as system text, the submit_triage tool, and
// one user message with the event group and the strategies whose match
// fits its first event. The list of strategies and the tool are the same
// from one triage to the next, so the cache breakpoints mark them. The
// caller adds the model settings, such as requiring the tool call.
func BuildTriage(in TriageInput) model.Request {
	tool := in.Tool
	tool.Cache = true

	return model.Request{
		System: []model.Text{
			{Text: triageSystemPrompt},
			{Text: renderStrategyList(in.Strategies), Cache: true},
		},
		Tools: []model.ToolSpec{tool},
		Messages: []model.Message{{Role: model.User, Parts: []model.Part{
			model.Text{Text: renderEvent(in.Group, in.Route)},
			model.Text{Text: renderStrategyChoice(in.Strategies, in.RouteStrategy, in.Group.First)},
		}}},
	}
}

// renderStrategyList writes the strategies, sorted by name, each with its
// description.
func renderStrategyList(strategies []strategy.Strategy) string {
	sorted := slices.SortedFunc(slices.Values(strategies), func(a, b strategy.Strategy) int {
		return cmp.Compare(a.Name, b.Name)
	})

	var b strings.Builder

	b.WriteString("# Strategies\n\nA strategy tells the full investigation what to check first for one kind of event.\n\n")

	for _, st := range sorted {
		fmt.Fprintf(&b, "- %s: %s\n", st.Name, st.Description)
	}

	return strings.TrimSpace(b.String())
}

// renderStrategyChoice writes what Sidekick knows about the choice of
// strategy for the event group: the route's strategy, and the strategies
// whose match fits the first event.
func renderStrategyChoice(strategies []strategy.Strategy, routeStrategy string, first event.Event) string {
	var fits []string

	text := strings.Join(slices.DeleteFunc([]string{first.Title, first.Text}, func(s string) bool { return s == "" }), "\n")

	for _, st := range strategies {
		if st.Match.Fits(first.AlertName, text) {
			fits = append(fits, st.Name)
		}
	}

	slices.Sort(fits)

	var b strings.Builder

	fmt.Fprintf(&b, "# Choosing a strategy\n\nThe route's strategy is %s.\n", routeStrategy)

	if len(fits) == 0 {
		b.WriteString("No strategy's match fits the alert name or the text of the first event.\n")
	} else {
		fmt.Fprintf(&b, "These strategies match the alert name or the text of the first event: %s.\n", strings.Join(fits, ", "))
	}

	return b.String()
}
