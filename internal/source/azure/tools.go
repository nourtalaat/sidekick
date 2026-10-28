package azure

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
// source prefix, for example azure__query_logs.
const (
	toolQueryLogs   = "query_logs"
	toolListChanges = "list_changes"
)

// Limits on how many items a tool returns.
const (
	// defaultRows and maxRows bound the rows of query_logs, over all its
	// tables.
	defaultRows = 100
	maxRows     = 500
	// defaultEvents and maxEvents bound the events of list_changes.
	defaultEvents = 100
	maxEvents     = 500
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolQueryLogs,
		Description: "Run a KQL query on a Log Analytics workspace over a time range, for example " +
			"AppRequests | where Success == false | summarize count() by bin(TimeGenerated, 5m), Name. " +
			"Returns each table's columns and rows, at most 500 rows, and a link to the workspace's logs.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"workspace": stringProperty("The workspace name or ID. It may be left out when Sidekick reads only one workspace."),
			"query":     stringProperty("The KQL query, passed to Log Analytics as given. Use take or summarize to keep the result small."),
			"since":     timeProperty("The start of the range. Default: one hour before until."),
			"until":     timeProperty("The end of the range. Default: now."),
			"limit":     limitProperty(defaultRows, maxRows),
		}, "query"),
		ReadOnly: true,
	},
	{
		Name: toolListChanges,
		Description: "List what changed in a subscription in a time range, newest first, from its Activity Log: " +
			"deployments, writes, deletes and actions on resources, with who made them and whether they failed.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"subscription":  subscriptionProperty(),
			"resourceGroup": stringProperty("Only changes in this resource group."),
			"resource":      resourceProperty("Only changes to this resource."),
			"since":         timeProperty("Only changes at or after this time. Default: 24 hours before until."),
			"until":         timeProperty("Only changes before this time. Default: now."),
			"limit":         limitProperty(defaultEvents, maxEvents),
		}),
		ReadOnly: true,
	},
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
		panic(fmt.Sprintf("azure: encode a tool schema: %v", err))
	}

	return data
}

// subscriptionProperty returns the subscription property.
func subscriptionProperty() *jsonschema.Schema {
	return stringProperty("The subscription ID. It may be left out when Sidekick reads only one subscription.")
}

// resourceProperty returns a property that holds a resource ID.
func resourceProperty(description string) *jsonschema.Schema {
	return stringProperty(description + " Its full resource ID, for example " +
		"/subscriptions/<id>/resourceGroups/shop/providers/Microsoft.Web/sites/checkout.")
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
	minimum, maximum := 1.0, float64(most)

	return &jsonschema.Schema{
		Type:        "integer",
		Description: fmt.Sprintf("The most items to return, 1 to %d. Default %d.", most, def),
		Minimum:     &minimum,
		Maximum:     &maximum,
	}
}
