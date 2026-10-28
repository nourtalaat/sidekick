package prompt

import (
	"cmp"
	_ "embed" // Embeds system.tmpl.
	"slices"
	"strings"
	"text/template"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// Version is the version of the prompt: the system prompt in system.tmpl
// and the layout of the other parts. Each investigation records it, so
// results always refer to an exact prompt. Change it whenever the prompt
// changes; a test fails until system.tmpl and this version agree.
const Version = "4"

//go:embed system.tmpl
var systemTemplate string

// systemPrompt is the rendered system prompt. It is the same for every
// investigation, so providers can cache it.
var systemPrompt = renderSystem()

// Input is what one investigation's first request is built from.
type Input struct {
	// Strategy is the strategy the route names, at the version loaded.
	Strategy strategy.Strategy
	// Tools are the tools the model may call: built-in tools, whose names
	// never hold source.Separator, and source tools named
	// <source>__<tool>. Build puts the built-in tools first and the source
	// tools after them, each sorted by name, so the order is the same on
	// every run and the cached prefix stays the same.
	Tools []model.ToolSpec
	// Group is the event group under investigation.
	Group Group
	// Route is the route that matched the group.
	Route RouteContext
	// Memory are similar confirmed investigations, shown in the order
	// given. Empty leaves the memory part out.
	Memory []Example
}

// Group is an event group as the prompt shows it.
type Group struct {
	// First is the event that opened the group.
	First event.Event
	// Count is the number of events in the group. Values below 1 count as
	// 1. Events that join later reach the model through Inbox.
	Count int
}

// RouteContext is what the prompt says about the matched route.
type RouteContext struct {
	// Name is the route's name.
	Name string
	// Details are facts about the route from config, such as the service
	// owner or the environment. They are shown sorted by key and are not
	// marked untrusted, because they come from config, not from the event.
	Details map[string]string
}

// parts are the pieces of the first request, in the fixed order of the
// prompt: the system prompt, the tools, the strategy, lessons, memory, then
// the event group. Stable pieces come first so providers can cache them:
// there is a cache breakpoint after the tools and one after the strategy.
type parts struct {
	system   string
	tools    []model.ToolSpec
	strategy string
	// lessons and memory are reviewed guidance and short summaries of
	// similar confirmed investigations. Lessons are always empty until
	// they arrive in M3. Both change from one investigation to the next,
	// so they sit after the last cache breakpoint.
	lessons []string
	memory  []string
	event   string
}

// Build assembles the first request of an investigation: the system prompt
// and the strategy as system text, the tools, and one user message with the
// similar past investigations, when there are any, then the event group.
// Cache breakpoints mark the last tool and the strategy. The
// caller adds the model settings (tool choice, token limits, thinking).
func Build(in Input) model.Request {
	p := parts{
		system:   systemPrompt,
		tools:    orderTools(in.Tools),
		strategy: renderStrategy(in.Strategy, in.Tools),
		memory:   renderMemory(in.Memory),
		event:    renderEvent(in.Group, in.Route),
	}

	return p.request()
}

// request turns the parts into a model request.
func (p parts) request() model.Request {
	var user []model.Part
	for _, text := range slices.Concat(p.lessons, p.memory, []string{p.event}) {
		user = append(user, model.Text{Text: text})
	}

	return model.Request{
		System: []model.Text{
			{Text: p.system},
			{Text: p.strategy, Cache: true},
		},
		Tools:    p.tools,
		Messages: []model.Message{{Role: model.User, Parts: user}},
	}
}

// orderTools returns a copy of tools with the built-in tools first and the
// source tools after them, each sorted by name, and the cache breakpoint on
// the last tool only.
func orderTools(tools []model.ToolSpec) []model.ToolSpec {
	out := slices.Clone(tools)
	slices.SortStableFunc(out, func(a, b model.ToolSpec) int {
		if c := cmp.Compare(toolGroup(a.Name), toolGroup(b.Name)); c != 0 {
			return c
		}

		return cmp.Compare(a.Name, b.Name)
	})

	for i := range out {
		out[i].Cache = i == len(out)-1
	}

	return out
}

// renderSystem renders system.tmpl.
func renderSystem() string {
	return renderTemplate("system.tmpl", systemTemplate, map[string]string{
		"Tag":           untrustedTag,
		"SubmitVerdict": report.SubmitVerdictTool,
	})
}

// Tool groups, in the order the prompt lists them.
const (
	builtInTools = iota
	sourceTools
)

// toolGroup says whether name is a built-in or a source tool. Only source
// tool names hold source.Separator.
func toolGroup(name string) int {
	if strings.Contains(name, source.Separator) {
		return sourceTools
	}

	return builtInTools
}

// renderTemplate renders one of the templates of this package with data.
// The templates are part of the binary, so a failure is a bug, caught by
// the tests.
func renderTemplate(name, text string, data map[string]string) string {
	tmpl := template.Must(template.New(name).Option("missingkey=error").Parse(text))

	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		panic("render " + name + ": " + err.Error())
	}

	return strings.TrimSpace(b.String())
}
