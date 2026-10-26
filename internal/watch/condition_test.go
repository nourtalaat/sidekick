package watch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/jsonpath"
)

// structured is the structured content the condition tests evaluate.
const structured = `{
  "status": "healthy",
  "ready": true,
  "replicas": 3,
  "lastError": null,
  "version": "1.10.2",
  "ratio": "0.004",
  "series": [{"name": "p99", "last": 0.004}, {"name": "p50", "last": 0.001}],
  "pods": [{"phase": "Running"}, {"phase": "Pending"}],
  "labels": {"app.kubernetes.io/name": "checkout"},
  "tags": ["blue", "green"],
  "matrix": [[1, 2], [30]]
}`

func TestConditionPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		op         Op
		value      Scalar
		content    string
		wantPass   bool
		wantDetail string
		wantErr    bool
	}{
		{name: "root exists", path: "$", op: OpExists, wantPass: true, wantDetail: "$ is "},
		{name: "field equals text", path: "$.status", op: OpEquals, value: "healthy", wantPass: true, wantDetail: `$.status is "healthy" (equals "healthy")`},
		{name: "field equals other text", path: "$.status", op: OpEquals, value: "down", wantDetail: `$.status is "healthy" (not equals "down")`},
		{name: "quoted field", path: `$.labels["app.kubernetes.io/name"]`, op: OpEquals, value: "checkout", wantPass: true},
		{name: "quoted field same as dot field", path: `$["status"]`, op: OpEquals, value: "healthy", wantPass: true},
		{name: "index", path: "$.series[0].last", op: OpLT, value: "0.01", wantPass: true, wantDetail: `$.series[0].last is 0.004 (lt "0.01")`},
		{name: "index past the end finds nothing", path: "$.series[5].last", op: OpExists, wantDetail: "$.series[5].last found nothing"},
		{name: "wildcard passes if any element passes", path: "$.pods[*].phase", op: OpEquals, value: "Pending", wantPass: true},
		{name: "wildcard fails when none pass", path: "$.pods[*].phase", op: OpEquals, value: "Failed", wantDetail: `none of the 2 values $.pods[*].phase found is equals "Failed"`},
		{name: "wildcard of wildcard", path: "$.matrix[*][*]", op: OpGT, value: "10", wantPass: true, wantDetail: `$.matrix[*][*] is 30 (gt "10")`},
		{name: "wildcard of an object finds nothing", path: "$.labels[*]", op: OpExists},
		{name: "missing field finds nothing", path: "$.missing", op: OpExists, wantDetail: "$.missing found nothing"},
		{name: "null exists", path: "$.lastError", op: OpExists, wantPass: true, wantDetail: "$.lastError is null (exists)"},
		{name: "null equals null", path: "$.lastError", op: OpEquals, value: "null", wantPass: true},
		{name: "no structured content", path: "$.status", op: OpExists, content: "-", wantDetail: "the result has no structured content"},
		{name: "structured content that is not JSON", path: "$.status", op: OpExists, content: "{", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content := json.RawMessage(structured)

			switch tt.content {
			case "":
			case "-":
				content = nil
			default:
				content = json.RawMessage(tt.content)
			}

			c, err := Condition{JSONPath: mustPath(t, tt.path), Op: tt.op, Value: tt.value}.compile()
			require.NoError(t, err)

			pass, detail, err := c.evaluate("", content)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantPass, pass)
			require.Contains(t, detail, tt.wantDetail)
		})
	}
}

