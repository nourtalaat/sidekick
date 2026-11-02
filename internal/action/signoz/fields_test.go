package signoz

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// v1Rule is a rule without schemaVersion, as SigNoz reads it back: the
// threshold and the window are at the top, and there are no read-only keys.
const v1Rule = `{
  "alert": "Checkout errors high", "alertType": "METRIC_BASED_ALERT", "ruleType": "threshold_rule",
  "description": "Error ratio of checkout", "version": "v5", "evalWindow": "5m", "frequency": "1m",
  "disabled": false,
  "labels": {"severity": "critical", "team": "payments"},
  "annotations": {"summary": "Checkout errors are {{$value}}"},
  "preferredChannels": ["slack-oncall"],
  "condition": {
    "compositeQuery": {
      "queries": [{"type": "builder_query", "spec": {
        "name": "A", "signal": "metrics", "source": "", "disabled": false, "legend": "",
        "aggregations": [{"metricName": "checkout_errors", "timeAggregation": "rate", "spaceAggregation": "sum"}],
        "filter": {"expression": "service.name = 'checkout'"}
      }}],
      "panelType": "graph", "queryType": "builder", "unit": "percent"
    },
    "selectedQueryName": "A", "op": "above", "target": 5, "matchType": "at_least_once", "targetUnit": "percent",
    "alertOnAbsent": true, "absentFor": 10
  }
}`

// v2Rule is a rule with schemaVersion v2alpha1, as SigNoz reads it back: a
// threshold that is not set shows as null.
const v2Rule = `{
  "alert": "Checkout latency", "alertType": "METRIC_BASED_ALERT", "ruleType": "threshold_rule",
  "version": "v5", "schemaVersion": "v2alpha1", "disabled": false,
  "labels": {"team": "payments"},
  "condition": {
    "compositeQuery": {
      "queries": [{"type": "builder_query", "spec": {
        "name": "A", "signal": "metrics", "source": "", "disabled": false, "legend": "",
        "aggregations": [{"metricName": "checkout_latency", "timeAggregation": "avg", "spaceAggregation": "avg"}]
      }}],
      "panelType": "graph", "queryType": "builder", "unit": "ms"
    },
    "selectedQueryName": "A",
    "thresholds": {"kind": "basic", "spec": [
      {"name": "critical", "target": 900, "targetUnit": "", "recoveryTarget": null, "matchType": "at_least_once", "op": "above", "channels": ["slack-oncall"]},
      {"name": "warning", "target": 600, "targetUnit": "", "recoveryTarget": null, "matchType": "at_least_once", "op": "above", "channels": null}
    ]}
  },
  "evaluation": {"kind": "rolling", "spec": {"evalWindow": "5m", "frequency": "1m"}},
  "notificationSettings": {"groupBy": ["service.name"], "usePolicy": false, "renotify": {"enabled": true, "interval": "4h", "alertStates": ["firing"]}}
}`

// promRule is a rule with a PromQL query and a cumulative evaluation.
const promRule = `{
  "alert": "Queue depth", "alertType": "METRIC_BASED_ALERT", "ruleType": "promql_rule",
  "version": "v5", "schemaVersion": "v2alpha1", "disabled": false,
  "condition": {
    "compositeQuery": {"queries": [{"type": "promql", "spec": {"name": "A", "query": "sum(queue_depth)", "disabled": false, "step": 0, "stats": false, "legend": ""}}], "panelType": "graph", "queryType": "promql"},
    "selectedQueryName": "A",
    "thresholds": {"kind": "basic", "spec": [{"name": "critical", "target": 100, "targetUnit": "", "recoveryTarget": null, "matchType": "last", "op": "above", "channels": ["slack-oncall"]}]}
  },
  "evaluation": {"kind": "cumulative", "spec": {"schedule": {"type": "hourly", "minute": 0}, "frequency": "1m", "timezone": "UTC"}},
  "notificationSettings": {"usePolicy": false}
}`

// changeOf applies set, a map from field path to new value (null removes),
// to rule, and returns the new rule and the fields that differ.
func changeOf(t *testing.T, rule string, set map[string]string) (json.RawMessage, []action.Delta) {
	t.Helper()

	edits := make([]action.Edit, 0, len(set))

	for key, value := range set {
		path, err := action.ParsePath(key)
		require.NoError(t, err)

		edit := action.Edit{Path: path, Value: json.RawMessage(value)}
		if value == "null" {
			edit.Value = nil
		}

		edits = append(edits, edit)
	}

	after, err := action.Apply(json.RawMessage(rule), edits)
	require.NoError(t, err)

	deltas, err := action.Diff(json.RawMessage(rule), after)
	require.NoError(t, err)

	return after, deltas
}

func TestCheckChangesAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule string
		set  map[string]string
	}{
		{name: "a v1 threshold", rule: v1Rule, set: map[string]string{"condition.target": "10"}},
		{name: "a v1 operator in capitals", rule: v1Rule, set: map[string]string{"condition.op": `"BELOW"`}},
		{name: "a v1 match type", rule: v1Rule, set: map[string]string{"condition.matchType": `"on_average"`}},
		{name: "a v1 window and frequency", rule: v1Rule, set: map[string]string{"evalWindow": `"10m"`, "frequency": `"2m"`}},
		{name: "v1 channels", rule: v1Rule, set: map[string]string{"preferredChannels": `["slack-oncall", "email"]`}},
		{name: "the whole preferred channels list removed", rule: v1Rule, set: map[string]string{"preferredChannels": "null"}},
		{name: "a label changed", rule: v1Rule, set: map[string]string{"labels.team": `"search"`}},
		{name: "a label added", rule: v1Rule, set: map[string]string{"labels.env": `"prod"`}},
		{name: "a label value with a dot", rule: v1Rule, set: map[string]string{"labels.team": `"a.b"`}},
		{name: "a label removed", rule: v1Rule, set: map[string]string{"labels.team": "null"}},
		{name: "every label removed", rule: v1Rule, set: map[string]string{"labels": "null"}},
		{name: "an annotation", rule: v1Rule, set: map[string]string{"annotations.summary": `"Errors"`}},
		{name: "the description", rule: v1Rule, set: map[string]string{"description": `"New"`}},
		{name: "the description removed", rule: v1Rule, set: map[string]string{"description": "null"}},
		{name: "the name", rule: v1Rule, set: map[string]string{"alert": `"New name"`}},
		{name: "disabled", rule: v1Rule, set: map[string]string{"disabled": "true"}},
		{name: "a filter", rule: v1Rule, set: map[string]string{"condition.compositeQuery.queries[0].spec.filter.expression": `"service.name = 'cart'"`}},
		{name: "a filter that was not there", rule: v2Rule, set: map[string]string{"condition.compositeQuery.queries[0].spec.filter.expression": `"service.name = 'cart'"`}},
		{name: "a filter removed", rule: v1Rule, set: map[string]string{"condition.compositeQuery.queries[0].spec.filter": "null"}},
		{name: "a having", rule: v1Rule, set: map[string]string{"condition.compositeQuery.queries[0].spec.having.expression": `"count() > 3"`}},
		{name: "a PromQL query", rule: promRule, set: map[string]string{"condition.compositeQuery.queries[0].spec.query": `"sum(queue_depth) > 1"`}},
		{name: "absence turned off", rule: v1Rule, set: map[string]string{"condition.alertOnAbsent": "null", "condition.absentFor": "null"}},
		{name: "a minimum of points", rule: v1Rule, set: map[string]string{"condition.requireMinPoints": "true", "condition.requiredNumPoints": "3"}},
		{name: "a v2 threshold", rule: v2Rule, set: map[string]string{"condition.thresholds.spec[0].target": "1000"}},
		{name: "a v2 recovery target", rule: v2Rule, set: map[string]string{"condition.thresholds.spec[0].recoveryTarget": "800"}},
		{name: "a v2 whole threshold", rule: v2Rule, set: map[string]string{"condition.thresholds.spec[1]": `{"name": "warning", "target": 650, "targetUnit": "", "recoveryTarget": null, "matchType": "at_least_once", "op": "above", "channels": null}`}},
		{name: "v2 channels", rule: v2Rule, set: map[string]string{"condition.thresholds.spec[1].channels": `["email"]`}},
		{name: "a v2 evaluation window", rule: v2Rule, set: map[string]string{"evaluation.spec.evalWindow": `"15m"`}},
		{name: "a v2 frequency of a cumulative evaluation", rule: promRule, set: map[string]string{"evaluation.spec.frequency": `"2m"`}},
		{name: "v2 grouping", rule: v2Rule, set: map[string]string{"notificationSettings.groupBy": `["service.name", "env"]`}},
		{name: "v2 renotify", rule: v2Rule, set: map[string]string{"notificationSettings.renotify.interval": `"1h"`, "notificationSettings.renotify.alertStates": `["firing", "nodata"]`}},
		{name: "v2 renotify added whole", rule: promRule, set: map[string]string{"notificationSettings.renotify": `{"enabled": true, "interval": "1h", "alertStates": ["firing"]}`}},
		{name: "v2 renotify removed", rule: v2Rule, set: map[string]string{"notificationSettings.renotify": "null"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			after, deltas := changeOf(t, tt.rule, tt.set)
			require.NotEmpty(t, deltas)
			require.NoError(t, checkChanges(after, deltas))
		})
	}
}

func TestCheckChangesRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rule    string
		set     map[string]string
		wantErr string
	}{
		{name: "a field that is not supported", rule: v1Rule, set: map[string]string{"alertType": `"LOGS_BASED_ALERT"`}, wantErr: "not a field Sidekick can change"},
		{name: "a typo that SigNoz would drop", rule: v1Rule, set: map[string]string{"condition.targt": "3"}, wantErr: `"condition.targt"`},
		{name: "a query that is not changed here", rule: v1Rule, set: map[string]string{"condition.compositeQuery.queries[0].spec.aggregations": "[]"}, wantErr: "not a field Sidekick can change"},
		{name: "a label name with a dot", rule: v1Rule, set: map[string]string{"labels.service.name": `"checkout"`}, wantErr: "a dot in a path separates keys"},
		{name: "a label name that is not valid", rule: v1Rule, set: map[string]string{"labels.1team": `"x"`}, wantErr: "is not a valid name"},
		{name: "a label that is not text", rule: v1Rule, set: map[string]string{"labels.team": "3"}, wantErr: "must be text"},
		{name: "a description that is not text", rule: v1Rule, set: map[string]string{"description": "3"}, wantErr: "must be text"},
		{name: "a name that is empty", rule: v1Rule, set: map[string]string{"alert": `" "`}, wantErr: "must be text that is not empty"},
		{name: "the name removed", rule: v1Rule, set: map[string]string{"alert": "null"}, wantErr: "cannot be removed"},
		{name: "disabled that is not a flag", rule: v1Rule, set: map[string]string{"disabled": `"yes"`}, wantErr: "must be true or false"},
		{name: "a target that is text", rule: v1Rule, set: map[string]string{"condition.target": `"5"`}, wantErr: "must be a number"},
		{name: "a target removed", rule: v1Rule, set: map[string]string{"condition.target": "null"}, wantErr: "cannot be removed"},
		{name: "an operator that SigNoz does not know", rule: v1Rule, set: map[string]string{"condition.op": `"bigger"`}, wantErr: "must be one of"},
		{name: "a match type that SigNoz does not know", rule: v1Rule, set: map[string]string{"condition.matchType": `"often"`}, wantErr: "must be one of"},
		{name: "a window that is a number", rule: v1Rule, set: map[string]string{"evalWindow": "300"}, wantErr: "a number would mean nanoseconds"},
		{name: "a window that is zero", rule: v1Rule, set: map[string]string{"evalWindow": `"0s"`}, wantErr: "above zero"},
		{name: "a window that is not a duration", rule: v1Rule, set: map[string]string{"frequency": `"often"`}, wantErr: "must be a duration in text"},
		{name: "channels that are not a list", rule: v1Rule, set: map[string]string{"preferredChannels": `"slack"`}, wantErr: "a list of text"},
		{name: "channels with an empty name", rule: v1Rule, set: map[string]string{"preferredChannels": `[""]`}, wantErr: "a list of text"},
		{name: "a count that is negative", rule: v1Rule, set: map[string]string{"condition.absentFor": "-1"}, wantErr: "a whole number, 0 or more"},
		{name: "a count that is not whole", rule: v1Rule, set: map[string]string{"condition.absentFor": "1.5"}, wantErr: "a whole number, 0 or more"},
		{name: "a minimum of points with no count", rule: v1Rule, set: map[string]string{"condition.requireMinPoints": "true"}, wantErr: "needs condition.requiredNumPoints above 0"},
		{name: "a v2 field on a v1 rule", rule: v1Rule, set: map[string]string{"evaluation.spec.evalWindow": `"10m"`}, wantErr: "applies to a rule with schemaVersion v2alpha1, and this rule without schemaVersion"},
		{name: "a v1 field on a v2 rule", rule: v2Rule, set: map[string]string{"condition.target": "3"}, wantErr: "applies to a rule without schemaVersion, and this rule with schemaVersion v2alpha1"},
		{name: "a v1 window on a v2 rule", rule: v2Rule, set: map[string]string{"evalWindow": `"10m"`}, wantErr: "SigNoz would keep the value and ignore it"},
		{name: "a threshold added", rule: v2Rule, set: map[string]string{"condition.thresholds.spec": `[{"name": "a"}]`}, wantErr: "change the fields of its items one at a time"},
		{name: "a threshold list replaced by one of another length", rule: v2Rule, set: map[string]string{"condition.thresholds.spec": `[{"name": "critical", "target": 1, "matchType": "last", "op": "above"}, {"name": "w", "target": 1, "matchType": "last", "op": "above"}, {"name": "x", "target": 1, "matchType": "last", "op": "above"}]`}, wantErr: "change the fields of its items one at a time"},
		{name: "a recovery target that is text", rule: v2Rule, set: map[string]string{"condition.thresholds.spec[0].recoveryTarget": `"80"`}, wantErr: "must be a number"},
		{name: "a threshold name removed", rule: v2Rule, set: map[string]string{"condition.thresholds.spec[0].name": "null"}, wantErr: "cannot be removed"},
		{name: "a window on a cumulative evaluation", rule: promRule, set: map[string]string{"evaluation.spec.evalWindow": `"10m"`}, wantErr: "exists only when evaluation.kind is rolling"},
		{name: "an alert state that does not exist", rule: v2Rule, set: map[string]string{"notificationSettings.renotify.alertStates": `["resolved"]`}, wantErr: "a list of firing and nodata"},
		{name: "renotify enabled with no interval", rule: promRule, set: map[string]string{"notificationSettings.renotify": `{"enabled": true}`}, wantErr: "notificationSettings.renotify.interval must be a duration above zero"},
		{name: "a filter on a PromQL query", rule: promRule, set: map[string]string{"condition.compositeQuery.queries[0].spec.filter.expression": `"a = 1"`}, wantErr: `applies to a builder query, and this query is "promql"`},
		{name: "a query text on a builder query", rule: v1Rule, set: map[string]string{"condition.compositeQuery.queries[0].spec.query": `"up"`}, wantErr: `applies to a PromQL or ClickHouse SQL query, and this query is "builder_query"`},
		{name: "a rule of a schema Sidekick does not know", rule: `{"alert": "x", "schemaVersion": "v3", "condition": {}}`, set: map[string]string{"alert": `"y"`}, wantErr: `schemaVersion "v3"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			after, deltas := changeOf(t, tt.rule, tt.set)

			err := checkChanges(after, deltas)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
		})
	}
}

func TestFieldKnown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		field string
		want  bool
	}{
		{field: "condition", want: true},
		{field: "condition.thresholds", want: true},
		{field: "condition.thresholds.spec[0].target", want: true},
		{field: "condition.thresholds.spec[*].target", want: true},
		{field: "labels", want: true},
		{field: "labels.team", want: true},
		{field: "condition.compositeQuery.queries[*].spec.filter", want: true},
		{field: "alert", want: true},
		{field: "alertType", want: false},
		{field: "condition.targt", want: false},
		{field: "labels.team.x", want: false},
		{field: "condition.thresholds.spec.target", want: false},
		{field: "id", want: false},
		{field: "updatedAt", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()

			path, err := action.ParsePath(tt.field)
			require.NoError(t, err)
			require.Equal(t, tt.want, fieldKnown(path))
		})
	}
}

func TestSupportedWithin(t *testing.T) {
	t.Parallel()

	fields, err := action.ParsePaths([]string{"condition.thresholds", "labels"})
	require.NoError(t, err)

	require.Equal(t, []string{
		"labels.<name>",
		"condition.thresholds.spec[n].name",
		"condition.thresholds.spec[n].target",
		"condition.thresholds.spec[n].targetUnit",
		"condition.thresholds.spec[n].recoveryTarget",
		"condition.thresholds.spec[n].matchType",
		"condition.thresholds.spec[n].op",
		"condition.thresholds.spec[n].channels",
	}, supportedWithin(fields))
}
