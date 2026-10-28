package memory

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as the source names them. The model sees them with the
// source prefix, for example memory__search.
const (
	toolSearch       = "search"
	toolServiceNotes = "service_notes"
)

// maxQueryLen is the longest search query a call may give.
const maxQueryLen = 500

// verdicts are the verdicts search can filter on.
var verdicts = []any{
	string(report.TruePositive), string(report.FalsePositive), string(report.Inconclusive),
	string(report.Duplicate), string(report.KnownIssue), string(report.Flapping),
}

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolSearch,
		Description: "Search past investigations whose verdict a person confirmed, by words in their alert name, summary " +
			"and cause. Returns up to 10, best match first, each with its confirmed verdict and cause. Use it to see " +
			"whether an alert like this one happened before and what the cause was; check the current systems before " +
			"you rely on an answer.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"query": {
				Type: "string", MinLength: new(1), MaxLength: new(maxQueryLen),
				Description: `Words to search for, such as "checkout latency deploy". Words in quotes match as a ` +
					`phrase, "or" between words matches either, and a word with a leading "-" must not appear.`,
			},
			"service": {Type: "string", Description: "Only investigations of this service, as alerts name it."},
			"verdict": {Type: "string", Enum: verdicts, Description: "Only investigations with this confirmed verdict."},
		}, "query"),
		ReadOnly: true,
	},
	{
		Name:        toolServiceNotes,
		Description: "Read the notes people wrote about a service, such as known problems, owners to ask, or alerts to treat with care.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"service": {Type: "string", MinLength: new(1), Description: "The service name, as alerts name it."},
		}, "service"),
		ReadOnly: true,
	},
}

// searchInput is the input of search.
type searchInput struct {
	Query   string `json:"query"`
	Service string `json:"service"`
	Verdict string `json:"verdict"`
}

// serviceNotesInput is the input of service_notes.
type serviceNotesInput struct {
	Service string `json:"service"`
}

// schema returns the JSON Schema of a tool's input. It panics when the
// schema does not encode, which is a programming error found by any test.
func schema(props map[string]*jsonschema.Schema, required ...string) json.RawMessage {
	s := &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		PropertyOrder:        slices.Sorted(maps.Keys(props)),
	}

	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("memory: encode a tool schema: %v", err))
	}

	return data
}
