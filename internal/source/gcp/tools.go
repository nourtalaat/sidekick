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
	toolQueryLogs               = "query_logs"
	toolQueryMetrics            = "query_metrics"
	toolQueryPromQL             = "query_promql"
	toolListAlerts              = "list_alerts"
	toolListChanges             = "list_changes"
	toolGetGKECluster           = "get_gke_cluster"
	toolListServiceHealthEvents = "list_service_health_events"
)

// Limits on how many items a tool returns.
const (
	// defaultEntries and maxEntries bound the log entries of query_logs and
	// list_changes.
	defaultEntries = 100
	maxEntries     = 500
	// defaultSeries and maxSeries bound the time series of query_metrics
	// and query_promql.
	defaultSeries = 20
	maxSeries     = 100
	// defaultItems and maxItems bound the alerts and service health events
	// of a list.
	defaultItems = 30
	maxItems     = 100
)

// Values the enum properties take.
var (
	aligners = []any{
		"ALIGN_MEAN", "ALIGN_MAX", "ALIGN_MIN", "ALIGN_SUM", "ALIGN_COUNT", "ALIGN_RATE", "ALIGN_DELTA",
		"ALIGN_NEXT_OLDER", "ALIGN_PERCENTILE_99", "ALIGN_PERCENTILE_95", "ALIGN_PERCENTILE_50",
	}
	reducers = []any{
		"REDUCE_MEAN", "REDUCE_MAX", "REDUCE_MIN", "REDUCE_SUM", "REDUCE_COUNT",
		"REDUCE_PERCENTILE_99", "REDUCE_PERCENTILE_95", "REDUCE_PERCENTILE_50",
	}
	alertStates  = []any{stateOpen, stateClosed, stateAny}
	healthStates = []any{stateActive, stateClosed, stateAny}
)

// States a list tool filters by.
const (
	stateOpen   = "open"
	stateClosed = "closed"
	stateActive = "active"
	stateAny    = "any"
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
		Name: toolQueryMetrics,
		Description: "Read Cloud Monitoring time series of a Google Cloud project with a monitoring filter such as " +
			"metric.type=\"run.googleapis.com/request_count\" AND resource.labels.service_name=\"checkout\", optionally " +
			"aligned and reduced. Returns each series' metric and resource labels and its points, newest first.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"filter": stringProperty("The monitoring filter, passed to Cloud Monitoring as given. It must name one metric.type."),
			"since":  timeProperty("The start of the range. Default: one hour before until."),
			"until":  timeProperty("The end of the range. Default: now."),
			"alignmentPeriod": {Type: "integer", Description: "Align points to periods of this many seconds, at least 60. " +
				"Default: raw points, or a period that gives about 120 points when aligner is set.", Minimum: ptr(minAlignment)},
			"aligner": {Type: "string", Description: "How each series is aligned. ALIGN_RATE suits counters; " +
				"ALIGN_MEAN suits gauges. Default: none, or ALIGN_MEAN with alignmentPeriod.", Enum: aligners},
			"reducer": {Type: "string", Description: "Combine series with this reducer, grouped by groupBy. Needs an aligner.",
				Enum: reducers},
			"groupBy": {Type: "array", Items: &jsonschema.Schema{Type: "string"},
				Description: "Labels to keep when reducing, for example resource.labels.service_name."},
			"limit": limitProperty(defaultSeries, maxSeries),
		}, "filter"),
		ReadOnly: true,
	},
	{
		Name: toolQueryPromQL,
		Description: "Run a PromQL range query on the Cloud Monitoring metrics of a Google Cloud project, including " +
			"Managed Service for Prometheus metrics. Returns each series' labels and its points, oldest first.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"query": stringProperty("The PromQL query, for example sum(rate(http_requests_total{code=~\"5..\"}[5m])) by (service)."),
			"since": timeProperty("The start of the range. Default: one hour before until."),
			"until": timeProperty("The end of the range. Default: now."),
			"step": {Type: "integer", Description: "Seconds between points, at least 10. Default: a step that gives about " +
				"120 points.", Minimum: ptr(minStep)},
			"limit": limitProperty(defaultSeries, maxSeries),
		}, "query"),
		ReadOnly: true,
	},
	{
		Name: toolListAlerts,
		Description: "List the Cloud Monitoring alerts of a Google Cloud project that were open at some time in a range, " +
			"newest first: state, open and close times, policy and severity, metric, resource and a console link.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"state": {Type: "string", Description: "Only alerts in this state. Default any.", Enum: alertStates},
			"since": timeProperty("Only alerts open at or after this time. Default: 24 hours before until."),
			"until": timeProperty("Only alerts opened before this time. Default: now."),
			"limit": limitProperty(defaultItems, maxItems),
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
	{
		Name: toolGetGKECluster,
		Description: "Get a GKE cluster of a Google Cloud project: status, versions, conditions, node pools with their " +
			"status and autoscaling, and its most recent operations such as upgrades. Without cluster, list the " +
			"project's clusters with their status.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"location": stringProperty("The cluster's region or zone, for example europe-west1. Required with cluster."),
			"cluster":  stringProperty("The cluster's name. Leave it out to list the clusters."),
		}),
		ReadOnly: true,
	},
	{
		Name: toolListServiceHealthEvents,
		Description: "List the Google Cloud incidents that Personalized Service Health says affect a project, most " +
			"recently updated first: title, state, products and locations, times and the latest update.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"state": {Type: "string", Description: "Only events in this state. Default any.", Enum: healthStates},
			"since": timeProperty("Only events updated at or after this time. Default: 24 hours before until."),
			"until": timeProperty("Only events updated before this time. Default: now."),
			"limit": limitProperty(defaultItems, maxItems),
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
