package signoz

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestNormalise(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "enum text in lower case",
			in:   `{"condition": {"op": " ABOVE ", "matchType": "At_Least_Once", "thresholds": {"spec": [{"op": ">", "matchType": "AVG", "target": 1}]}}}`,
			want: `{"condition": {"op": "above", "matchType": "at_least_once", "thresholds": {"spec": [{"op": ">", "matchType": "avg", "target": 1}]}}}`,
		},
		{
			name: "alert states in lower case",
			in:   `{"notificationSettings": {"renotify": {"enabled": true, "alertStates": ["Firing", "NoData"]}}}`,
			want: `{"notificationSettings": {"renotify": {"enabled": true, "alertStates": ["firing", "nodata"]}}}`,
		},
		{
			name: "optional values SigNoz does not keep when empty",
			in:   `{"description": "", "labels": {}, "annotations": {}, "preferredChannels": [], "condition": {"alertOnAbsent": false, "absentFor": 0, "requireMinPoints": false, "requiredNumPoints": 0, "targetUnit": ""}}`,
			want: `{"condition": {}}`,
		},
		{
			name: "a threshold that SigNoz shows with null and empty values",
			in:   `{"condition": {"thresholds": {"spec": [{"name": "a", "target": 0, "targetUnit": "", "recoveryTarget": null, "channels": null}, {"name": "b", "channels": []}]}}}`,
			want: `{"condition": {"thresholds": {"spec": [{"name": "a", "target": 0}, {"name": "b", "channels": []}]}}}`,
		},
		{
			name: "notification settings that are empty",
			in:   `{"notificationSettings": {"groupBy": null, "usePolicy": false, "newGroupEvalDelay": null, "renotify": {"enabled": false, "alertStates": null}}}`,
			want: `{"notificationSettings": {"usePolicy": false, "renotify": {"enabled": false}}}`,
		},
		{
			name: "zero, false and empty lists SigNoz keeps for fields without omitempty",
			in:   `{"condition": {"thresholds": {"spec": [{"name": "a", "recoveryTarget": 0, "targetUnit": "", "channels": []}]}}, "notificationSettings": {"groupBy": [], "renotify": {"alertStates": []}}}`,
			want: `{"condition": {"thresholds": {"spec": [{"name": "a", "recoveryTarget": 0, "channels": []}]}}, "notificationSettings": {"groupBy": [], "renotify": {"alertStates": []}}}`,
		},
		{
			name: "a recovery target of zero is a value, a null one is not",
			in:   `{"condition": {"thresholds": {"spec": [{"recoveryTarget": 0.0}, {"recoveryTarget": null}]}}}`,
			want: `{"condition": {"thresholds": {"spec": [{"recoveryTarget": 0.0}, {}]}}}`,
		},
		{
			name: "values that are kept",
			in:   `{"description": "text", "labels": {"a": ""}, "disabled": false, "condition": {"target": 0, "absentFor": 3}}`,
			want: `{"description": "text", "labels": {"a": ""}, "disabled": false, "condition": {"target": 0, "absentFor": 3}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalise(json.RawMessage(tt.in))
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestNormaliseRefusesBadJSON(t *testing.T) {
	t.Parallel()

	_, err := normalise(json.RawMessage(`{`))
	require.Error(t, err)
}

func TestSameDefinitionIgnoresWhatSigNozShapes(t *testing.T) {
	t.Parallel()

	readBack := `{
      "alert": "Checkout latency", "alertType": "METRIC_BASED_ALERT", "ruleType": "threshold_rule",
      "version": "v5", "schemaVersion": "v2alpha1", "disabled": false, "labels": {"team": "payments"},
      "condition": {
        "compositeQuery": {"queries": [{"type": "builder_query", "spec": {"name": "A", "signal": "metrics", "source": "", "disabled": false, "legend": "",
          "aggregations": [{"metricName": "checkout_latency", "timeAggregation": "avg", "spaceAggregation": "avg"}]}}],
          "panelType": "graph", "queryType": "builder", "unit": "ms"},
        "selectedQueryName": "A",
        "thresholds": {"kind": "basic", "spec": [
          {"name": "critical", "target": 900.0, "matchType": "AT_LEAST_ONCE", "op": "ABOVE", "channels": ["slack-oncall"]},
          {"name": "warning", "target": 600, "matchType": "at_least_once", "op": "above"}
        ]}
      },
      "evaluation": {"kind": "rolling", "spec": {"evalWindow": "5m", "frequency": "1m"}},
      "notificationSettings": {"groupBy": ["service.name"], "usePolicy": false, "renotify": {"enabled": true, "interval": "4h", "alertStates": ["FIRING"]}}
    }`

	same, err := sameDefinition(json.RawMessage(v2Rule), json.RawMessage(readBack))
	require.NoError(t, err)
	require.True(t, same)

	other := `{"alert": "Checkout latency", "disabled": true}`
	same, err = sameDefinition(json.RawMessage(`{"alert": "Checkout latency", "disabled": false}`), json.RawMessage(other))
	require.NoError(t, err)
	require.False(t, same)
}

func TestHoldsAfterAndBefore(t *testing.T) {
	t.Parallel()

	after, deltas := changeOf(t, v2Rule, map[string]string{
		"condition.thresholds.spec[0].recoveryTarget": "800",
		"condition.thresholds.spec[0].op":             `"BELOW"`,
		"labels.team":                                 "null",
		"notificationSettings.renotify.interval":      `"1h"`,
	})
	snap := action.Snapshot{Before: json.RawMessage(v2Rule), After: after}
	require.Len(t, deltas, 4)

	t.Run("the rule before the change", func(t *testing.T) {
		t.Parallel()

		held, err := holdsBefore(json.RawMessage(v2Rule), snap)
		require.NoError(t, err)
		require.True(t, held)

		held, err = holdsAfter(json.RawMessage(v2Rule), snap)
		require.NoError(t, err)
		require.False(t, held)
	})

	t.Run("the rule after the change, as SigNoz reads it back", func(t *testing.T) {
		t.Parallel()

		// SigNoz reads the operator in lower case and shows no labels at all
		// when the map is empty.
		readBack, _ := changeOf(t, v2Rule, map[string]string{
			"condition.thresholds.spec[0].recoveryTarget": "800.0",
			"condition.thresholds.spec[0].op":             `"below"`,
			"labels":                                      "null",
			"notificationSettings.renotify.interval":      `"1h"`,
		})

		held, err := holdsAfter(readBack, snap)
		require.NoError(t, err)
		require.True(t, held)

		wrong, err := keptWrong(readBack, snap, false)
		require.NoError(t, err)
		require.Len(t, wrong, 4, "none of the old values is left")
	})

	t.Run("a field that SigNoz kept another way", func(t *testing.T) {
		t.Parallel()

		readBack, _ := changeOf(t, string(after), map[string]string{"notificationSettings.renotify.interval": `"2h"`})

		wrong, err := keptWrong(readBack, snap, true)
		require.NoError(t, err)
		require.Equal(t, []string{"notificationSettings.renotify.interval"}, wrong)
	})
}
