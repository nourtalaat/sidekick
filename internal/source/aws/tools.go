package aws

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
// source prefix, for example aws__query_logs.
const (
	toolQueryLogs   = "query_logs"
	toolListChanges = "list_changes"
)

// Limits on how many items a tool returns or takes.
const (
	// defaultRows and maxRows bound the rows of query_logs.
	defaultRows = 100
	maxRows     = 500
	// maxInputLogGroups bounds the log groups and prefixes one query_logs
	// call names.
	maxInputLogGroups = 20
	// defaultEvents and maxEvents bound the events of list_changes.
	defaultEvents = 50
	maxEvents     = 200
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolQueryLogs,
		Description: "Run a CloudWatch Logs Insights query on log groups over a time range, for example " +
			"fields @timestamp, @message | filter @message like /ERROR/ | sort @timestamp desc | limit 50. " +
			"Returns the fields and rows, at most 500 rows, and a link to the query in the CloudWatch console.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"account": accountProperty(),
			"region":  regionProperty(),
			"logGroups": {Type: "array", Items: &jsonschema.Schema{Type: "string"}, MinItems: ptr(1), MaxItems: ptr(maxInputLogGroups),
				Description: "Log group names, or the start of names followed by *, for example [\"/ecs/checkout\", \"/aws/eks/prod/*\"]. " +
					"One query reads at most 50 log groups."},
			"query": stringProperty("The Logs Insights query, passed to CloudWatch as given. Use limit or stats to keep the result small."),
			"since": timeProperty("The start of the range."),
			"until": timeProperty("The end of the range."),
			"limit": limitProperty(defaultRows, maxRows),
		}, "logGroups", "query", "since", "until"),
		ReadOnly: true,
	},
	{
		Name: toolListChanges,
		Description: "List the write events of CloudTrail event history in a time range, newest first: which API " +
			"was called on which resources, by whom, and whether it failed, with a console link to each event. Read-only calls are left out.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"account": accountProperty(),
			"region":  regionProperty(),
			"resource": stringProperty("Only events about this resource, by name or ARN, for example checkout or " +
				"arn:aws:ecs:us-east-1:111122223333:service/shop/checkout."),
			"eventSource": stringProperty("Only events of this AWS service, for example ecs.amazonaws.com."),
			"since":       timeProperty("Only events at or after this time. Default: 24 hours before until."),
			"until":       timeProperty("Only events before this time. Default: now."),
			"limit":       limitProperty(defaultEvents, maxEvents),
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
		panic(fmt.Sprintf("aws: encode a tool schema: %v", err))
	}

	return data
}

// accountProperty returns the account property.
func accountProperty() *jsonschema.Schema {
	return stringProperty("The 12-digit AWS account ID. It may be left out when Sidekick reads only one account.")
}

// regionProperty returns the region property.
func regionProperty() *jsonschema.Schema {
	return stringProperty("The AWS Region, for example us-east-1. It may be left out when Sidekick reads only one Region.")
}

// stringProperty returns a string property.
func stringProperty(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

// timeProperty returns a property that holds an RFC 3339 time.
func timeProperty(description string) *jsonschema.Schema {
	first, rest, _ := strings.Cut(strings.TrimSuffix(description, "."), ". ")
	text := first + ", in RFC 3339 form, for example 2026-05-04T10:00:00Z."

	if rest != "" {
		text += " " + rest + "."
	}

	return &jsonschema.Schema{Type: "string", Format: "date-time", Description: text}
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

// ptr returns a pointer to n, for schema bounds.
func ptr(n int) *int {
	return &n
}
