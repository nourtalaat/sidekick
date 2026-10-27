package gcp

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as the source names them. The model sees them with the
// source prefix, for example gcp__query_logs.
const (
	toolQueryLogs   = "query_logs"
	toolListChanges = "list_changes"
)

// Limits on how many log entries query_logs and list_changes return.
const (
	defaultEntries = 100
	maxEntries     = 500
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolQueryLogs,
		Description: "Search the Cloud Logging entries of a Google Cloud project in a time range, newest first, with a " +
			"Logging query language filter such as resource.type=\"k8s_container\" AND severity>=ERROR. Returns each " +
			"entry's time, severity, log, resource and message, cut when long, and a Logs Explorer link.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"filter": stringProperty("The Logging query language filter, passed to Cloud Logging as given. The time range is " +
				"added to it. Empty matches every entry."),
			"since": timeProperty("Only entries at or after this time. Default: one hour before until."),
			"until": timeProperty("Only entries before this time. Default: now."),
			"limit": limitProperty(defaultEntries, maxEntries),
		}),
		ReadOnly: true,
	},
	{
		Name: toolListChanges,
		Description: "List what changed in a Google Cloud project in a time range, newest first, from its Admin Activity " +
			"and System Event audit logs: deploys, config changes, GKE upgrades and the like. Returns each change's time, " +
			"service, method, resource, who made it and whether it failed.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"service":  stringProperty("Only changes through this API, for example run.googleapis.com or container.googleapis.com."),
			"resource": stringProperty("Only changes to resources whose name contains this text, for example services/checkout."),
			"since":    timeProperty("Only changes at or after this time. Default: 24 hours before until."),
			"until":    timeProperty("Only changes before this time. Default: now."),
			"limit":    limitProperty(defaultEntries, maxEntries),
		}),
		ReadOnly: true,
	},
}

// schema returns the JSON Schema of a tool's input: the project property,
// the tool's own properties and the required ones. It panics when the
// schema does not encode, which is a programming error found by any test.
func schema(props map[string]*jsonschema.Schema, required ...string) json.RawMessage {
	all := map[string]*jsonschema.Schema{
		"project": stringProperty("The project ID, for example shop-prod. It may be left out when Sidekick reads only one project."),
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
		panic(fmt.Sprintf("gcp: encode a tool schema: %v", err))
	}

	return data
}

// stringProperty returns a string property.
func stringProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

// timeProperty returns a property that holds an RFC 3339 time.
func timeProperty(description string) *jsonschema.Schema {
	first, rest, _ := strings.Cut(description, ". ")

	return &jsonschema.Schema{
		Type:        "string",
		Format:      "date-time",
		Description: first + ", in RFC 3339 form, for example 2026-05-04T10:00:00Z. " + rest,
	}
}

// limitProperty returns the limit property of a tool.
func limitProperty(def, most int) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "integer",
		Description: fmt.Sprintf("The most items to return, 1 to %d. Default %d.", most, def),
		Minimum:     ptr(1),
		Maximum:     ptr(most),
	}
}

// ptr returns a pointer to n as a float64, for schema bounds.
func ptr(n int) *float64 {
	f := float64(n)

	return &f
}
