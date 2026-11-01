//go:build integration

package summarize_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

const (
	testModel = "summarizer-1"
	testRoute = "default"
	toolName  = "submit_items"
)

// today is the day the tests run on, as spend_daily keeps it.
var (
	now   = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	today = pgtype.Date{Time: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Valid: true}
)

func TestCallTool(t *testing.T) {
	t.Parallel()

	usage := model.Usage{InputTokens: 2000, OutputTokens: 100}
	tool := model.ToolSpec{Name: toolName, Description: "Submit the items.", InputSchema: json.RawMessage(`{"type":"object"}`)}

	tests := []struct {
		name string
		// spent is seeded into spend_daily for the route before the call.
		spent  int64
		daily  summarize.Daily
		turns  []modeltest.Turn
		caps   model.Capabilities
		want   string
		wantIs error
		// wantCost is what the route's spend grew by.
		wantCost   int64
		wantChoice model.ToolChoice
	}{
		{
			name:       "the tool's input is returned and the call is priced",
			turns:      []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", toolName, `{"items":["a"]}`))},
			caps:       model.Capabilities{ToolCalling: true, ToolChoice: true},
			want:       `{"items":["a"]}`,
			wantCost:   2500,
			wantChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: toolName},
		},
		{
			name:       "a connector that ignores a forced choice gets auto",
			turns:      []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", toolName, `{}`))},
			caps:       model.Capabilities{ToolCalling: true},
			want:       `{}`,
			wantCost:   2500,
			wantChoice: model.ToolChoice{Mode: model.ToolChoiceAuto},
		},
		{
			name:       "an answer without the tool call is an error that still counts",
			turns:      []modeltest.Turn{modeltest.Text(usage, "I will not use the tool")},
			caps:       model.Capabilities{ToolCalling: true, ToolChoice: true},
			wantIs:     summarize.ErrNoToolCall,
			wantCost:   2500,
			wantChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: toolName},
		},
		{
			name:       "a failed call costs nothing",
			turns:      []modeltest.Turn{modeltest.Fail(errProviderDown)},
			caps:       model.Capabilities{ToolCalling: true, ToolChoice: true},
			wantIs:     errProviderDown,
			wantChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: toolName},
		},
		{
			name:   "the route's cap is reached",
			spent:  1000,
			daily:  summarize.Daily{RouteMicros: map[string]int64{testRoute: 1000}},
			caps:   model.Capabilities{ToolCalling: true, ToolChoice: true},
			wantIs: summarize.ErrOverCap,
		},
		{
			name:   "the global cap is reached",
			spent:  5000,
			daily:  summarize.Daily{MaxCostMicros: 5000},
			caps:   model.Capabilities{ToolCalling: true, ToolChoice: true},
			wantIs: summarize.ErrOverCap,
		},
		{
			name:       "under the caps the call runs",
			spent:      999,
			daily:      summarize.Daily{MaxCostMicros: 5000, RouteMicros: map[string]int64{testRoute: 1000}},
			turns:      []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", toolName, `{}`))},
			caps:       model.Capabilities{ToolCalling: true, ToolChoice: true},
			want:       `{}`,
			wantCost:   2500,
			wantChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: toolName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			queries := db.New(pool)

			if tt.spent > 0 {
				require.NoError(t, queries.AddSpend(t.Context(), db.AddSpendParams{Day: today, Route: testRoute, Connector: "other", Model: "m", CostMicros: tt.spent}))
			}

			conn := modeltest.New(t, tt.caps, tt.turns...)

			caller := summarize.New(summarize.Options{
				Name: "summarizer", Connector: conn, Model: testModel, Daily: tt.daily,
				Prices: pricing.New(map[string]model.Price{testModel: {InputPerMTok: 1_000_000, OutputPerMTok: 5_000_000}}),
				Now:    func() time.Time { return now },
			})

			got, err := caller.CallTool(t.Context(), pool, summarize.Request{
				Route: testRoute, System: "Draft items.", Tool: tool, MaxTokens: 500,
				Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "the cause"}}}},
			})

			spend, spendErr := queries.GetDailySpend(t.Context(), db.GetDailySpendParams{Route: testRoute, Day: today})
			require.NoError(t, spendErr)
			require.Equal(t, tt.spent+tt.wantCost, spend.RouteMicros)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			} else {
				require.NoError(t, err)
				require.JSONEq(t, tt.want, string(got))
			}

			if len(tt.turns) == 0 {
				return
			}

			reqs := conn.Requests()
			require.Len(t, reqs, 1)
			require.Equal(t, tt.wantChoice, reqs[0].ToolChoice)
			require.Equal(t, []model.ToolSpec{tool}, reqs[0].Tools)
			require.Equal(t, string(model.RoleSummarize), reqs[0].Metadata[model.MetadataRole])
			require.Equal(t, testRoute, reqs[0].Metadata[model.MetadataRoute])
		})
	}
}

// errProviderDown stands for the error the failing turn returns.
var errProviderDown = errors.New("provider down")