func TestConditionOperators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		path  string
		op    Op
		value Scalar
		want  bool
	}{
		{name: "equals number", path: "$.replicas", op: OpEquals, value: "3", want: true},
		{name: "equals number in another form", path: "$.replicas", op: OpEquals, value: "3.0", want: true},
		{name: "equals boolean", path: "$.ready", op: OpEquals, value: "true", want: true},
		{name: "equals is case sensitive", path: "$.status", op: OpEquals, value: "Healthy", want: false},
		{name: "notEquals text", path: "$.status", op: OpNotEquals, value: "down", want: true},
		{name: "notEquals same text", path: "$.status", op: OpNotEquals, value: "healthy", want: false},
		{name: "notEquals number", path: "$.replicas", op: OpNotEquals, value: "3", want: false},
		{name: "notEquals empty value", path: "$.status", op: OpNotEquals, value: "", want: true},
		{name: "lt number", path: "$.replicas", op: OpLT, value: "4", want: true},
		{name: "lt equal number", path: "$.replicas", op: OpLT, value: "3", want: false},
		{name: "lte equal number", path: "$.replicas", op: OpLTE, value: "3", want: true},
		{name: "lte larger number", path: "$.replicas", op: OpLTE, value: "2", want: false},
		{name: "gt number", path: "$.replicas", op: OpGT, value: "2", want: true},
		{name: "gt equal number", path: "$.replicas", op: OpGT, value: "3", want: false},
		{name: "gte equal number", path: "$.replicas", op: OpGTE, value: "3", want: true},
		{name: "gte smaller number", path: "$.replicas", op: OpGTE, value: "4", want: false},
		{name: "numbers compare as numbers, not text", path: "$.replicas", op: OpLT, value: "10", want: true},
		{name: "text that holds a number compares as a number", path: "$.ratio", op: OpLT, value: "0.01", want: true},
		{name: "text compares as text", path: "$.version", op: OpLT, value: "1.9", want: true},
		{name: "a number against text compares as text", path: "$.replicas", op: OpLT, value: "abc", want: true},
		{name: "contains in text", path: "$.status", op: OpContains, value: "health", want: true},
		{name: "contains missing text", path: "$.status", op: OpContains, value: "sick", want: false},
		{name: "contains in an array as JSON", path: "$.tags", op: OpContains, value: `"green"`, want: true},
		{name: "contains in a number", path: "$.replicas", op: OpContains, value: "3", want: true},
		{name: "exists", path: "$.ready", op: OpExists, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := Condition{JSONPath: mustPath(t, tt.path), Op: tt.op, Value: tt.value}.compile()
			require.NoError(t, err)

			pass, _, err := c.evaluate("", json.RawMessage(structured))
			require.NoError(t, err)
			require.Equal(t, tt.want, pass)
		})
	}
}

func TestConditionTextRegex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		regex      string
		negate     bool
		text       string
		wantPass   bool
		wantDetail string
	}{
		{name: "match passes", regex: `(?i)\bstatus: healthy\b`, text: "Status: HEALTHY since 10:42", wantPass: true, wantDetail: "the text matches textRegex"},
		{name: "no match fails", regex: `(?i)\bstatus: healthy\b`, text: "status: degraded", wantDetail: "the text does not match textRegex"},
		{name: "negate passes without a match", regex: "CrashLoopBackOff", negate: true, text: "Running", wantPass: true, wantDetail: "does not match"},
		{name: "negate fails on a match", regex: "CrashLoopBackOff", negate: true, text: "pod-1 CrashLoopBackOff", wantDetail: "the text matches textRegex"},
		{name: "empty text", regex: "ok", text: "", wantDetail: "does not match"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := Condition{TextRegex: tt.regex, Negate: tt.negate}.compile()
			require.NoError(t, err)

			pass, detail, err := c.evaluate(tt.text, nil)
			require.NoError(t, err)
			require.Equal(t, tt.wantPass, pass)
			require.Contains(t, detail, tt.wantDetail)
		})
	}
}

func TestConditionValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cond    Condition
		wantErr string
	}{
		{name: "a path with an operator", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpLT, Value: "1"}},
		{name: "exists without a value", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpExists}},
		{name: "equals an empty value", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpEquals}},
		{name: "a regex", cond: Condition{TextRegex: "ok"}},
		{name: "a negated regex", cond: Condition{TextRegex: "ok", Negate: true}},
		{name: "nothing set", cond: Condition{}, wantErr: "set jsonPath with op, or textRegex"},
		{name: "both forms", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpExists, TextRegex: "ok"}, wantErr: "not both"},
		{name: "a path without an operator", cond: Condition{JSONPath: mustPath(t, "$.a")}, wantErr: "op: required"},
		{name: "an unknown operator", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: "below", Value: "1"}, wantErr: `unknown operator "below"; expected one of equals, notEquals, lt, lte, gt, gte, contains, exists`},
		{name: "exists with a value", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpExists, Value: "1"}, wantErr: "exists takes no value"},
		{name: "lt without a value", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpLT}, wantErr: "value: required with lt"},
		{name: "contains without a value", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpContains}, wantErr: "value: required with contains"},
		{name: "negate with a path", cond: Condition{JSONPath: mustPath(t, "$.a"), Op: OpExists, Negate: true}, wantErr: "negate goes with textRegex"},
		{name: "an operator with a regex", cond: Condition{TextRegex: "ok", Op: OpEquals}, wantErr: "op and value go with jsonPath"},
		{name: "a regex that does not compile", cond: Condition{TextRegex: "(unclosed"}, wantErr: "textRegex: error parsing regexp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cond.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConditionDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		json    string
		want    Condition
		wantErr string
	}{
		{
			name: "YAML with a number value",
			yaml: `{jsonPath: "$.series[0].last", op: lt, value: 0.01}`,
			want: Condition{JSONPath: mustPath(t, "$.series[0].last"), Op: OpLT, Value: "0.01"},
		},
		{
			name: "YAML with a regex",
			yaml: `{textRegex: "(?i)healthy", negate: true}`,
			want: Condition{TextRegex: "(?i)healthy", Negate: true},
		},
		{
			name: "JSON with a number value",
			json: `{"jsonPath": "$.series[0].last", "op": "lt", "value": 0.01}`,
			want: Condition{JSONPath: mustPath(t, "$.series[0].last"), Op: OpLT, Value: "0.01"},
		},
		{
			name: "JSON with a boolean value",
			json: `{"jsonPath": "$.ready", "op": "equals", "value": true}`,
			want: Condition{JSONPath: mustPath(t, "$.ready"), Op: OpEquals, Value: "true"},
		},
		{
			name: "JSON with a text value",
			json: `{"jsonPath": "$.status", "op": "equals", "value": "healthy"}`,
			want: Condition{JSONPath: mustPath(t, "$.status"), Op: OpEquals, Value: "healthy"},
		},
		{name: "JSON with an object value", json: `{"jsonPath": "$.a", "op": "equals", "value": {}}`, wantErr: "must be a string, a number or a boolean"},
		{name: "JSON with a bad path", json: `{"jsonPath": "a", "op": "exists"}`, wantErr: "must start with $"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got Condition

			var err error
			if tt.yaml != "" {
				err = yaml.Unmarshal([]byte(tt.yaml), &got)
			} else {
				err = json.Unmarshal([]byte(tt.json), &got)
			}

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			data, err := json.Marshal(got)
			require.NoError(t, err)

			var back Condition
			require.NoError(t, json.Unmarshal(data, &back))
			require.Equal(t, got, back, "a condition reads back from its JSON")
		})
	}
}

func TestShownValue(t *testing.T) {
	t.Parallel()

	long := make([]byte, 2*maxShownValue)
	for i := range long {
		long[i] = 'a'
	}

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "text is quoted", value: "a\nb", want: `"a\nb"`},
		{name: "long text is cut", value: string(long), want: `"` + string(long[:maxShownValue]) + `..."`},
		{name: "a number", value: json.Number("0.5"), want: "0.5"},
		{name: "an object", value: map[string]any{"a": true}, want: `{"a":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, shown(tt.value))
		})
	}
}

// mustPath parses a JSON path.
func mustPath(t *testing.T, s string) jsonpath.Path {
	t.Helper()

	p, err := jsonpath.Parse(s)
	require.NoError(t, err)

	return p
}
