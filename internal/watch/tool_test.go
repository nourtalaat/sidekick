package watch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/jsonpath"
	"github.com/nourtalaat/sidekick/internal/source"
)

// queryTool is the tool of the mocked source.
var queryTool = source.Tool{
	Name:        "query",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	ReadOnly:    true,
}

// writeTool is a tool that can change something.
var writeTool = source.Tool{Name: "restart", ReadOnly: false}

// healthy is the condition most cases use.
var healthy = Condition{JSONPath: jsonPathOf("$.series[0].last"), Op: OpLT, Value: "0.01"}

func TestToolCheck(t *testing.T) {
	t.Parallel()

	upArgs := map[string]any{"query": "up"}

	tests := []struct {
		name       string
		check      ToolCheck
		settings   source.Settings
		setupMocks func(m *source.MockSource)
		wantResult Result
		wantDetail string
		wantNot    string
	}{
		{
			name:  "the condition passes",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, c source.Call) (source.Result, error) {
					if c.Tool != "query" || string(c.Input) != `{"query":"up"}` || c.InvestigationID != "inv-1" || !strings.HasPrefix(c.ID, toolCallPrefix) {
						return source.Result{}, errors.New("unexpected call")
					}

					return source.Result{Structured: json.RawMessage(`{"series":[{"last":0.004}]}`)}, nil
				})
			},
			wantResult: Pass, wantDetail: `$.series[0].last is 0.004 (lt "0.01")`,
		},
		{
			name:  "the condition fails",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{Structured: json.RawMessage(`{"series":[{"last":0.2}]}`)}, nil)
			},
			wantResult: Fail, wantDetail: `$.series[0].last is 0.2 (not lt "0.01")`,
		},
		{
			name:  "a path that finds nothing fails",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{Structured: json.RawMessage(`{"series":[]}`)}, nil)
			},
			wantResult: Fail, wantDetail: "$.series[0].last found nothing",
		},
		{
			name:  "a text regex on the redacted text, without the untrusted data markers",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: Condition{TextRegex: `^status: healthy, owner \[REDACTED:`}},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{Text: "status: healthy, owner ops@example.com"}, nil)
			},
			wantResult: Pass, wantDetail: "the text matches textRegex",
		},
		{
			name:  "a negated text regex",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: Condition{TextRegex: "CrashLoopBackOff", Negate: true}},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{Text: "pod-1 CrashLoopBackOff"}, nil)
			},
			wantResult: Fail, wantDetail: "the text matches textRegex",
		},
		{
			name:  "no data fails by default",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{NoData: true}, nil)
			},
			wantResult: Fail, wantDetail: "the tool found no data",
		},
		{
			name:  "no data fails with noData: fail",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy, NoData: NoDataFail},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{NoData: true}, nil)
			},
			wantResult: Fail, wantDetail: "the tool found no data",
		},
		{
			name:  "no data passes with noData: pass",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy, NoData: NoDataPass},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{NoData: true}, nil)
			},
			wantResult: Pass, wantDetail: "the tool found no data, which noData: pass accepts",
		},
		{
			name:     "a renamed tool is found by the source's own name",
			check:    ToolCheck{Tool: "query", Args: upArgs, Condition: Condition{JSONPath: jsonPathOf("$.ok"), Op: OpEquals, Value: "true"}},
			settings: source.Settings{Tools: source.ToolSettings{Rename: map[string]string{"query": "promql"}}},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{Structured: json.RawMessage(`{"ok":true}`)}, nil)
			},
			wantResult: Pass,
		},
		{
			name:  "no arguments send an empty object",
			check: ToolCheck{Tool: "status", Condition: Condition{TextRegex: "up"}},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{{Name: "status", ReadOnly: true}}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, c source.Call) (source.Result, error) {
					if string(c.Input) != "{}" {
						return source.Result{}, errors.New("unexpected input " + string(c.Input))
					}

					return source.Result{Text: "up"}, nil
				})
			},
			wantResult: Pass,
		},
		{
			name:  "a tool error",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{}, &source.ToolError{Message: "parse error near ops@example.com"})
			},
			wantResult: Error, wantDetail: "tool_error prom__query: parse error near [REDACTED:", wantNot: "ops@example.com",
		},
		{
			name:  "arguments that do not match the schema",
			check: ToolCheck{Tool: "query", Args: map[string]any{"q": "up"}, Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
			},
			wantResult: Error, wantDetail: "invalid_input prom__query: the input does not match the tool's schema",
		},
		{
			name:  "a source that is not connected",
			check: ToolCheck{Tool: "query", Args: upArgs, Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return(nil, source.ErrUnavailable)
			},
			wantResult: Error, wantDetail: "the source prom cannot be used: the source is not connected",
		},
		{
			name:  "a tool that can change something",
			check: ToolCheck{Tool: "restart", Condition: healthy},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool, writeTool}, nil)
			},
			wantResult: Error, wantDetail: "the source prom has no tool restart that checks may call",
		},
		{
			name:     "a tool the source's filter denies",
			check:    ToolCheck{Tool: "query", Args: upArgs, Condition: healthy},
			settings: source.Settings{Tools: source.ToolSettings{Filter: source.Filter{Deny: []string{"query"}}}},
			setupMocks: func(m *source.MockSource) {
				m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{queryTool}, nil)
			},
			wantResult: Error, wantDetail: "the source prom has no tool query that checks may call",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			m := source.NewMockSource(ctrl)
			tt.setupMocks(m)

			c := newChecker(t, testDeps{sources: []tools.Source{{Name: "prom", Source: m, Settings: tt.settings}}})

			check := tt.check
			check.Source = "prom"

			o := c.Run(t.Context(), Target{InvestigationID: "inv-1"}, check)
			require.Equal(t, tt.wantResult, o.Result, o.Detail)
			require.Contains(t, o.Detail, tt.wantDetail)

			if tt.wantNot != "" {
				require.NotContains(t, o.Detail, tt.wantNot)
			}
		})
	}
}

func TestToolCheckUnknownSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantDetail string
	}{
		{name: "a source that is not configured", wantDetail: "the source loki cannot be used: the source is not configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newChecker(t, testDeps{})

			o := c.Run(t.Context(), Target{}, ToolCheck{Source: "loki", Tool: "query", Condition: healthy})
			require.Equal(t, Error, o.Result)
			require.Equal(t, tt.wantDetail, o.Detail)
		})
	}
}

func TestToolCheckValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		check   ToolCheck
		wantErr []string
	}{
		{name: "a full check", check: ToolCheck{Source: "prom", Tool: "query", Args: map[string]any{"query": "up"}, Condition: healthy, NoData: NoDataPass}},
		{name: "nothing set", check: ToolCheck{}, wantErr: []string{"source: required", "tool: required", "condition: set jsonPath with op, or textRegex"}},
		{name: "a bad condition", check: ToolCheck{Source: "prom", Tool: "query", Condition: Condition{TextRegex: "("}}, wantErr: []string{"condition: textRegex: error parsing regexp"}},
		{name: "a bad noData", check: ToolCheck{Source: "prom", Tool: "query", Condition: healthy, NoData: "skip"}, wantErr: []string{`noData: expected fail or pass, got "skip"`}},
		{name: "args that do not encode", check: ToolCheck{Source: "prom", Tool: "query", Condition: healthy, Args: map[string]any{"f": func() {}}}, wantErr: []string{"args: json: unsupported type"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.check.Validate()
			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.Len(t, strings.Split(err.Error(), "\n"), len(tt.wantErr))

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

// jsonPathOf parses a JSON path that is known to be valid.
func jsonPathOf(s string) jsonpath.Path {
	p, err := jsonpath.Parse(s)
	if err != nil {
		panic(err)
	}

	return p
}
