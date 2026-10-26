//go:build integration

package router

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Payloads in fixture/alertmanager.
const (
	firingTwo      = "fixture/alertmanager/firing-two.json"
	resolvedFirst  = "fixture/alertmanager/resolved-first.json"
	resolvedSecond = "fixture/alertmanager/resolved-second.json"
)

// inconclusiveReport is a report that needs no evidence.
const inconclusiveReport = `{"verdict": "inconclusive", "confidence": "low", "reason": "Too little data.", "summary": "Not sure yet.", "findings": []}`

func TestRecoveryEvents(t *testing.T) {
	t.Parallel()

	afterTheWindow := time.Now().Add(config.DefaultGroupingWindow + time.Minute)

	tests := []struct {
		name string
		// steps run in order: a payload file is delivered and routed, and
		// "close" runs the close_group jobs after the window.
		steps      []string
		wantStatus string
		wantReason *string
		// wantLinked is whether the recoveries are in the group.
		wantLinked bool
	}{
		{
			name:       "a recovery of one of two alerts",
			steps:      []string{firingTwo, resolvedFirst},
			wantStatus: "open",
			wantLinked: true,
		},
		{
			name:       "a recovery of every alert resolves the group",
			steps:      []string{firingTwo, resolvedFirst, resolvedSecond, "close-now"},
			wantStatus: "closed",
			wantReason: new(grouper.ClosedResolved),
			wantLinked: true,
		},
		{
			name:       "an alert that fires again after its recovery",
			steps:      []string{firingTwo, resolvedFirst, resolvedSecond, firingTwoAgain, "close-now"},
			wantStatus: "open",
			wantLinked: true,
		},
		{
			name:       "a recovery after the window ended",
			steps:      []string{firingTwo, "close", resolvedFirst, resolvedSecond, "close-now"},
			wantStatus: "closed",
			wantReason: new(grouper.ClosedWindowEnded),
			wantLinked: true,
		},
		{
			name:  "a recovery of an alert in no group",
			steps: []string{resolvedFirst},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			for _, step := range tt.steps {
				switch step {
				case "close":
					h.closeGroups(t, afterTheWindow)
				case "close-now":
					h.closeGroups(t, time.Now())
				case firingTwoAgain:
					_, err := h.pool.Exec(t.Context(), "UPDATE events SET key = key || '-before' WHERE kind = 'alert'")
					require.NoError(t, err)
					h.deliver(t, firingTwo)
					h.routeAll(t)
				default:
					h.deliver(t, step)
					h.routeAll(t)
				}
			}

			// The seeded recovery, which intake skipped, is not part of it.
			recoveries := collect[db.Event](t, h.pool,
				"SELECT * FROM events WHERE kind = 'resolved' AND id <> '"+resolvedEvent.String()+"' ORDER BY received_at, id")
			require.NotEmpty(t, recoveries)

			groups := collect[db.EventGroup](t, h.pool, "SELECT * FROM event_groups WHERE route = 'crashloop'")

			if !tt.wantLinked {
				require.Empty(t, groups)

				for _, ev := range recoveries {
					require.Equal(t, "skipped", ev.Status)
					require.Equal(t, new("no group has this alert"), ev.SkipReason)
				}

				return
			}

			require.Len(t, groups, 1)
			group := groups[0]
			require.Equal(t, tt.wantStatus, group.Status)
			require.Equal(t, tt.wantReason, group.ClosedReason)

			for _, ev := range recoveries {
				require.Equal(t, "grouped", ev.Status)
				require.Equal(t, &group.ID, ev.GroupID)
				require.Nil(t, ev.Route, "a recovery is not routed")
			}

			var waiting int
			require.NoError(t, h.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM investigation_inbox WHERE kind = 'resolved' AND investigation_id = $1", *group.InvestigationID).Scan(&waiting))
			require.Equal(t, len(recoveries), waiting, "each recovery goes to the inbox")
		})
	}
}

// firingTwoAgain stands for firing-two.json sent again with new keys, as
// Alertmanager does when the alerts fire again.
const firingTwoAgain = "firing-two-again"

// TestRecoveryDuringARun delivers a recovery while the investigation's
// model takes its first turn, and checks that the second turn sees it,
// that the run is not stopped, and that the group closes as resolved once
// both alerts have recovered.
func TestRecoveryDuringARun(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.deliver(t, firingTwo)
	h.routeAll(t)

	conn := modeltest.New(t, model.Capabilities{ToolCalling: true},
		modeltest.Turn{Func: func(context.Context, model.Request) (model.Response, error) {
			h.deliver(t, resolvedFirst)
			h.routeAll(t)

			return modeltest.ToolUse(model.Usage{}, modeltest.Call("f1", investigator.RecordFindingTool,
				`{"claim": "The pods restart every few minutes.", "evidenceToolCallIds": []}`)).Response, nil
		}},
		modeltest.ToolUse(model.Usage{}, modeltest.Call("v1", report.SubmitVerdictTool, inconclusiveReport)),
	)
	conn.ExpectAllUsed()

	require.NoError(t, h.investigate(t, conn))

	requests := conn.Requests()
	require.Len(t, requests, 2)
	require.Contains(t, lastUserText(requests[0]), "1 new event joined the group.", "the second alert waited for the first turn")
	require.NotContains(t, lastUserText(requests[0]), "recovery arrived")
	require.Contains(t, lastUserText(requests[1]), "1 recovery arrived.", "the recovery reaches the next turn")

	inv := h.investigations(t)[0]
	require.Equal(t, investigator.StatusFinal, inv.Status, "the recovery did not stop the run")
	require.Equal(t, new(string(report.StopSubmitted)), inv.StopReason)

	h.closeGroups(t, time.Now())
	require.Equal(t, "open", h.groups(t)[0].Status, "one alert has not recovered yet")

	h.deliver(t, resolvedSecond)
	h.routeAll(t)
	h.closeGroups(t, time.Now())

	group := h.groups(t)[0]
	require.Equal(t, "closed", group.Status)
	require.Equal(t, new(grouper.ClosedResolved), group.ClosedReason)
}

// investigate runs the pending investigate job with conn as the fake
// connector.
func (h *harness) investigate(t *testing.T, conn model.Connector) error {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	guard, err := tools.New(nil, tools.Options{Redactor: redactor, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	handler, err := investigator.New(investigator.Options{
		Pool:       h.pool,
		Guard:      guard,
		Connectors: map[string]investigator.Connector{"fake": {Connector: conn}},
		Prices:     pricing.Shipped(),
		Strategies: h.cfg.StrategySet(),
		Routes:     map[string]investigator.Route{"crashloop": {}},
	})
	require.NoError(t, err)

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindInvestigate}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	if err := handler.Handle(t.Context(), jobs[0]); err != nil {
		return fmt.Errorf("investigate: %w", err)
	}

	return nil
}

// lastUserText joins the text parts of the last message of req.
func lastUserText(req model.Request) string {
	var b strings.Builder

	for _, p := range req.Messages[len(req.Messages)-1].Parts {
		if text, ok := p.(model.Text); ok {
			b.WriteString(text.Text)
		}
	}

	return b.String()
}
