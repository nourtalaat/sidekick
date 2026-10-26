package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestExpose(t *testing.T) {
	t.Parallel()

	tool := func(name string, readOnly bool) source.Tool {
		return source.Tool{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: readOnly}
	}

	long := strings.Repeat("x", source.MaxToolNameLen)

	tests := []struct {
		name        string
		settings    source.ToolSettings
		tools       []source.Tool
		wantExposed []string
		wantDropped []Dropped
	}{
		{
			name:        "no filter exposes every read-only tool",
			tools:       []source.Tool{tool("search_logs", true), tool("query_metrics", true)},
			wantExposed: []string{"signoz__search_logs", "signoz__query_metrics"},
		},
		{
			name:        "allow",
			settings:    source.ToolSettings{Filter: source.Filter{Allow: []string{"search_*"}}},
			tools:       []source.Tool{tool("search_logs", true), tool("query_metrics", true)},
			wantExposed: []string{"signoz__search_logs"},
			wantDropped: []Dropped{{Tool: "query_metrics", Reason: DropNotAllowed, Detail: "no tools.allow glob matches it"}},
		},
		{
			name:        "deny wins over allow",
			settings:    source.ToolSettings{Filter: source.Filter{Allow: []string{"*"}, Deny: []string{"*delete*"}}},
			tools:       []source.Tool{tool("search_logs", true), tool("delete_alert", true)},
			wantExposed: []string{"signoz__search_logs"},
			wantDropped: []Dropped{{Tool: "delete_alert", Reason: DropDenied, Detail: "a tools.deny glob matches it"}},
		},
		{
			name:        "writing tools are dropped even when allowed",
			settings:    source.ToolSettings{Filter: source.Filter{Allow: []string{"create_alert", "search_logs"}}},
			tools:       []source.Tool{tool("search_logs", true), tool("create_alert", false)},
			wantExposed: []string{"signoz__search_logs"},
			wantDropped: []Dropped{{Tool: "create_alert", Reason: DropWrites, Detail: "the source marks it as a tool that can change something"}},
		},
		{
			name:        "rename",
			settings:    source.ToolSettings{Rename: map[string]string{"signoz_search_logs": "logs"}},
			tools:       []source.Tool{tool("signoz_search_logs", true)},
			wantExposed: []string{"signoz__logs"},
		},
		{
			name:        "globs match the source's own name, not the new one",
			settings:    source.ToolSettings{Filter: source.Filter{Allow: []string{"signoz_*"}}, Rename: map[string]string{"signoz_search_logs": "logs"}},
			tools:       []source.Tool{tool("signoz_search_logs", true)},
			wantExposed: []string{"signoz__logs"},
		},
		{
			name:  "a name over 64 characters is dropped",
			tools: []source.Tool{tool(long, true)},
			wantDropped: []Dropped{{
				Tool:   long,
				Reason: DropBadName,
				Detail: `tool "signoz__` + long + `" is longer than 64 characters: invalid name; rename it with tools.rename`,
			}},
		},
		{
			name:        "a long name renamed to fit is exposed",
			settings:    source.ToolSettings{Rename: map[string]string{long: "short"}},
			tools:       []source.Tool{tool(long, true)},
			wantExposed: []string{"signoz__short"},
		},
		{
			name:        "a name with characters providers refuse is dropped",
			tools:       []source.Tool{tool("search.logs", true)},
			wantDropped: []Dropped{{Tool: "search.logs", Reason: DropBadName, Detail: `tool "search.logs" of source "signoz": invalid name; rename it with tools.rename`}},
		},
		{
			name:        "a rename onto another tool's name drops the second",
			settings:    source.ToolSettings{Rename: map[string]string{"old_logs": "logs"}},
			tools:       []source.Tool{tool("logs", true), tool("old_logs", true)},
			wantExposed: []string{"signoz__logs"},
			wantDropped: []Dropped{{Tool: "old_logs", Reason: DropDuplicate, Detail: "another tool is already exposed as signoz__logs"}},
		},
		{
			name:        "a schema that cannot be parsed is dropped",
			tools:       []source.Tool{{Name: "logs", InputSchema: json.RawMessage(`{"type":`), ReadOnly: true}},
			wantDropped: []Dropped{{Tool: "logs", Reason: DropBadSchema, Detail: "parse input schema: unexpected end of JSON input"}},
		},
		{
			name:        "no schema accepts any input",
			tools:       []source.Tool{{Name: "logs", ReadOnly: true}},
			wantExposed: []string{"signoz__logs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exposed, dropped := Expose("signoz", source.Settings{Tools: tt.settings}, tt.tools)

			names := []string{}
			for _, e := range exposed {
				names = append(names, e.Name)

				require.NotNil(t, e.schema)
			}

			require.Equal(t, append([]string{}, tt.wantExposed...), names)
			require.Equal(t, tt.wantDropped, dropped)
		})
	}
}
