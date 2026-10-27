//go:build integration

package investigator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// planFence is the fence of busy in fixture/plans.sql.
const planFence = 1000 + 10000

// endedFence is the fence of ended in fixture/plans.sql.
const endedFence = 1000 + 10005

// TestQueryPlans runs EXPLAIN ANALYZE on each investigator query over
// 20,000 investigations with 500,000 steps and a year of daily spend for
// 20 routes, and checks that it uses the index it is meant to. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	// busy is a running investigation in the middle of the table, and
	// group its group.
	var busy, group uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('inv' || 10000)::uuid, md5('grp' || 10000)::uuid").Scan(&busy, &group))

	// ended is an investigation that has ended, with follow-up questions
	// in its inbox.
	var ended uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('inv' || 10005)::uuid").Scan(&ended))

	// busyEvent is the event of busy's group.
	var busyEvent uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('evt' || 10000)::uuid").Scan(&busyEvent))

	report := json.RawMessage(`{"verdict": "inconclusive"}`)
	today := spendDay(time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC), time.UTC)

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "InsertInboxItem",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.InsertInboxItem(ctx, db.InsertInboxItemParams{InvestigationID: busy, Kind: InboxNewEvent, Body: json.RawMessage(`{}`)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on investigation_inbox"},
		},
		{
			name: "InsertKeyedInboxItem",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertKeyedInboxItem(ctx, db.InsertKeyedInboxItemParams{
					InvestigationID: ended, Kind: InboxFollowUp, Body: json.RawMessage(`{}`), Key: "Ev10005-1",
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on investigation_inbox", "Conflict Arbiter Indexes: investigation_inbox_key"},
		},
		{
			name: "HasWaitingInbox",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.HasWaitingInbox(ctx, db.HasWaitingInboxParams{InvestigationID: busy, Kind: InboxFollowUp})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigation_inbox_waiting"},
		},
		{
			name: "LockInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockInvestigation(ctx, ended)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey", "LockRows"},
		},
		{
			name: "StartFollowUpRun",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.StartFollowUpRun(ctx, db.StartFollowUpRunParams{ID: ended, Fence: endedFence + 1})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "EndFollowUpRun",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.EndFollowUpRun(ctx, db.EndFollowUpRunParams{ID: busy, Report: report})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "FindInvestigationByExternal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.FindInvestigationByExternal(ctx, db.FindInvestigationByExternalParams{
					Sinks: []string{"slack-thread", "slack-incidents"}, External: "C0123ALERTS:1793010005.000100",
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"output_refs_external"},
		},
		{
			name: "ListWaitingInbox",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListWaitingInbox(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigation_inbox_waiting"},
		},
		{
			name: "MarkInboxDelivered",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.MarkInboxDelivered(ctx, db.MarkInboxDeliveredParams{DeliveredSeq: new(int32(25)), InvestigationID: busy, Ids: []int64{100001, 100002}})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigation_inbox_waiting"},
		},
		{
			name: "ListEventsByID",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListEventsByID(ctx, []uuid.UUID{busyEvent, group})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"events_pkey"},
		},
		{
			name: "GetInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetInvestigation(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "GetEventInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetEventInvestigation(ctx, busyEvent)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"events_pkey", "event_groups_pkey", "investigations_pkey"},
		},
		{
			name: "StartInvestigationRun",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.StartInvestigationRun(ctx, db.StartInvestigationRunParams{ID: busy, Fence: planFence + 1})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "GetFirstEvent",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetFirstEvent(ctx, group)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"event_groups_pkey", "events_group"},
		},
		{
			name: "ListSteps",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListSteps(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"steps_pkey"},
		},
		{
			name: "AdvanceInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.AdvanceInvestigation(ctx, db.AdvanceInvestigationParams{
					InputTokens: 1000, OutputTokens: 200, CostMicros: 10500, ToolCalls: 1, Finding: true, ID: busy, Fence: planFence,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "InsertStep",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.InsertStep(ctx, db.InsertStepParams{
					InvestigationID: busy, Seq: 25, Run: 1, Kind: kindToolResult, Parts: json.RawMessage(`[]`),
					Status: statusOK, StartedAt: time.Now(), DurationMs: 10,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on steps"},
		},
		{
			name: "FinishInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.FinishInvestigation(ctx, db.FinishInvestigationParams{
					Status: StatusFinal, Verdict: new("inconclusive"), Confidence: new("low"), Report: report,
					StopReason: new("budget"), ID: busy, Fence: planFence,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "FailInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.FailInvestigation(ctx, db.FailInvestigationParams{
					Verdict: new("inconclusive"), Confidence: new("low"), Report: report, StopReason: new("error"), ID: busy,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "AddSpend",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.AddSpend(ctx, db.AddSpendParams{
					Day: today, Route: "route-7", Connector: "claude", Model: "model-2", CostMicros: 10500, InputTokens: 1000, OutputTokens: 200,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on spend_daily", "Conflict Arbiter Indexes: spend_daily_pkey"},
		},
		{
			name: "GetDailySpend",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetDailySpend(ctx, db.GetDailySpendParams{Route: "route-7", Day: today})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"spend_daily_pkey"},
		},
		{
			name: "ListDailySpend",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListDailySpend(ctx, today)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"spend_daily_pkey"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}
