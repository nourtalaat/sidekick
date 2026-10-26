//go:build integration

package grouper

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

// TestQueryPlans runs EXPLAIN ANALYZE on each grouper query over 50,000
// groups with 100,000 events and checks that it uses the index it is meant
// to. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy.sql")

	// busy is an open group in the middle of the table, and busyEvent one
	// of its events.
	var busy, busyEvent uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('group' || 25000)::uuid, md5('event' || 25000 || '-' || 1)::uuid").Scan(&busy, &busyEvent))

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "LockOpenEventGroup",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockOpenEventGroup(ctx, "fp-open-25000")

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"event_groups_open_fingerprint"},
		},
		{
			name: "LockEventGroup",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockEventGroup(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"event_groups_pkey"},
		},
		{
			name: "OpenEventGroup",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.OpenEventGroup(ctx, db.OpenEventGroupParams{
					ID: uuid.New(), Fingerprint: "fp-new", Route: "default", LastEventAt: time.Now(), WindowUntil: time.Now().Add(window),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on event_groups"},
		},
		{
			name: "JoinEventGroup",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.JoinEventGroup(ctx, db.JoinEventGroupParams{ReceivedAt: time.Now(), WindowUntil: time.Now().Add(window), ID: busy})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"event_groups_pkey"},
		},
		{
			name: "CloseEventGroup",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CloseEventGroup(ctx, db.CloseEventGroupParams{ClosedReason: ClosedWindowEnded, ID: busy})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"event_groups_pkey"},
		},
		{
			name: "MarkEventRouted",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.MarkEventRouted(ctx, db.MarkEventRoutedParams{ID: busyEvent, Route: new("default")})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"events_pkey"},
		},
		{
			name: "MarkEventGrouped",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.MarkEventGrouped(ctx, db.MarkEventGroupedParams{GroupID: &busy, ID: busyEvent})

				return fmt.Errorf("run query: %w", err)
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

			explain := &storetest.Explainer{Tx: tx}

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)
			t.Logf("EXPLAIN ANALYZE of %s:\n%s", tt.name, explain.Plan)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}
