package prometheus

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
// source prefix, for example prometheus__query_range.
const (
	toolQuery      = "query"
	toolQueryRange = "query_range"
	toolListAlerts = "list_alerts"
	toolGetRules   = "get_rules"
)

// Limits on what a tool returns.
const (
	// maxSeries bounds the series of query and query_range.
	maxSeries = 50
	// maxPoints is the most points a series keeps. query_range picks its
	// step so a series has no more.
	maxPoints = 250
	// defaultAlerts and maxAlerts bound the alerts of list_alerts.
	defaultAlerts = 50
	maxAlerts     = 200
	// maxRules bounds the rules of get_rules: one alert name can be in
	// several groups.
	maxRules = 20
)

// toolList is what Tools returns. Every tool only reads.
var toolList = []source.Tool{
	{
		Name: toolQuery,
		Description: "Run an instant PromQL query at one time, by default now, for example " +
			"sum by (service) (rate(http_requests_total{code=~\"5..\"}[5m])). Returns each series' labels and value, at most " +
			"50 series. A range selector such as up[10m] returns each series' points instead, as [unix seconds, value].",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"query": stringProperty("The PromQL query. It is checked with the Prometheus parser before it is sent."),
			"time":  timeProperty("The time to evaluate the query at. Default: now."),
		}, "query"),
		ReadOnly: true,
	},
	{
		Name: toolQueryRange,
		Description: "Run a PromQL range query, for example sum by (service) (rate(http_requests_total{code=~\"5..\"}[5m])). " +
			"The step is picked so each series has at most 250 points. Returns at most 50 series, each with its labels, the " +
			"min, max, avg and last of its values, and its points as [unix seconds, value], oldest first. Narrow a query " +
			"that matches more series with label matchers, sum by or topk.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"query": stringProperty("The PromQL query. It must return an instant vector or a scalar, not a range vector such as x[5m]."),
			"since": timeProperty("The start of the range. Default: one hour before until."),
			"until": timeProperty("The end of the range. Default: now."),
		}, "query"),
		ReadOnly: true,
	},
	{
		Name: toolListAlerts,
		Description: "List the alerts that are firing or pending now, firing first and newest first: name, state, since when, " +
			"labels and annotations. When Sidekick reads an Alertmanager, list its alerts instead, with the silences and " +
			"inhibitions that mute them.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"alertname": stringProperty("Only alerts with this alertname label."),
			"limit":     limitProperty(defaultAlerts, maxAlerts),
		}),
		ReadOnly: true,
	},
	{
		Name: toolGetRules,
		Description: "Get the alerting rules with one alert name: their group, PromQL expression, for, labels, annotations, " +
			"health, last error and last evaluation. Run the expression with query_range to see when and why it fired.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"alertname": stringProperty("The alert name, the alertname label of the alert."),
		}, "alertname"),
		ReadOnly: true,
	},
}

// schema returns the JSON Schema of a tool's input: its properties and the
// required ones. It panics when the schema does not encode, which is a
// programming error found by any test.
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
		panic(fmt.Sprintf("prometheus: encode a tool schema: %v", err))
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
