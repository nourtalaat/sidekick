//go:build integration

package investigator

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/source"
)

// replicaDeadline is how soon a replica must use a route that another
// replica saved. The caches reload every hour, so only the notification
// can make them in time.
const replicaDeadline = 5 * time.Second

// Source names and their one tool each, in the test routes.
const (
	signozSource = "signoz"
	lokiSource   = "loki"
	signozTool   = "search_logs"
	lokiTool     = "query_loki"
)

// replicaVerdict is a report that needs no evidence.
const replicaVerdict = `{"verdict": "inconclusive", "confidence": "low", "reason": "Too little data.", "summary": "Not sure yet.", "findings": []}`

var routeActor = rules.SlackUser("U01ADMIN")

// TestARouteSavedOnAnotherReplicaIsUsed saves and then edits a runtime route
// through the store of replica A, and checks that the investigator of
// replica B uses each version for its next investigation.
func TestARouteSavedOnAnotherReplicaIsUsed(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, replicaVerdict))}})
	storeA, cacheB := replicas(t, h)

	savePayments(t, storeA, []string{lokiSource})

	requireRoute(t, h, cacheB, []string{lokiSource})

	require.NoError(t, h.run(t))
	require.Equal(t, StatusFinal, h.investigation(t).Status)
	require.Equal(t, []string{qualified(t, lokiSource, lokiTool)}, sourceToolNames(t, h.conn.Requests()[0]), "the saved route's sources")

	savePayments(t, storeA, []string{signozSource})

	requireRoute(t, h, cacheB, []string{signozSource})
}

// TestARunKeepsTheRouteItStartedWith edits the route while a run is going
// and checks that the run still offers the sources it started with.
func TestARunKeepsTheRouteItStartedWith(t *testing.T) {
	t.Parallel()

	var (
		storeA *rules.Store
		cacheB *rules.Cache
		h      *harness
	)

	editRoute := func(context.Context, model.Request) (model.Response, error) {
		savePayments(t, storeA, []string{signozSource})
		requireRoute(t, h, cacheB, []string{signozSource})

		return modeltest.ToolUse(testUsage, modeltest.Call("f1", RecordFindingTool, `{"claim": "Errors rose.", "evidenceToolCallIds": []}`)).Response, nil
	}

	h = setup(t, options{turns: []modeltest.Turn{
		{Func: editRoute},
		turn(modeltest.Call("v1", report.SubmitVerdictTool, replicaVerdict)),
	}})
	storeA, cacheB = replicas(t, h)

	savePayments(t, storeA, []string{lokiSource})
	requireRoute(t, h, cacheB, []string{lokiSource})

	require.NoError(t, h.run(t))
	require.Equal(t, StatusFinal, h.investigation(t).Status)

	want := []string{qualified(t, lokiSource, lokiTool)}
	for i, req := range h.conn.Requests() {
		require.Equal(t, want, sourceToolNames(t, req), "turn %d", i+1)
	}
}

// TestAnInvestigationOfADeletedRouteGivesUp deletes the route of a queued
// investigation and checks that the run gives up and says why.
func TestAnInvestigationOfADeletedRouteGivesUp(t *testing.T) {
	t.Parallel()

	h := setup(t, options{})
	storeA, cacheB := replicas(t, h)

	savePayments(t, storeA, nil)

	require.Eventually(t, func() bool { _, ok := cacheRoute(cacheB, "payments"); return ok }, replicaDeadline, 10*time.Millisecond)

	_, err := storeA.DeleteRoute(t.Context(), routeActor, "payments")
	require.NoError(t, err)

	require.Eventually(t, func() bool { _, ok := cacheRoute(cacheB, "payments"); return !ok }, replicaDeadline, 10*time.Millisecond)

	err = h.run(t)
	require.ErrorIs(t, err, queue.ErrGiveUp)
	require.ErrorContains(t, err, `route "payments" is no longer in effect`)
}

// replicas returns the store of replica A and the cache of replica B, which
// the handler of h follows. The file config has the default route only, so
// payments is a runtime route. The handler has the signoz and loki sources.
func replicas(t *testing.T, h *harness) (*rules.Store, *rules.Cache) {
	t.Helper()

	file := replicaConfig()
	cacheB := rules.NewCache(t.Context(), rules.Options{File: file, Pool: h.pool, Interval: time.Hour})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- cacheB.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	h.handler.opts.Routes = ConfigRoutes(func() []config.Route { return cacheB.Current().RouteConfigs() })
	h.handler.opts.Guard = twoSourceGuard(t)

	return rules.NewStore(h.pool, file), cacheB
}

// replicaConfig is the file config of both replicas.
func replicaConfig() *config.Config {
	def := config.NewRoute(config.DefaultRouteName)

	return &config.Config{
		Sources: map[string]config.Section{signozSource: {Type: "mcp"}, lokiSource: {Type: "mcp"}},
		Outputs: map[string]config.Section{"slack-thread": {Type: "slack"}},
		Routes:  []config.Route{def},
		Budgets: config.Budgets{
			PerInvestigation: config.Limits{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute},
			Concurrency:      8,
		},
	}
}

// requireRoute waits until the handler of h sees the payments route with
// sources.
func requireRoute(t *testing.T, h *harness, cache *rules.Cache, sources []string) {
	t.Helper()

	require.Eventually(t, func() bool {
		r, ok := cacheRoute(cache, "payments")

		return ok && slices.Equal(r.Sources, sources)
	}, replicaDeadline, 10*time.Millisecond)

	got, ok := h.handler.opts.Routes.Route("payments")
	require.True(t, ok)
	require.Equal(t, sources, got.Sources)
}

// cacheRoute returns the route named name in effect in cache.
func cacheRoute(cache *rules.Cache, name string) (config.Route, bool) {
	for _, r := range cache.Current().RouteConfigs() {
		if r.Name == name {
			return r, true
		}
	}

	return config.Route{}, false
}

// savePayments saves the runtime route payments through store with sources.
func savePayments(t *testing.T, store *rules.Store, sources []string) {
	t.Helper()

	_, err := store.SetRoute(t.Context(), routeActor, "payments", func(r *rules.Route) error {
		r.Config.Match = &config.Match{Service: "checkout-*"}
		r.Config.Sources = sources
		r.Config.Outputs = []string{"slack-thread"}

		return nil
	})
	require.NoError(t, err)
}

// qualified returns the name the model sees for a source's tool.
func qualified(t *testing.T, sourceName, tool string) string {
	t.Helper()

	name, err := source.QualifiedName(sourceName, tool)
	require.NoError(t, err)

	return name
}

// sourceToolNames returns the names of the signoz and loki tools in req.
func sourceToolNames(t *testing.T, req model.Request) []string {
	t.Helper()

	var names []string

	for _, spec := range req.Tools {
		if spec.Name == qualified(t, signozSource, signozTool) || spec.Name == qualified(t, lokiSource, lokiTool) {
			names = append(names, spec.Name)
		}
	}

	return names
}

// twoSourceGuard returns a guard with a signoz source and a loki source,
// each with one tool.
func twoSourceGuard(t *testing.T) *tools.Guard {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	guard, err := tools.New([]tools.Source{
		{Name: signozSource, Source: namedToolSource(t, signozTool)},
		{Name: lokiSource, Source: namedToolSource(t, lokiTool)},
	}, tools.Options{Redactor: redactor, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	return guard
}

// namedToolSource returns a source with one read-only tool named name.
func namedToolSource(t *testing.T, name string) source.Source {
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
