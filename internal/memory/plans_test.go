//go:build integration

package memory

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each memory query over 20,000
// entries, and checks that it uses the index it is meant to. Run with -v
// to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := setup(t, "fixture/plans.sql")

	// busy is one of the seeded investigations, in the middle of the table.
	var busy uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('inv' || 10000)::uuid").Scan(&busy))

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "UpsertMemoryEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpsertMemoryEntry(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey", "events_group", "Conflict Arbiter Indexes: memory_entries_pkey"},
		},
		{
			name: "DeleteMemoryEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.DeleteMemoryEntry(ctx, busy))
			},
			wantIndex: []string{"memory_entries_pkey"},
		},
		{
			name: "ListMemoryByFingerprint",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryByFingerprint(ctx, db.ListMemoryByFingerprintParams{
					Fingerprint: "fp-42", Exclude: []uuid.UUID{busy}, MaxRows: DefaultExamples,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_fingerprint"},
		},
		{
			name: "ListMemoryByServiceAlert",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryByServiceAlert(ctx, db.ListMemoryByServiceAlertParams{
					Service: "svc-42", AlertName: "Alert42", Exclude: []uuid.UUID{busy}, MaxRows: DefaultExamples,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_service_alert"},
		},
		{
			name: "ListSimilarMemory",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListSimilarMemory(ctx, db.ListSimilarMemoryParams{
					Text: "Alert w42 fired for w43", Exclude: []uuid.UUID{busy}, MaxRows: DefaultExamples,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_search"},
		},
		{
			name: "SearchMemory",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.SearchMemory(ctx, db.SearchMemoryParams{
					Query: "w42", Verdict: new("true_positive"), MaxRows: DefaultExamples,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_search"},
		},
		{
			// With a service, the service index can find fewer rows to
			// check than the text index; either is fast.
			name: "SearchMemoryOfAService",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.SearchMemory(ctx, db.SearchMemoryParams{
					Query: "w42", Service: new("svc-42"), Verdict: new("true_positive"), MaxRows: DefaultExamples,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Index Scan on memory_entries_"},
		},
		{
			name: "GetServiceNotes",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetServiceNotes(ctx, "svc-42")

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"service_notes_pkey"},
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
