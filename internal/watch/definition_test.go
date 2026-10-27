package watch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/jsonpath"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

func TestCheckOf(t *testing.T) {
	t.Parallel()

	path, err := jsonpath.Parse("$.series[0].last")
	require.NoError(t, err)

	tests := []struct {
		name    string
		yaml    string
		want    Check
		wantErr string
	}{
		{
			name: "an HTTP check",
			yaml: `{type: http, url: "https://a.example.com/healthz", expect: {status: [200, 204], bodyRegex: ok}, headers: {X-Token: {name: s, key: k}}, timeout: 5s}`,
			want: HTTPCheck{
				URL:     "https://a.example.com/healthz",
				Expect:  Expect{Status: []int{200, 204}, BodyRegex: "ok"},
				Headers: map[string]registry.SecretRef{"X-Token": {Name: "s", Key: "k"}},
				Timeout: 5 * time.Second,
			},
		},
		{
			name: "a tool check with a JSON path condition",
			yaml: `{type: tool, source: signoz, tool: query_metrics, args: {query: rate, window: 5m}, condition: {jsonPath: "$.series[0].last", op: lt, value: 0.01}, noData: pass}`,
			want: ToolCheck{
				Source:    "signoz",
				Tool:      "query_metrics",
				Args:      map[string]any{"query": "rate", "window": "5m"},
				Condition: Condition{JSONPath: path, Op: OpLT, Value: "0.01"},
				NoData:    NoDataPass,
			},
		},
		{
			name: "a tool check with a text condition",
			yaml: `{type: tool, source: k8s, tool: get_workload, condition: {textRegex: "ready", negate: true}}`,
			want: ToolCheck{Source: "k8s", Tool: "get_workload", Condition: Condition{TextRegex: "ready", Negate: true}},
		},
		{
			name: "an alert check",
			yaml: `{type: alert, require: any}`,
			want: AlertCheck{Require: RequireAny},
		},
		{
			name:    "an unknown type",
			yaml:    `{type: ping}`,
			wantErr: `type: expected one of http, tool or alert, got "ping"`,
		},
		{
			name:    "a condition value that is a list",
			yaml:    `{type: tool, source: k8s, tool: t, condition: {jsonPath: "$.a", op: equals, value: [1]}}`,
			wantErr: "condition: value: must be a string, a number or a boolean",
		},
		{
			name:    "arguments that are not a mapping",
			yaml:    `{type: tool, source: k8s, tool: t, args: [1, 2]}`,
			wantErr: "args: expected a mapping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var c strategy.WatcherCheck
			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &c))

			got, err := CheckOf(c)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.NoError(t, got.Validate())
		})
	}
}

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		match     strategy.WatcherMatch
		alertName string
		text      string
		want      bool
	}{
		{name: "an empty match takes every event", want: true},
		{name: "an alert name glob", match: strategy.WatcherMatch{AlertName: []string{"*Down*"}}, alertName: "CheckoutDown", want: true},
		{name: "an alert name that no glob matches", match: strategy.WatcherMatch{AlertName: []string{"*Down*"}}, alertName: "HighLatency"},
		{name: "a text pattern", match: strategy.WatcherMatch{Text: "(?i)error rate"}, text: "Error rate is high", want: true},
		{
			name:      "both must match",
			match:     strategy.WatcherMatch{AlertName: []string{"*Down*"}, Text: "checkout"},
			alertName: "CheckoutDown",
			text:      "payments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, err := MatchOf(tt.match)
			require.NoError(t, err)
			require.Equal(t, tt.want, m.Matches(tt.alertName, tt.text))
		})
	}
}
