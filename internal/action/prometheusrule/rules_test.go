package prometheusrule

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// sampleGroups is the spec.groups of a PrometheusRule with a group whose
// name has a dot, an alert with an expression written as a number, a rule
// with a field the action does not know, and a recording rule.
const sampleGroups = `[
  {"name": "checkout.rules", "interval": "30s", "partial_response_strategy": "warn", "rules": [
    {"alert": "HighLatency", "expr": "latency > 2", "for": "5m", "labels": {"severity": "warning", "team": "pay"}, "annotations": {"summary": "slow"}},
    {"alert": "AlwaysOn", "expr": 1},
    {"record": "job:rate5m", "expr": "rate(x[5m])", "labels": {"k": "v"}, "extra": "kept"}
  ]},
  {"name": "other", "rules": [
    {"alert": "Dup", "expr": "a"}, {"alert": "Dup", "expr": "b"}
  ]}
]`

func mustGroups(t *testing.T, raw string) groupList {
	t.Helper()

	g, err := parseGroups(json.RawMessage(raw))
	require.NoError(t, err)

	return g
}

func TestView(t *testing.T) {
	t.Parallel()

	g := mustGroups(t, sampleGroups)

	doc, missing, err := g.view([]ruleRef{
		{group: "checkout.rules", name: "HighLatency"},
		{group: "checkout.rules", name: "AlwaysOn"},
		{group: "checkout.rules", name: "job:rate5m", record: true},
		{group: "checkout.rules", name: "Nope"},
		{group: "nogroup", name: "HighLatency"},
		{group: "checkout.rules", name: "job:rate5m"},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"checkout.rules": {
	  "HighLatency": {"expr": "latency > 2", "for": "5m", "labels": {"severity": "warning", "team": "pay"}, "annotations": {"summary": "slow"}},
	  "AlwaysOn": {"expr": "1"},
	  "job:rate5m": {"expr": "rate(x[5m])", "labels": {"k": "v"}}
	}}`, string(doc), "the unknown field is left out and the number expression is text")
	require.Equal(t, []ruleRef{
		{group: "checkout.rules", name: "Nope"},
		{group: "nogroup", name: "HighLatency"},
		{group: "checkout.rules", name: "job:rate5m"},
	}, missing, "a record is not found as an alert")
}

func TestViewAmbiguous(t *testing.T) {
	t.Parallel()

	_, _, err := mustGroups(t, sampleGroups).view([]ruleRef{{group: "other", name: "Dup"}})
	require.ErrorIs(t, err, errRuleAmbiguous)
}

func TestEdit(t *testing.T) {
	t.Parallel()

	g := mustGroups(t, sampleGroups)
	refs := []ruleRef{{group: "checkout.rules", name: "HighLatency"}, {group: "checkout.rules", name: "job:rate5m", record: true}}

	got, err := g.edit(refs, []action.Edit{
		{Path: action.Path{"checkout.rules", "HighLatency", "for"}, Value: json.RawMessage(`"10m"`)},
		{Path: action.Path{"checkout.rules", "HighLatency", "labels", "team"}},
		{Path: action.Path{"checkout.rules", "job:rate5m", "expr"}, Value: json.RawMessage(`"sum(x)"`)},
		{Path: action.Path{"checkout.rules", "job:rate5m", "labels", "env"}, Value: json.RawMessage(`"prod"`)},
	})
	require.NoError(t, err)

	raw, err := got.marshal()
	require.NoError(t, err)
	require.JSONEq(t, `[
	  {"name": "checkout.rules", "interval": "30s", "partial_response_strategy": "warn", "rules": [
	    {"alert": "HighLatency", "expr": "latency > 2", "for": "10m", "labels": {"severity": "warning"}, "annotations": {"summary": "slow"}},
	    {"alert": "AlwaysOn", "expr": 1},
	    {"record": "job:rate5m", "expr": "sum(x)", "labels": {"k": "v", "env": "prod"}, "extra": "kept"}
	  ]},
	  {"name": "other", "rules": [{"alert": "Dup", "expr": "a"}, {"alert": "Dup", "expr": "b"}]}
	]`, string(raw), "only the edited fields change and every other field is kept")

	original, err := g.marshal()
	require.NoError(t, err)
	require.JSONEq(t, sampleGroups, string(original), "the groups read are not changed")
}

func TestEditRefuses(t *testing.T) {
	t.Parallel()

	g := mustGroups(t, sampleGroups)
	refs := []ruleRef{{group: "checkout.rules", name: "HighLatency"}}

	tests := []struct {
		name string
		edit action.Edit
	}{
		{name: "a path that stops at the rule", edit: action.Edit{Path: action.Path{"checkout.rules", "HighLatency"}, Value: json.RawMessage(`{}`)}},
		{name: "a rule that is not in refs", edit: action.Edit{Path: action.Path{"checkout.rules", "AlwaysOn", "expr"}, Value: json.RawMessage(`"x"`)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := g.edit(refs, []action.Edit{tt.edit})
			require.ErrorIs(t, err, errBadEdit)
		})
	}
}

func TestValidationFile(t *testing.T) {
	t.Parallel()

	g := mustGroups(t, sampleGroups)

	file, err := g.validationFile([]ruleRef{{group: "checkout.rules", name: "HighLatency"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"groups": [{"name": "checkout.rules", "interval": "30s", "rules": [
	  {"alert": "HighLatency", "expr": "latency > 2", "for": "5m", "labels": {"severity": "warning", "team": "pay"}, "annotations": {"summary": "slow"}},
	  {"alert": "AlwaysOn", "expr": "1"},
	  {"record": "job:rate5m", "expr": "rate(x[5m])", "labels": {"k": "v"}}
	]}]}`, string(file), "the whole group, without the Thanos field, and the other group is left out")
	require.Contains(t, string(file), "rate(x[5m])", "no HTML escapes")
}

func TestCheckShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rule    string
		wantErr string
	}{
		{name: "a full rule", rule: `{"expr": "up == 0", "for": "5m", "keep_firing_for": "1m", "labels": {"a": "b"}, "annotations": {}}`},
		{name: "no expr", rule: `{"for": "5m"}`, wantErr: "expr must be text"},
		{name: "a blank expr", rule: `{"expr": "  "}`, wantErr: "expr must be text"},
		{name: "a for that is a number", rule: `{"expr": "x", "for": 5}`, wantErr: "for must be a duration"},
		{name: "an empty keep_firing_for", rule: `{"expr": "x", "keep_firing_for": ""}`, wantErr: "keep_firing_for must be a duration"},
		{name: "labels that are a list", rule: `{"expr": "x", "labels": ["a"]}`, wantErr: "labels must be an object"},
		{name: "a label that is a number", rule: `{"expr": "x", "labels": {"a": 1}}`, wantErr: "labels.a must be text"},
		{name: "an annotation that is null", rule: `{"expr": "x", "annotations": {"a": null}}`, wantErr: "annotations.a must be text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkShapes(json.RawMessage(`{"g": {"R": ` + tt.rule + `}}`))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
		})
	}
}

func TestRuleFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule string
		want string
	}{
		{name: "an integer expression", rule: `{"alert": "A", "expr": 1}`, want: `{"expr": "1"}`},
		{name: "a negative number", rule: `{"expr": -2}`, want: `{"expr": "-2"}`},
		{name: "a decimal number keeps its text", rule: `{"expr": 1.50}`, want: `{"expr": "1.50"}`},
		{name: "a text that looks like a number", rule: `{"expr": "1"}`, want: `{"expr": "1"}`},
		{name: "null fields are left out", rule: `{"expr": "x", "for": null, "labels": null}`, want: `{"expr": "x"}`},
		{name: "empty labels are kept", rule: `{"expr": "x", "labels": {}}`, want: `{"expr": "x", "labels": {}}`},
		{name: "fields of other names are left out", rule: `{"expr": "x", "record": "r", "other": 1}`, want: `{"expr": "x"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fields, err := ruleFields(json.RawMessage(tt.rule))
			require.NoError(t, err)

			got, err := json.Marshal(fields)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	g := mustGroups(t, sampleGroups)

	require.Equal(t, []string{
		`alert "HighLatency" in group "checkout.rules"`,
		`alert "AlwaysOn" in group "checkout.rules"`,
	}, g.names([]string{"checkout.rules"}, 2), "cut at the limit")
	require.Empty(t, g.names([]string{"missing"}, 10))
}
