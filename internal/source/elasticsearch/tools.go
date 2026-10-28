package elasticsearch

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
// source prefix, for example logs__search_logs.
const (
	toolSearchLogs = "search_logs"
	toolCountLogs  = "count_logs"
	toolListFields = "list_fields"
)

// Limits on how many items a tool returns.
const (
	// defaultHits and maxHits bound the hits of search_logs.
	defaultHits = 50
	maxHits     = 200
	// maxSplitValues is how many values of the split field count_logs
	// counts on their own.
	maxSplitValues = 10
	// maxBuckets bounds the time buckets of one count_logs series.
	maxBuckets = 200
	// minInterval is the shortest time bucket of count_logs, in seconds.
	minInterval = 1
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolSearchLogs,
		Description: "Search the logs in an Elasticsearch or OpenSearch index pattern in a time range, newest first, with " +
			"a Lucene query string such as level:error AND service.name:checkout. Returns each hit's index, ID and " +
			"chosen fields, long values cut, the total number of matches, and a link to the search.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"query": stringProperty("The Lucene query string, for example status:>=500 AND NOT path:\"/healthz\". " +
				"Empty matches every log in the time range."),
			"since": timeProperty("Only logs at or after this time. Required; the range may be at most 7 days."),
			"until": timeProperty("Only logs before this time. Default: now."),
			"fields": {Type: "array", Items: &jsonschema.Schema{Type: "string"}, MaxItems: new(maxFields),
				Description: "The fields to return, such as service.name or kubernetes.*. The time and message fields " +
					"are always returned. Default: the time and message fields and a few common ones such as log.level."},
			"limit": limitProperty(defaultHits, maxHits),
		}, "since"),
		ReadOnly: true,
	},
	{
		Name: toolCountLogs,
		Description: "Count the logs in an Elasticsearch or OpenSearch index pattern that match a Lucene query string, " +
			"in time buckets over a range, optionally split by the 10 most common values of one field. Shows when " +
			"errors started or stopped, and which service or host they come from.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"query": stringProperty("The Lucene query string, for example level:error. Empty counts every log."),
			"since": timeProperty("The start of the range. Required; the range may be at most 7 days."),
			"until": timeProperty("The end of the range. Default: now."),
			"interval": {Type: "integer", Minimum: ptr(minInterval), Description: fmt.Sprintf("The length of one time bucket, in "+
				"seconds. At most %d buckets fit in the range. Default: about 60 buckets.", maxBuckets)},
			"splitBy": stringProperty("A keyword field to split the counts by, such as service.name or host.name. " +
				"list_fields says which fields are aggregatable."),
		}, "since"),
		ReadOnly: true,
	},
	{
		Name: toolListFields,
		Description: "List the fields of an Elasticsearch or OpenSearch index pattern with their types, and whether " +
			"each can be searched and counted by, so queries name fields that exist.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"fields": stringProperty("Only fields whose names match this pattern, such as kubernetes.* or *.name. Default: every field."),
		}),
		ReadOnly: true,
	},
}

// schema returns the JSON Schema of a tool's input: the index property,
// the tool's own properties and the required ones. It panics when the
// schema does not encode, which is a programming error found by any test.
func schema(props map[string]*jsonschema.Schema, required ...string) json.RawMessage {
	all := map[string]*jsonschema.Schema{
		"index": stringProperty("The index or index pattern to read, such as logs-*, one that Sidekick's config allows. " +
			"It may be left out when Sidekick allows only one pattern."),
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
		panic(fmt.Sprintf("elasticsearch: encode a tool schema: %v", err))
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
		Description: fmt.Sprintf("The most hits to return, 1 to %d. Default %d.", most, def),
		Minimum:     ptr(1),
		Maximum:     ptr(most),
	}
}

// ptr returns a pointer to n as a float64, for schema bounds.
func ptr(n int) *float64 {
	f := float64(n)

	return &f
}
