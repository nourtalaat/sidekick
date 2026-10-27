package mock

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// testSnapshot lists search_logs and query_metrics.
const testSnapshot = "fixture/tools.json"

// call is one call a test makes and the answer it expects.
type call struct {
	tool  string
	input string
	want  source.Result
	// wantToolError is the message of the tool error the call returns
	// instead of a result.
	wantToolError string
}

// text returns a result with text only.
func text(s string) source.Result {
	return source.Result{Text: s}
}

// noData is the result of a call no fixture matches with the default
// default.
var noData = source.Result{Text: NoDataText, NoData: true}

func TestCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cfg   Config
		calls []call
	}{
		{
			name: "fixtures are tried in file order and the first match wins",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "search_logs", Match: args("query", Matcher{Regex: "pool"}), Respond: &Response{Text: "first"}},
				{Tool: "search_logs", Respond: &Response{Text: "second"}},
			}},
			calls: []call{
				{tool: "search_logs", input: `{"query": "pool timeout"}`, want: text("first")},
				{tool: "search_logs", input: `{"query": "errors"}`, want: text("second")},
			},
		},
		{
			name: "responses come in order and the last one repeats",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "search_logs", Responses: []Response{{Text: "one"}, {Text: "two"}, {NoData: true}}},
			}},
			calls: []call{
				{tool: "search_logs", input: `{}`, want: text("one")},
				{tool: "search_logs", input: `{}`, want: text("two")},
				{tool: "search_logs", input: `{}`, want: noData},
				{tool: "search_logs", input: `{}`, want: noData},
			},
		},
		{
			name: "each fixture counts its own calls",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "search_logs", Responses: []Response{{Text: "logs 1"}, {Text: "logs 2"}}},
				{Tool: "query_metrics", Responses: []Response{{Text: "metrics 1"}, {Text: "metrics 2"}}},
			}},
			calls: []call{
				{tool: "search_logs", input: `{}`, want: text("logs 1")},
				{tool: "query_metrics", input: `{}`, want: text("metrics 1")},
				{tool: "search_logs", input: `{}`, want: text("logs 2")},
				{tool: "query_metrics", input: `{}`, want: text("metrics 2")},
			},
		},
		{
			name: "a call that does not match does not count",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "search_logs", Match: args("query", Matcher{Equals: "pool"}), Responses: []Response{{Text: "one"}, {Text: "two"}}},
			}},
			calls: []call{
				{tool: "search_logs", input: `{"query": "other"}`, want: noData},
				{tool: "search_logs", input: `{"query": "pool"}`, want: text("one")},
				{tool: "search_logs", input: `{"query": "pool"}`, want: text("two")},
			},
		},
		{
			name: "respond answers every matching call the same",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "search_logs", Respond: &Response{
					Text:     "10:10:04 ERROR timeout",
					Evidence: []Evidence{{Kind: source.EvidenceLogQuery, Title: "Logs", URL: "https://logs.example/q"}},
				}},
			}},
			calls: []call{
				{tool: "search_logs", input: `{}`, want: source.Result{
					Text:     "10:10:04 ERROR timeout",
					Evidence: []source.Evidence{{Kind: source.EvidenceLogQuery, Title: "Logs", URL: "https://logs.example/q"}},
				}},
				{tool: "search_logs", input: `{}`, want: source.Result{
					Text:     "10:10:04 ERROR timeout",
					Evidence: []source.Evidence{{Kind: source.EvidenceLogQuery, Title: "Logs", URL: "https://logs.example/q"}},
				}},
			},
		},
		{
			name: "arguments the fixture does not name are ignored",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "search_logs", Match: args("service", Matcher{Equals: "checkout-api"}), Respond: &Response{Text: "found"}},
			}},
			calls: []call{
				{tool: "search_logs", input: `{"service": "checkout-api", "query": "anything"}`, want: text("found")},
			},
		},
		{
			name: "strict fails on arguments the fixture does not name",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "search_logs", Strict: true, Match: args("service", Matcher{Equals: "checkout-api"}), Respond: &Response{Text: "found"}},
			}},
			calls: []call{
				{tool: "search_logs", input: `{"service": "checkout-api", "query": "anything"}`, want: noData},
				{tool: "search_logs", input: `{"service": "checkout-api"}`, want: text("found")},
			},
		},
		{
			name: "tool accepts globs",
			cfg: Config{Fixtures: []Fixture{
				{Tool: "*_metrics", Respond: &Response{Text: "metrics"}},
				{Tool: "*", Respond: &Response{Text: "anything"}},
			}},
			calls: []call{
				{tool: "query_metrics", input: `{}`, want: text("metrics")},
				{tool: "search_logs", input: `{}`, want: text("anything")},
			},
		},
		{
			name: "within counts from the scenario clock",
			cfg: Config{Now: clock, Fixtures: []Fixture{
				{Tool: "query_metrics", Match: args("since", Matcher{Within: 2 * time.Hour}), Respond: &Response{Text: "recent"}},
			}},
			calls: []call{
				{tool: "query_metrics", input: `{"since": "2026-05-04T09:00:00Z"}`, want: text("recent")},
				{tool: "query_metrics", input: `{"since": "2026-05-03T09:00:00Z"}`, want: noData},
			},
		},
		{
			name: "no match returns no data by default",
			cfg:  Config{},
			calls: []call{
				{tool: "search_logs", input: `{"query": "x"}`, want: noData},
			},
		},
		{
			name: "no match returns a tool error with default error",
			cfg:  Config{Default: DefaultError},
			calls: []call{
				{tool: "search_logs", input: `{"query": "x"}`, wantToolError: noMatchMessage},
			},
		},
		{
			name: "an input that is not an object matches nothing",
			cfg:  Config{Fixtures: []Fixture{{Tool: "search_logs", Respond: &Response{Text: "found"}}}},
			calls: []call{
				{tool: "search_logs", input: `[1]`, want: noData},
				{tool: "search_logs", input: ``, want: text("found")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.cfg.Tools.Snapshot = testSnapshot
			require.NoError(t, tt.cfg.Validate())

			src, err := New(t.Context(), "signoz", tt.cfg, registry.Deps{})
			require.NoError(t, err)

			for i, c := range tt.calls {
				got, err := src.Call(t.Context(), source.Call{ID: "c1", Tool: c.tool, Input: json.RawMessage(c.input)})

				if c.wantToolError != "" {
					var terr *source.ToolError
					require.ErrorAs(t, err, &terr, "call %d", i)
					require.Equal(t, c.wantToolError, terr.Message, "call %d", i)

					continue
				}

				require.NoError(t, err, "call %d", i)
				require.Equal(t, c.want, got, "call %d", i)
			}
		})
	}
}

func TestTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		// want are the names of the listed tools; nil means listing fails
		// as unavailable.
		want []string
	}{
		{name: "the snapshot's tools", cfg: Config{Tools: Tools{Snapshot: testSnapshot}}, want: []string{"search_logs", "query_metrics"}},
		{name: "unavailable with a snapshot", cfg: Config{Tools: Tools{Snapshot: testSnapshot}, Unavailable: true}},
		{name: "unavailable without a snapshot", cfg: Config{Unavailable: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, tt.cfg.Validate())

			src, err := New(t.Context(), "signoz", tt.cfg, registry.Deps{})
			require.NoError(t, err)

			tools, err := src.Tools(t.Context())
			if tt.want == nil {
				require.ErrorIs(t, err, source.ErrUnavailable)

				_, err = src.Call(t.Context(), source.Call{Tool: "search_logs", Input: json.RawMessage(`{}`)})
				require.ErrorIs(t, err, source.ErrUnavailable, "every call fails too")

				return
			}

			require.NoError(t, err)

			names := make([]string, 0, len(tools))
			for _, tool := range tools {
				require.True(t, tool.ReadOnly)
				require.NotEmpty(t, tool.InputSchema)

				names = append(names, tool.Name)
			}

			require.Equal(t, tt.want, names)
		})
	}
}

func TestNewErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "missing snapshot", cfg: Config{Tools: Tools{Snapshot: "fixture/missing.json"}}, wantErr: "tools.snapshot: read:"},
		{name: "snapshot that is not JSON", cfg: Config{Tools: Tools{Snapshot: "mock.go"}}, wantErr: "tools.snapshot: decode"},
		{
			name:    "fixture tool that matches no tool",
			cfg:     Config{Tools: Tools{Snapshot: testSnapshot}, Fixtures: []Fixture{{Tool: "get_pod", Respond: &Response{}}}},
			wantErr: `fixtures[0].tool: "get_pod" matches no tool of the snapshot`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(t.Context(), "signoz", tt.cfg, registry.Deps{})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		// wantErr is a part of the error; empty means valid.
		wantErr string
	}{
		{name: "valid", cfg: Config{Tools: Tools{Snapshot: testSnapshot}, Fixtures: []Fixture{{Tool: "*", Respond: &Response{}}}}},
		{name: "unavailable needs no snapshot", cfg: Config{Unavailable: true}},
		{name: "no snapshot", cfg: Config{}, wantErr: "tools.snapshot: required unless unavailable is true"},
		{name: "unknown default", cfg: Config{Unavailable: true, Default: "empty"}, wantErr: `default: must be no_data or error, not "empty"`},
		{
			name:    "every problem keeps its path",
			cfg:     Config{Unavailable: true, Fixtures: []Fixture{{}}},
			wantErr: "fixtures[0].tool: required\nfixtures[0].respond: set respond or responses",
		},
		{name: "no tool", cfg: Config{Unavailable: true, Fixtures: []Fixture{{Respond: &Response{}}}}, wantErr: "fixtures[0].tool: required"},
		{name: "bad glob", cfg: Config{Unavailable: true, Fixtures: []Fixture{{Tool: "[", Respond: &Response{}}}}, wantErr: "fixtures[0].tool: invalid glob"},
		{name: "no response", cfg: Config{Unavailable: true, Fixtures: []Fixture{{Tool: "a"}}}, wantErr: "fixtures[0].respond: set respond or responses"},
		{
			name:    "both responses",
			cfg:     Config{Unavailable: true, Fixtures: []Fixture{{Tool: "a", Respond: &Response{}, Responses: []Response{{}}}}},
			wantErr: "fixtures[0].respond: set respond or responses, not both",
		},
		{
			name:    "matcher without operators",
			cfg:     Config{Unavailable: true, Fixtures: []Fixture{{Tool: "a", Match: args("q", Matcher{}), Respond: &Response{}}}},
			wantErr: "fixtures[0].match.args.q: set at least one of",
		},
		{
			name:    "bad regex",
			cfg:     Config{Unavailable: true, Fixtures: []Fixture{{Tool: "a", Match: args("q", Matcher{Regex: "("}), Respond: &Response{}}}},
			wantErr: "fixtures[0].match.args.q: regex:",
		},
		{
			name:    "empty oneOf",
			cfg:     Config{Unavailable: true, Fixtures: []Fixture{{Tool: "a", Match: args("q", Matcher{OneOf: []any{}}), Respond: &Response{}}}},
			wantErr: "fixtures[0].match.args.q: oneOf: must not be empty",
		},
		{
			name: "unknown evidence kind",
			cfg: Config{Unavailable: true, Fixtures: []Fixture{{Tool: "a", Responses: []Response{
				{}, {Evidence: []Evidence{{Kind: "graph", Title: "x"}}},
			}}}},
			wantErr: `fixtures[0].responses[1].evidence[0].kind: unknown kind "graph"`,
		},
		{
			name:    "evidence without a title",
			cfg:     Config{Unavailable: true, Fixtures: []Fixture{{Tool: "a", Respond: &Response{Evidence: []Evidence{{Kind: source.EvidenceCommit}}}}}},
			wantErr: "fixtures[0].respond.evidence[0].title: required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
		want    source.Result
	}{
		{
			name: "a section as a scenario writes it",
			section: `
tools: { snapshot: fixture/tools.json }
fixtures:
  - tool: search_logs
    match:
      args:
        query: { regex: "(?i)pool|timeout" }
        limit: { gte: 10, lte: 100 }
    respond:
      text: "10:10:04 ERROR pq: timeout acquiring connection from pool (size=5)"
      evidence:
        - { kind: log_query, title: pool errors, url: "https://logs.example/q" }
`,
			want: source.Result{
				Text:     "10:10:04 ERROR pq: timeout acquiring connection from pool (size=5)",
				Evidence: []source.Evidence{{Kind: source.EvidenceLogQuery, Title: "pool errors", URL: "https://logs.example/q"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.section), &node))

			src, err := registry.Sources.Build(t.Context(), Type, "signoz", &node, registry.Deps{})
			require.NoError(t, err)

			got, err := src.Call(t.Context(), source.Call{Tool: "search_logs", Input: json.RawMessage(`{"query": "Pool size", "limit": 20}`)})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// args returns a Match with one argument matcher.
func args(name string, m Matcher) Match {
	return Match{Args: map[string]Matcher{name: m}}
}
