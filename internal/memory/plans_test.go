//go:build integration

package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

	// after is the place of the entry in the middle of the seeded list.
	after := time.Date(2026, 10, 26, 0, 3, 0, 0, time.UTC).Add(10000 * time.Minute)

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
			name: "DeleteUnconfirmedMemoryEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.DeleteUnconfirmedMemoryEntry(ctx, busy))
			},
			wantIndex: []string{"memory_entries_pkey", "investigations_pkey"},
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
		{
			name: "ListMemoryEntries",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_confirmed"},
		},
		{
			name: "ListMemoryEntriesAfterACursor",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{AfterAt: &after, AfterID: &busy, PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_confirmed"},
		},
		{
			name: "ListMemoryEntriesOfAService",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{Service: new("svc-42"), PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_service_listing"},
		},
		{
			name: "ListMemoryEntriesOfAServiceAfterACursor",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{Service: new("svc-42"), AfterAt: &after, AfterID: &busy, PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_service_listing"},
		},
		{
			name: "ListMemoryEntriesWithAVerdict",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{Verdict: new("false_positive"), PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_confirmed"},
		},
		{
			name: "ListMemoryEntriesWithWords",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{Search: new("w42"), PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_search_all"},
		},
		{
			// With enabled entries only, either text index serves.
			name: "ListMemoryEntriesWithWordsOfEnabledEntries",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{Search: new("w42"), Disabled: new(false), PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_search"},
		},
		{
			name: "ListMemoryEntriesOfDisabledEntries",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{Disabled: new(true), PageSize: 51})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_disabled_listing"},
		},
		{
			name: "ListMemoryEntriesWithEveryFilter",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryEntries(ctx, db.ListMemoryEntriesParams{
					Service: new("svc-40"), Verdict: new("true_positive"), Search: new("w40"), Disabled: new(true),
					AfterAt: &after, AfterID: &busy, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Index Scan"},
		},
		{
			name: "GetMemoryEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetMemoryEntry(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_pkey"},
		},
		{
			name: "LockMemoryEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockMemoryEntry(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_pkey"},
		},
		{
			name: "UpdateMemoryEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpdateMemoryEntry(ctx, db.UpdateMemoryEntryParams{InvestigationID: busy, Summary: "A new summary.", Disabled: true})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_pkey"},
		},
		{
			name: "DeleteMemoryEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DeleteMemoryEntry(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_pkey"},
		},
		{
			name: "InsertMemoryUses",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.InsertMemoryUses(ctx, db.InsertMemoryUsesParams{
					InvestigationID: busy, Run: 2, ExampleIds: []uuid.UUID{md5UUID(t, pool, "inv11"), md5UUID(t, pool, "inv12")},
					Matches: []string{"fingerprint", "text"},
				}))
			},
			wantIndex: []string{"Insert on memory_uses", "Conflict Arbiter Indexes: memory_uses_pkey"},
		},
		{
			name: "ListMemoryUsedBy",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryUsedBy(ctx, db.ListMemoryUsedByParams{InvestigationID: busy, Run: 1, MaxRows: MaxExamples})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_uses_pkey", "memory_entries_pkey", "investigations_pkey"},
		},
		{
			name: "GetServiceNotesDetail",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetServiceNotesDetail(ctx, "svc-42")

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"service_notes_pkey", "users_pkey"},
		},
		{
			name: "ListServiceNotes",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListServiceNotes(ctx, db.ListServiceNotesParams{After: "svc-420", PageSize: 101})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"service_notes_pkey", "users_pkey"},
		},
		{
			name: "LockServiceNotes",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.LockServiceNotes(ctx, "svc-42"))
			},
		},
		{
			name: "UpsertServiceNotes",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.UpsertServiceNotes(ctx, db.UpsertServiceNotesParams{Service: "svc-42", Text: "New notes."}))
			},
			wantIndex: []string{"Conflict Arbiter Indexes: service_notes_pkey"},
		},
		{
			name: "DeleteServiceNotes",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DeleteServiceNotes(ctx, "svc-42")

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

// md5UUID returns md5(seed) as a UUID, as the plan fixture makes its IDs.
func md5UUID(t *testing.T, pool *pgxpool.Pool, seed string) uuid.UUID {
	t.Helper()

	var id uuid.UUID

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5($1)::uuid", seed).Scan(&id))

	return id
}
