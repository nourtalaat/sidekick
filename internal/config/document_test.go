package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDocument(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), exampleYAML(t), testOptions())
	require.NoError(t, err)

	raw, err := json.Marshal(Document(cfg))
	require.NoError(t, err)

	var doc map[string]any

	require.NoError(t, json.Unmarshal(raw, &doc))

	tests := []struct {
		name string
		path []string
		want any
	}{
		{name: "a plain value", path: []string{"server", "publicURL"}, want: "https://sidekick.example.com"},
		{name: "a default", path: []string{"budgets", "concurrency"}, want: float64(DefaultConcurrency)},
		{name: "a duration as text", path: []string{"budgets", "perInvestigation", "maxWallTime"}, want: DefaultMaxWallTime.String()},
		{name: "a secret reference stays a reference", path: []string{"database", "urlSecret"}, want: map[string]any{"name": "sidekick-db", "key": "url"}},
		{name: "a connector's provider", path: []string{"model", "connectors", "claude", "provider"}, want: "anthropic"},
		{name: "a connector's own key", path: []string{"model", "connectors", "claude", "timeout"}, want: "120s"},
		{name: "a connector's secret reference", path: []string{"model", "connectors", "claude", "apiKeySecret"}, want: map[string]any{"name": "sidekick-anthropic", "key": "apiKey"}},
		{name: "a source's type", path: []string{"sources", "signoz", "type"}, want: "mcp"},
		{name: "a webhook's adapter", path: []string{"intake", "webhooks", "alertmanager", "adapter"}, want: "alertmanager"},
		{name: "a webhook's trust", path: []string{"intake", "webhooks", "alertmanager", "trusted"}, want: true},
		{name: "a webhook's tolerance", path: []string{"intake", "webhooks", "generic-ci", "auth", "tolerance"}, want: (5 * time.Minute).String()},
		{name: "a route's name", path: []string{"routes", "0", "name"}, want: cfg.Routes[0].Name},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, at(t, doc, tt.path))
		})
	}
}

func TestDocumentOfNothing(t *testing.T) {
	t.Parallel()

	var missing *Route

	require.Nil(t, Document(missing))
	require.Nil(t, Document(nil))
}

// at returns the value at path in doc, where a number names a list item.
func at(t *testing.T, doc any, path []string) any {
	t.Helper()

	for _, key := range path {
		switch node := doc.(type) {
		case map[string]any:
			doc = node[key]
		case []any:
			var i int

			require.NoError(t, json.Unmarshal([]byte(key), &i))
			require.Less(t, i, len(node))

			doc = node[i]
		default:
			require.FailNow(t, "no value at "+key)
		}
	}

	return doc
}
