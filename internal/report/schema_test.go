package report

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// schemaNode is the part of a JSON Schema the tests read.
type schemaNode struct {
	Type                 string                `json:"type"`
	Enum                 []string              `json:"enum"`
	Required             []string              `json:"required"`
	AdditionalProperties *bool                 `json:"additionalProperties"`
	Properties           map[string]schemaNode `json:"properties"`
	Items                *schemaNode           `json:"items"`
}

func TestSubmitVerdictSchema(t *testing.T) {
	t.Parallel()

	var schema schemaNode
	require.NoError(t, json.Unmarshal(SubmitVerdictSchema, &schema))

	props := schema.Properties
	prevention := props["falsePositive"].Properties["prevention"].Items
	require.NotNil(t, prevention)

	tests := []struct {
		name string
		node schemaNode
		want []string
	}{
		{
			name: "verdict enum lists every verdict",
			node: props["verdict"],
			want: strs(TruePositive, FalsePositive, Inconclusive, Duplicate, KnownIssue, Flapping),
		},
		{
			name: "confidence enum lists every confidence level",
			node: props["confidence"],
			want: strs(confidenceOrder...),
		},
		{
			name: "prevention kind enum lists every prevention kind",
			node: prevention.Properties["kind"],
			want: strs(PreventThreshold, PreventDuration, PreventFilter, PreventExclusion, PreventDelete, PreventOther),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ElementsMatch(t, tt.want, tt.node.Enum)
		})
	}
}

func TestSubmitVerdictSchemaShape(t *testing.T) {
	t.Parallel()

	var schema schemaNode
	require.NoError(t, json.Unmarshal(SubmitVerdictSchema, &schema))

	props := schema.Properties

	tests := []struct {
		name         string
		node         schemaNode
		wantRequired []string
	}{
		{name: "top level", node: schema, wantRequired: []string{"verdict", "confidence", "reason", "summary", "findings"}},
		{name: "finding", node: *props["findings"].Items, wantRequired: []string{"claim", "evidenceToolCallIds"}},
		{name: "fix", node: *props["fixes"].Items, wantRequired: []string{"title", "detail"}},
		{name: "false positive", node: props["falsePositive"], wantRequired: []string{"whyFired", "prevention"}},
		{
			name:         "prevention",
			node:         *props["falsePositive"].Properties["prevention"].Items,
			wantRequired: []string{"kind", "detail"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, "object", tt.node.Type)
			require.NotNil(t, tt.node.AdditionalProperties, "objects must set additionalProperties")
			require.False(t, *tt.node.AdditionalProperties, "objects must not accept unknown fields")
			require.ElementsMatch(t, tt.wantRequired, tt.node.Required)

			for _, field := range tt.node.Required {
				require.Contains(t, tt.node.Properties, field)
			}
		})
	}
}

func TestAnswerFollowUpSchemaShape(t *testing.T) {
	t.Parallel()

	var schema schemaNode
	require.NoError(t, json.Unmarshal(AnswerFollowUpSchema, &schema))

	require.Equal(t, "object", schema.Type)
	require.NotNil(t, schema.AdditionalProperties, "objects must set additionalProperties")
	require.False(t, *schema.AdditionalProperties, "objects must not accept unknown fields")
	require.ElementsMatch(t, []string{"answer", "evidenceToolCallIds"}, schema.Required)
	require.Equal(t, "string", schema.Properties["answer"].Type)
	require.Equal(t, "array", schema.Properties["evidenceToolCallIds"].Type)
}

func strs[S ~string](values ...S) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}

	return out
}
