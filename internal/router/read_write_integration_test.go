//go:build integration

package router

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestInvestigationModeOfAReadWriteRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		event        uuid.UUID
		intake       string
		allowUntrust bool
		want         string
	}{
		{name: "a trusted event keeps read-write", event: paymentsEvent, intake: "alertmanager", want: "read-write"},
		{name: "an untrusted event waits for approval", event: diskEvent, intake: "grafana", want: "approved"},
		{name: "an untrusted event with the override keeps read-write", event: diskEvent, intake: "grafana", allowUntrust: true, want: "read-write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, instances := loadConfig(t)

			route := config.NewRoute("write")
			route.Priority = 1
			route.Match = &config.Match{Intake: []string{tt.intake}}
			route.Mode, route.Actions, route.AllowUntrustedWrite = action.ReadWrite, []string{"monitor-note"}, tt.allowUntrust
			cfg.Routes = append(cfg.Routes, cfg.ResolveRuntimeRoute(route))

			h := setupWith(t, cfg, instances)
			require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, tt.event)))

			investigations := h.investigations(t)
			require.Len(t, investigations, 1)
			require.Equal(t, "write", investigations[0].Route)
			require.Equal(t, tt.want, investigations[0].Mode)
		})
	}
}

// secondEvent is the ID of the event that the join tests add.
var secondEvent = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000a1")

// addEventLike stores a copy of the event like, as a received event with the
// given kind, trust and fingerprint, a minute later.
func (h *harness) addEventLike(t *testing.T, like uuid.UUID, kind string, trusted bool, fingerprint string) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), `
		INSERT INTO events (id, intake, key, kind, trusted, alert_name, title, body, service, severity, labels, fingerprint, status, skip_reason, received_at)
		SELECT $1, intake, key || '-second', $3, $4, alert_name, title, body, service, severity, labels, $5, 'received', NULL, received_at + interval '1 minute'
		FROM events WHERE id = $2`, secondEvent, like, kind, trusted, fingerprint)
	require.NoError(t, err)
}

// TestAnEventThatIsNotTrustedMovesAReadWriteInvestigationToApproved joins a
// second event to the read-write investigation a trusted event opened. The
// text of the second event reaches the model, so unless the route has the
// override, what the model proposes next waits for a person.
func TestAnEventThatIsNotTrustedMovesAReadWriteInvestigationToApproved(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		kind         string
		trusted      bool
		fingerprint  string
		related      bool
		allowUntrust bool
		want         string
	}{
		{name: "a trusted event with the same fingerprint", kind: "alert", trusted: true, fingerprint: "fp-checkout", want: "read-write"},
		{name: "an event that is not trusted, with the same fingerprint", kind: "alert", fingerprint: "fp-checkout", want: "approved"},
		{name: "the same, on a route with the override", kind: "alert", fingerprint: "fp-checkout", allowUntrust: true, want: "read-write"},
		{name: "an event that is not trusted, joined by the related window", kind: "alert", fingerprint: "fp-checkout-2", related: true, want: "approved"},
		{name: "a recovery event that is not trusted", kind: "resolved", fingerprint: "fp-checkout", want: "approved"},
		{name: "a trusted recovery event", kind: "resolved", trusted: true, fingerprint: "fp-checkout", want: "read-write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, instances := loadConfig(t)

			route := config.NewRoute("write")
			route.Priority = 1
			route.Match = &config.Match{Intake: []string{"alertmanager"}}
			route.Mode, route.Actions, route.AllowUntrustedWrite = action.ReadWrite, []string{"monitor-note"}, tt.allowUntrust

			if tt.related {
				route.Grouping.RelatedWindow = 10 * time.Minute
			}

			cfg.Routes = append(cfg.Routes, cfg.ResolveRuntimeRoute(route))

			h := setupWith(t, cfg, instances)
			require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, paymentsEvent)))
			require.Equal(t, "read-write", h.investigations(t)[0].Mode)

			h.addEventLike(t, paymentsEvent, tt.kind, tt.trusted, tt.fingerprint)
			require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, secondEvent)))

			investigations := h.investigations(t)
			require.Len(t, investigations, 1, "the second event joined the investigation")
			require.Equal(t, tt.want, investigations[0].Mode)
			require.Len(t, h.inbox(t), 1)
		})
	}
}

