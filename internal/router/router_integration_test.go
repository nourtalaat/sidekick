//go:build integration

package router

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestRouteEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event uuid.UUID
		// runs is how many times the job runs; zero means once.
		runs        int
		wantRoute   string
		wantMode    string
		wantBudget  string
		wantService *string
	}{
		{
			name:        "a trusted event on its own route",
			event:       paymentsEvent,
			wantRoute:   "payments-latency",
			wantMode:    "read-only",
			wantBudget:  `{"max_tokens": 400000, "max_tool_calls": 30, "max_wall_time_ms": 300000, "max_cost_micros": 1000000}`,
			wantService: ptr("checkout-api"),
		},
		{
			name:        "an untrusted event",
			event:       diskEvent,
			wantRoute:   "grafana",
			wantMode:    "read-only",
			wantBudget:  `{"max_tokens": 400000, "max_tool_calls": 40, "max_wall_time_ms": 600000, "max_cost_micros": 1000000}`,
			wantService: ptr("db"),
		},
		{
			name:       "an event only the default route fits",
			event:      otherEvent,
			wantRoute:  "default",
			wantMode:   "read-only",
			wantBudget: `{"max_tokens": 400000, "max_tool_calls": 40, "max_wall_time_ms": 600000, "max_cost_micros": 1000000}`,
		},
		{
			name:        "the same job run twice creates nothing more",
			event:       paymentsEvent,
			runs:        2,
			wantRoute:   "payments-latency",
			wantMode:    "read-only",
			wantBudget:  `{"max_tokens": 400000, "max_tool_calls": 30, "max_wall_time_ms": 300000, "max_cost_micros": 1000000}`,
			wantService: ptr("checkout-api"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			before := h.event(t, tt.event)
			require.Equal(t, "received", before.Status)

			job := h.routeJob(t, tt.event)

			for range max(tt.runs, 1) {
				require.NoError(t, h.router.Handle(t.Context(), job))
			}

			groups := h.groups(t)
			require.Len(t, groups, 1)

			group := groups[0]
			require.Equal(t, before.Fingerprint, group.Fingerprint)
			require.Equal(t, tt.wantRoute, group.Route)
			require.Equal(t, tt.wantService, group.Service)
			require.Equal(t, "open", group.Status)
			require.Equal(t, int32(1), group.EventCount)
			require.Equal(t, before.ReceivedAt, group.LastEventAt)
			require.Nil(t, group.WindowUntil)
			require.Nil(t, group.ClosedReason)

			after := h.event(t, tt.event)
			require.Equal(t, "routed", after.Status)
			require.Equal(t, &tt.wantRoute, after.Route)
			require.Equal(t, &group.ID, after.GroupID)

			investigations := h.investigations(t)
			require.Len(t, investigations, 1)

			inv := investigations[0]
			require.Equal(t, group.InvestigationID, &inv.ID)
			require.Equal(t, group.ID, inv.GroupID)
			require.Equal(t, tt.wantRoute, inv.Route)
			require.Equal(t, tt.wantMode, inv.Mode)
			require.Equal(t, investigator.StatusQueued, inv.Status)
			require.Equal(t, int32(1), inv.Run)
			require.Len(t, *inv.StrategyVersion, 16)
			require.JSONEq(t, tt.wantBudget, string(inv.Budget))
			require.Nil(t, inv.StartedAt)

			jobs := h.investigateJobs(t)
			require.Len(t, jobs, 1)
			require.Equal(t, &tt.wantRoute, jobs[0].Route)
			require.Equal(t, investigator.UniqueKey(group.ID), *jobs[0].UniqueKey)

			var payload investigator.Payload
			require.NoError(t, json.Unmarshal(jobs[0].Payload, &payload))
			require.Equal(t, investigator.Payload{InvestigationID: inv.ID, GroupID: group.ID}, payload)

			loaded, err := db.New(h.pool).GetInvestigationWithEvent(t.Context(), inv.ID)
			require.NoError(t, err)
			require.Equal(t, inv, loaded.Investigation)
			require.Equal(t, after, loaded.Event)
		})
	}
}

func TestRouteEventSettings(t *testing.T) {
	t.Parallel()

	h := setup(t)
	require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, paymentsEvent)))

	investigations := h.investigations(t)
	require.Len(t, investigations, 1)

	inv := investigations[0]
	require.Equal(t, "latency-alert", *inv.Strategy)
	require.JSONEq(t, `{
		"investigate": {"connector": "big", "model": "big-model"},
		"summarize": {"connector": "fake", "model": "fake-model"}
	}`, string(inv.Connectors))

	var budget investigator.Budget
	require.NoError(t, json.Unmarshal(inv.Budget, &budget))
	require.Equal(t, 30, budget.MaxToolCalls)
}

