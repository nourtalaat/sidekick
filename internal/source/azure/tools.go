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
	toolQueryLogs         = "query_logs"
	toolQueryMetrics      = "query_metrics"
	toolListAlerts        = "list_alerts"
	toolListChanges       = "list_changes"
	toolGetResourceHealth = "get_resource_health"
	toolGetAKSCluster     = "get_aks_cluster"
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
	// defaultSeries and maxSeries bound the time series of query_metrics.
	defaultSeries = 10
	maxSeries     = 50
	// defaultItems and maxItems bound the alerts of list_alerts.
	defaultItems = 30
	maxItems     = 100
)

// Values the enum properties take.
var (
	aggregations = []any{"Average", "Total", "Minimum", "Maximum", "Count"}
	intervals    = []any{"PT1M", "PT5M", "PT15M", "PT30M", "PT1H", "PT6H", "PT12H", "P1D"}
	alertStates  = []any{stateFired, stateResolved, stateAny}
)

// States list_alerts filters by.
const (
	stateFired    = "fired"
	stateResolved = "resolved"
	stateAny      = "any"
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
		Name: toolQueryMetrics,
		Description: "Read Azure Monitor platform metrics of one resource over a time range, such as Requests or " +
			"CpuPercentage, optionally split by a dimension filter. Returns each series' dimensions and points, oldest first.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"resource": resourceProperty("The resource whose metrics to read."),
			"metrics": {Type: "array", Items: &jsonschema.Schema{Type: "string"}, MinItems: ptr(1), MaxItems: ptr(maxMetricNames),
				Description: "The metric names, for example [\"Requests\", \"Http5xx\"]."},
			"namespace":   stringProperty("The metric namespace, for example Microsoft.Web/sites. Default: the resource's own."),
			"aggregation": {Type: "string", Description: "How points are aggregated. Default Average.", Enum: aggregations},
			"interval":    {Type: "string", Description: "The time grain of the points. Default: the smallest that gives at most 120 points.", Enum: intervals},
			"filter": stringProperty("An OData dimension filter, passed to Azure Monitor as given, for example " +
				"\"Instance eq '*'\" to split by instance."),
			"since": timeProperty("The start of the range. Default: one hour before until."),
			"until": timeProperty("The end of the range. Default: now."),
			"limit": limitProperty(defaultSeries, maxSeries),
		}, "resource", "metrics"),
		ReadOnly: true,
	},
	{
		Name: toolListAlerts,
		Description: "List the Azure Monitor alerts of a subscription that started in a time range, newest first: " +
			"severity, state, condition, rule, target resource, times and a portal link.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"subscription":  subscriptionProperty(),
			"resourceGroup": stringProperty("Only alerts on resources in this resource group."),
			"resource":      resourceProperty("Only alerts on this resource."),
			"state": {Type: "string", Description: "Only alerts whose condition is fired or resolved. Default any.",
				Enum: alertStates},
			"since": timeProperty("Only alerts that started at or after this time. Default: 24 hours before until."),
			"until": timeProperty("Only alerts that started before this time. Default: now."),
			"limit": limitProperty(defaultItems, maxItems),
		}),
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
	{
		Name: toolGetResourceHealth,
		Description: "Get the Resource Health of one resource: available, degraded or unavailable, why, and since " +
			"when. Without resource, list the resources of the subscription that are not available, and the Azure " +
			"service health events that affect it.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"subscription": subscriptionProperty(),
			"resource":     resourceProperty("The resource to check. Leave it out to check the whole subscription."),
			"since":        timeProperty("Only service health events updated at or after this time. Default: 24 hours before until."),
			"until":        timeProperty("Only service health events updated before this time. Default: now."),
		}),
		ReadOnly: true,
	},
	{
		Name: toolGetAKSCluster,
		Description: "Get an AKS cluster: provisioning and power state, Kubernetes version, upgrade channel, and " +
			"its node pools with their state, version, size and autoscaling. Without cluster, list the subscription's " +
			"clusters with their state.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"subscription":  subscriptionProperty(),
			"resourceGroup": stringProperty("The cluster's resource group. Required with cluster."),
			"cluster":       stringProperty("The cluster's name. Leave it out to list the clusters."),
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

// ptr returns a pointer to n, for schema bounds.
func ptr(n int) *int {
	return &n
}
