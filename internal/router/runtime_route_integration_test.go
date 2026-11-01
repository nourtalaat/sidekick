//go:build integration

package router

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// runtimeActor is the Slack user who saves the runtime route.
var runtimeActor = rules.SlackUser("U01ADMIN")

// TestInvestigationRunsOnARuntimeRoute saves a route at runtime, routes an
// event to it, and runs the investigation to a verdict with the runtime
// route's sources and outputs.
func TestInvestigationRunsOnARuntimeRoute(t *testing.T) {
	t.Parallel()

	cfg, instances := loadConfig(t)
	cfg.Sources = map[string]config.Section{"signoz": {Type: "mcp"}, "loki": {Type: "mcp"}}

	h := setupWith(t, cfg, instances)

	route := config.NewRoute("runtime-info")
	route.Priority = 1
	route.Match = &config.Match{Severity: []event.Severity{event.SeverityInfo}}
	route.Sources = []string{"signoz"}
	route.Outputs = []string{"callback"}

	_, err := rules.NewStore(h.pool, cfg).CreateRoute(t.Context(), runtimeActor, route)
	require.NoError(t, err)

	cache := rules.NewCache(t.Context(), rules.Options{File: cfg, Pool: h.pool})
	require.NoError(t, cache.Reload(t.Context()))
	require.NoError(t, h.router.SetRoutes(cache.Current().RouteConfigs()))

	require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, otherEvent)))

	inv := h.investigations(t)[0]
	require.Equal(t, "runtime-info", inv.Route, "the event takes the runtime route")

	states := collect[db.PublishState](t, h.pool, "SELECT * FROM publish_states")
	require.Len(t, states, 1)
	require.Equal(t, []string{"callback"}, states[0].Sinks)

	conn := modeltest.New(t, model.Capabilities{ToolCalling: true},
		modeltest.ToolUse(model.Usage{}, modeltest.Call("v1", report.SubmitVerdictTool, inconclusiveReport)),
	)
	conn.ExpectAllUsed()

	routes := investigator.ConfigRoutes(func() []config.Route { return cache.Current().RouteConfigs() })
	require.NoError(t, h.investigateOn(t, conn, twoSourceGuard(t), routes))

	inv = h.investigations(t)[0]
	require.Equal(t, investigator.StatusFinal, inv.Status)
	require.Equal(t, new(string(report.StopSubmitted)), inv.StopReason)
	require.NotNil(t, inv.Verdict)

	requests := conn.Requests()
	require.Len(t, requests, 1)

	signoz, err := source.QualifiedName("signoz", "search_logs")
	require.NoError(t, err)
	require.Equal(t, []string{signoz}, sourceToolNames(t, requests[0]), "only the runtime route's source is offered")
}

// sourceToolNames returns the names of the signoz and loki tools in req.
func sourceToolNames(t *testing.T, req model.Request) []string {
	t.Helper()

	signoz, err := source.QualifiedName("signoz", "search_logs")
	require.NoError(t, err)

	loki, err := source.QualifiedName("loki", "query_loki")
	require.NoError(t, err)

	var names []string

	for _, spec := range req.Tools {
		if spec.Name == signoz || spec.Name == loki {
			names = append(names, spec.Name)
		}
	}

	return names
}

// twoSourceGuard returns a guard with two sources: signoz, whose one tool
// is search_logs, and loki, whose one tool is query_loki.
func twoSourceGuard(t *testing.T) *tools.Guard {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	guard, err := tools.New([]tools.Source{
		{Name: "signoz", Source: toolSource(t, "search_logs")},
		{Name: "loki", Source: toolSource(t, "query_loki")},
	}, tools.Options{Redactor: redactor, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	return guard
}

// toolSource returns a source with one read-only tool named name.
func toolSource(t *testing.T, name string) source.Source {
	t.Helper()

	src := source.NewMockSource(gomock.NewController(t))
	src.EXPECT().Tools(gomock.Any()).Return([]source.Tool{{
		Name:        name,
		Description: "Search.",
		InputSchema: json.RawMessage(`{"type": "object"}`),
		ReadOnly:    true,
	}}, nil).AnyTimes()
	src.EXPECT().Call(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, source.Call) (source.Result, error) {
		return source.Result{Text: "nothing found"}, nil
	}).AnyTimes()

	return src
}
