package flags

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as a flag source names them. The model sees them with the
// source prefix, for example launchdarkly__list_flag_changes.
const (
	ToolListChanges = "list_flag_changes"
	ToolGetFlag     = "get_flag"
)

// Limits on how many changes list_flag_changes returns.
const (
	DefaultChanges = 20
	MaxChanges     = 100
)

// Tools returns the tools of a flag source for service, the name of the
// flag service such as LaunchDarkly. Every tool only reads.
func Tools(service string) []source.Tool {
	return []source.Tool{
		{
			Name: ToolListChanges,
			Description: "List the changes to feature flags in one " + service + " project and environment in a time range, " +
				"newest first, from " + service + "'s audit log: when, which flag, what was done, who did it, the summary of " +
				"the change, the comment left with it, and a link. Use it to check whether someone turned a flag on or off, " +
				"changed its targeting or rolled it out around the time a problem started. Give flag to see one flag only.",
			InputSchema: schema(map[string]*jsonschema.Schema{
				"flag": stringProperty("Only changes to the flag with this key. Leave it out for every flag."),
				"since": timeProperty(fmt.Sprintf("Only changes at or after this time. The range may be at most %d days long.",
					int(MaxRange.Hours()/24))),
				"until": timeProperty("Only changes before this time. Default: now."),
				"limit": {
					Type:        "integer",
					Description: fmt.Sprintf("The most changes to return, 1 to %d. Default %d.", MaxChanges, DefaultChanges),
					Minimum:     ptr(1),
					Maximum:     ptr(MaxChanges),
				},
			}, "since"),
			ReadOnly: true,
		},
		{
			Name: ToolGetFlag,
			Description: "Get one feature flag's current state in one " + service + " environment: whether it is on, how " +
				"many targeting rules and individual targets it has, what it serves when on and no rule matches, what it " +
				"serves when off, and when it last changed.",
			InputSchema: schema(map[string]*jsonschema.Schema{
				"flag": stringProperty("The flag key, for example new-checkout."),
			}, "flag"),
			ReadOnly: true,
		},
	}
}

// schema returns the JSON Schema of a tool's input: the project and
// environment properties, the tool's own properties and the required ones.
// It panics when the schema does not encode, which is a programming error
// found by any test.
func schema(props map[string]*jsonschema.Schema, required ...string) json.RawMessage {
	all := map[string]*jsonschema.Schema{
		"project": stringProperty("The project key. It may be left out when Sidekick reads only one project."),
		"environment": stringProperty("The environment key, for example production. It may be left out when Sidekick " +
			"reads only one environment of the project."),
	}
	maps.Copy(all, props)

	s := &jsonschema.Schema{
		Type:                 "object",
		Properties:           all,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		PropertyOrder:        slices.Sorted(maps.Keys(all)),
	}

	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("flags: encode a tool schema: %v", err))
	}

	return data
}

// stringProperty returns a string property.
func stringProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

// timeProperty returns a property that holds an RFC 3339 time.
func timeProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Format:      "date-time",
		Description: description + " In RFC 3339 form, for example 2026-05-04T10:00:00Z.",
	}
}

// ptr returns a pointer to n as a float64, for schema bounds.
func ptr(n int) *float64 {
	f := float64(n)

	return &f
}
