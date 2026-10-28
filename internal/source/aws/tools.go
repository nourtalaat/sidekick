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
	toolQueryLogs        = "query_logs"
	toolQueryMetrics     = "query_metrics"
	toolListAlarms       = "list_alarms"
	toolListChanges      = "list_changes"
	toolGetEKSCluster    = "get_eks_cluster"
	toolGetECSService    = "get_ecs_service"
	toolListHealthEvents = "list_health_events"
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
	// maxMetricQueries bounds the queries of one query_metrics call.
	maxMetricQueries = 10
	// defaultSeries and maxSeries bound the series of query_metrics.
	defaultSeries = 10
	maxSeries     = 50
	// defaultAlarms and maxAlarms bound the alarms, or the history items
	// of one alarm, of list_alarms.
	defaultAlarms = 30
	maxAlarms     = 100
	// defaultServiceEvents and maxServiceEvents bound the service events
	// of get_ecs_service. ECS keeps the latest 100.
	defaultServiceEvents = 20
	maxServiceEvents     = 100
	// defaultHealthEvents and maxHealthEvents bound the events of
	// list_health_events.
	defaultHealthEvents = 20
	maxHealthEvents     = 100
)

// Values the enum properties take.
var (
	alarmStates  = []any{stateAlarm, stateOK, stateInsufficient, stateAny}
	healthStates = []any{healthOpen, healthUpcoming, healthClosed, stateAny}
)

// States list_alarms and list_health_events filter by.
const (
	stateAlarm        = "ALARM"
	stateOK           = "OK"
	stateInsufficient = "INSUFFICIENT_DATA"
	stateAny          = "any"
	healthOpen        = "open"
	healthUpcoming    = "upcoming"
	healthClosed      = "closed"
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
		Name: toolQueryMetrics,
		Description: "Read CloudWatch metrics over a time range with GetMetricData: metrics by namespace, name, dimensions " +
			"and statistic, and metric math expressions over them, such as 100 * errors / requests. Returns each series' points, oldest first.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"account": accountProperty(),
			"region":  regionProperty(),
			"queries": {Type: "array", Items: metricQuerySchema(), MinItems: ptr(1), MaxItems: ptr(maxMetricQueries),
				Description: "The metrics and expressions to read. Each has an id and either namespace and metric, or expression."},
			"period": {Type: "integer", Minimum: ptrFloat(minPeriod), Description: "The seconds each point covers, a multiple of 60. " +
				"Default and least: the smallest that gives at most 120 points per series."},
			"since": timeProperty("The start of the range. Default: one hour before until."),
			"until": timeProperty("The end of the range. Default: now."),
			"limit": limitProperty(defaultSeries, maxSeries),
		}, "queries"),
		ReadOnly: true,
	},
	{
		Name: toolListAlarms,
		Description: "List the CloudWatch alarms in a state, by default ALARM, with their reason, metric and threshold. " +
			"With alarm, return that alarm and its state changes in a time range, newest first.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"account": accountProperty(),
			"region":  regionProperty(),
			"alarm":   stringProperty("The name of one alarm, to read its state history."),
			"prefix":  stringProperty("Only alarms whose name starts with this."),
			"state":   {Type: "string", Description: "Only alarms in this state. Default ALARM.", Enum: alarmStates},
			"since":   timeProperty("Only state changes at or after this time. Default: 24 hours before until."),
			"until":   timeProperty("Only state changes before this time. Default: now."),
			"limit":   limitProperty(defaultAlarms, maxAlarms),
		}),
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
	{
		Name: toolGetEKSCluster,
		Description: "Get an EKS cluster: status, Kubernetes and platform version, support type and health issues, " +
			"and its managed node groups with their status, version, size and health issues. Without cluster, list the clusters.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"account": accountProperty(),
			"region":  regionProperty(),
			"cluster": stringProperty("The cluster's name. Leave it out to list the clusters."),
		}),
		ReadOnly: true,
	},
	{
		Name: toolGetECSService,
		Description: "Get an ECS service: status, desired, running and pending tasks, its deployments with their rollout " +
			"state, and its latest service events, newest first. Without service, list the cluster's services.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"account": accountProperty(),
			"region":  regionProperty(),
			"cluster": stringProperty("The ECS cluster's name."),
			"service": stringProperty("The service's name. Leave it out to list the cluster's services."),
			"limit":   limitProperty(defaultServiceEvents, maxServiceEvents),
		}, "cluster"),
		ReadOnly: true,
	},
	{
		Name: toolListHealthEvents,
		Description: "List the AWS Health events of the account in the Region and for global services, updated in a " +
			"time range, newest first: service, type, status, times and the latest description. Needs a Business, " +
			"Enterprise On-Ramp or Enterprise support plan.",
		InputSchema: schema(map[string]*jsonschema.Schema{
			"account": accountProperty(),
			"region":  regionProperty(),
			"status":  {Type: "string", Description: "Only events with this status. Default any.", Enum: healthStates},
			"since":   timeProperty("Only events updated at or after this time. Default: 24 hours before until."),
			"until":   timeProperty("Only events updated before this time. Default: now."),
			"limit":   limitProperty(defaultHealthEvents, maxHealthEvents),
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

// metricQuerySchema returns the schema of one query of query_metrics.
func metricQuerySchema() *jsonschema.Schema {
	props := map[string]*jsonschema.Schema{
		"id":        stringProperty("The query's ID, starting with a lower-case letter, for example m1. Expressions name other queries by it."),
		"namespace": stringProperty("The metric's namespace, for example AWS/ApplicationELB."),
		"metric":    stringProperty("The metric's name, for example HTTPCode_Target_5XX_Count."),
		"dimensions": {Type: "object", AdditionalProperties: &jsonschema.Schema{Type: "string"},
			Description: "The metric's dimensions by name, for example {\"LoadBalancer\": \"app/shop/50dc6c495c0c9188\"}."},
		"stat": stringProperty("The metric's statistic: Average, Sum, Minimum, Maximum, SampleCount or a percentile such as p99. Default Average."),
		"expression": stringProperty("A metric math expression over the IDs of other queries, for example 100 * m1 / m2, " +
			"instead of namespace and metric."),
		"label":  stringProperty("A name for the series."),
		"hidden": {Type: "boolean", Description: "Leave the query's own series out, for one only used in an expression."},
	}

	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             []string{"id"},
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		PropertyOrder:        slices.Sorted(maps.Keys(props)),
	}
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

// ptrFloat returns a pointer to n as a float, for schema bounds.
func ptrFloat(n int) *float64 {
	f := float64(n)

	return &f
}