func TestSameFingerprintOpensTwoGroups(t *testing.T) {
	t.Parallel()

	h := setup(t)

	for _, id := range []uuid.UUID{diskEvent, diskAgain} {
		require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, id)))
	}

	groups := h.groups(t)
	require.Len(t, groups, 2)

	for _, g := range groups {
		require.Equal(t, "fp-disk", g.Fingerprint)
		require.Equal(t, "open", g.Status)
	}

	require.NotEqual(t, groups[0].ID, groups[1].ID)
	require.NotEqual(t, *h.event(t, diskEvent).GroupID, *h.event(t, diskAgain).GroupID)
	require.Len(t, h.investigations(t), 2)
	require.Len(t, h.investigateJobs(t), 2)
}

func TestEventsThatAreNotRouted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// prepare changes the seeded event before the job runs.
		prepare string
		event   uuid.UUID
		want    string
	}{
		{name: "a resolved event that intake skipped", event: resolvedEvent, want: "skipped"},
		{
			name:    "a resolved event that is still received",
			prepare: "UPDATE events SET status = 'received', skip_reason = NULL WHERE id = $1",
			event:   resolvedEvent,
			want:    "received",
		},
		{
			name:    "an event routed before",
			prepare: "UPDATE events SET status = 'routed', route = 'default' WHERE id = $1",
			event:   paymentsEvent,
			want:    "routed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			if tt.prepare != "" {
				_, err := h.pool.Exec(t.Context(), tt.prepare, tt.event)
				require.NoError(t, err)
			}

			before := h.event(t, tt.event)

			require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, tt.event)))
			require.Equal(t, tt.want, h.event(t, tt.event).Status)
			require.Equal(t, before, h.event(t, tt.event))
			require.Empty(t, h.groups(t))
			require.Empty(t, h.investigations(t))
			require.Empty(t, h.investigateJobs(t))
		})
	}
}

func TestRouteEventAtTheSameTime(t *testing.T) {
	t.Parallel()

	const runs = 4

	h := setup(t)
	job := h.routeJob(t, paymentsEvent)

	var wg sync.WaitGroup

	errs := make([]error, runs)
	for i := range runs {
		wg.Go(func() { errs[i] = h.router.Handle(t.Context(), job) })
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	require.Len(t, h.groups(t), 1)
	require.Len(t, h.investigations(t), 1)
	require.Len(t, h.investigateJobs(t), 1)
}

func TestRouteEventGivesUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{name: "a payload that is not JSON", payload: `"event"`},
		{name: "a payload without an event ID", payload: `{}`},
		{name: "an event that does not exist", payload: `{"event_id": "0192d3a4-0000-7000-8000-0000000000ff"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			err := h.router.Handle(t.Context(), h.enqueue(t, t.Context(), []byte(tt.payload)))
			require.ErrorIs(t, err, queue.ErrGiveUp)
			require.Empty(t, h.groups(t))

			ended := h.spans.Ended()
			require.Len(t, ended, 1)
			require.Equal(t, codes.Error, ended[0].Status().Code)
		})
	}
}

func TestRouteSpan(t *testing.T) {
	t.Parallel()

	h := setup(t)
	job := h.routeJob(t, paymentsEvent)
	require.NoError(t, h.router.Handle(t.Context(), job))

	ended := h.spans.Ended()

	var intake, route trace.ReadOnlySpan

	for _, s := range ended {
		switch s.Name() {
		case "intake.receive":
			intake = s
		case SpanName:
			route = s
		}
	}

	require.NotNil(t, intake)
	require.NotNil(t, route)
	require.Equal(t, intake.SpanContext().TraceID(), route.SpanContext().TraceID())
	require.Equal(t, intake.SpanContext().SpanID(), route.Parent().SpanID(), "router.route is a child of the span that enqueued the job")

	group := h.groups(t)[0]
	require.ElementsMatch(t, []attribute.KeyValue{
		attribute.String(attrRoute, "payments-latency"),
		attribute.String(attrGroupID, group.ID.String()),
	}, route.Attributes())

	jobs := h.investigateJobs(t)
	require.Len(t, jobs, 1)
	require.Contains(t, *jobs[0].Traceparent, route.SpanContext().SpanID().String(), "the investigate job carries the router.route span")
}