// noteAction is an action that writes a note; the tests only queue it.
type noteAction struct{}

func (noteAction) Spec() action.Spec {
	return action.Spec{
		Name:         "monitor-note",
		Description:  "Writes a note.",
		ParamsSchema: json.RawMessage(`{"type": "object", "additionalProperties": false, "required": ["title"], "properties": {"title": {"type": "string"}}}`),
	}
}

func (noteAction) Validate(context.Context, json.RawMessage) error { return nil }

func (noteAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{Summary: "Wrote a note."}, nil
}

const proposeNote = `{"action": "monitor-note", "params": {"title": "Slow checkout"}, "reason": "p99 doubled after the deploy."}`

// TestProposalOnAReadWriteRoute follows an event from the router to the
// proposal its investigation makes: only an event from a trusted sender, or
// a route with the override, runs without a person.
func TestProposalOnAReadWriteRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		event        uuid.UUID
		intake       string
		allowUntrust bool
		want         action.ProposalStatus
		wantByPolicy bool
		wantJobs     int
	}{
		{name: "an event from a trusted sender runs without a person", event: paymentsEvent, intake: "alertmanager", want: action.StatusApproved, wantByPolicy: true, wantJobs: 1},
		{name: "an event that is not trusted waits for a person", event: diskEvent, intake: "grafana", want: action.StatusProposed},
		{name: "an event that is not trusted, on a route with the override", event: diskEvent, intake: "grafana", allowUntrust: true, want: action.StatusApproved, wantByPolicy: true, wantJobs: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, instances := loadConfig(t)

			route := config.NewRoute("write")
			route.Priority = 1
			route.Match = &config.Match{Intake: []string{tt.intake}}
			route.Mode, route.Actions, route.AllowUntrustedWrite = action.ReadWrite, []string{"monitor-note"}, tt.allowUntrust
			cfg.Routes = append(cfg.Routes, cfg.ResolveRuntimeRoute(route))

			h := setupWith(t, cfg, instances)
			require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, tt.event)))

			svc, err := proposals.New(proposals.Options{
				Pool:    h.pool,
				Actions: map[string]action.Action{"monitor-note": noteAction{}},
				Policy:  action.StaticPolicy{"write": {Mode: action.ReadWrite, Actions: []string{"monitor-note"}}},
			})
			require.NoError(t, err)

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true},
				modeltest.ToolUse(model.Usage{}, modeltest.Call("p1", proposals.ProposeActionTool, proposeNote)),
				modeltest.ToolUse(model.Usage{}, modeltest.Call("v1", report.SubmitVerdictTool, inconclusiveReport)),
			)
			conn.ExpectAllUsed()

			handler, err := investigator.New(investigator.Options{
				Pool:       h.pool,
				Guard:      twoSourceGuard(t),
				Connectors: map[string]investigator.Connector{"fake": {Connector: conn}},
				Prices:     pricing.Shipped(),
				Strategies: h.cfg.StrategySet(),
				Routes:     investigator.ConfigRoutes(func() []config.Route { return cfg.Routes }),
				Actions:    svc,
			})
			require.NoError(t, err)

			jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
				Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindInvestigate}, Batch: 1,
			})
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			require.NoError(t, handler.Handle(t.Context(), jobs[0]))

			inv := h.investigations(t)[0]
			require.Equal(t, investigator.StatusFinal, inv.Status)

			stored, err := proposals.OfInvestigation(t.Context(), h.pool, inv.ID)
			require.NoError(t, err)
			require.Len(t, stored, 1)
			require.Equal(t, tt.want, stored[0].Status)
			require.Equal(t, tt.wantByPolicy, stored[0].ApprovedByPolicy)

			var executeJobs int
			require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'execute_action'").Scan(&executeJobs))
			require.Equal(t, tt.wantJobs, executeJobs)
		})
	}
}
