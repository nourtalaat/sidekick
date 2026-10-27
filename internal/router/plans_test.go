//go:build integration

package router

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each rate limit query over 50,000
// events and 100 scopes with a full window of 600 admissions each, and
// checks that it uses the index it is meant to. Run with -v to see the
// plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	// waiting is a received event in the middle of the table.
	var waiting uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('event' || 25000)::uuid").Scan(&waiting))

	now := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	route := "route-50"

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "LockRateScope",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.LockRateScope(ctx, "route:"+route))
			},
			wantIndex: []string{"Result"},
		},
		{
			name: "GetRateWindow",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetRateWindow(ctx, db.GetRateWindowParams{Since: now.Add(-time.Hour), EventID: waiting, Scope: "route:" + route})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"rate_admissions_"},
		},
		{
			name: "AdmitEvent",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.AdmitEvent(ctx, db.AdmitEventParams{Scope: "route:" + route, EventID: waiting, AdmittedAt: now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on rate_admissions", "Conflict Arbiter Indexes: rate_admissions_pkey"},
		},
		{
			name: "PruneRateAdmissions",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.PruneRateAdmissions(ctx, db.PruneRateAdmissionsParams{Scope: "route:" + route, Before: now.Add(-time.Hour)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"rate_admissions_scope_admitted"},
		},
		{
			name: "MarkEventHeld",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.MarkEventHeld(ctx, db.MarkEventHeldParams{Status: statusDelayed, Route: &route, SkipReason: &route, ID: waiting})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"events_pkey"},
		},
		{
			name: "MarkEventReceived",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.MarkEventReceived(ctx, waiting))
			},
			wantIndex: []string{"events_pkey"},
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
