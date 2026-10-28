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

// planDimensions is the length of the vectors in the plan tests.
const planDimensions = 8

// TestVectorQueryPlans runs EXPLAIN ANALYZE on each vector query over 20,000
// entries that all have a random embedding, and checks that the nearest
// neighbour query uses the HNSW index. Run with -v to see the plans.
func TestVectorQueryPlans(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, planDimensions, "fixture/plans.sql")

	// Each embedding is a random vector; the correlated subquery makes
	// Postgres draw new numbers for every row.
	_, err := pool.Exec(t.Context(), `
		INSERT INTO memory_embeddings (investigation_id, model, dimensions, embedding, embedded_at)
		SELECT m.investigation_id, 'test-model', 8,
		       ('[' || (SELECT string_agg(random()::text, ',') FROM generate_series(1, 8) AS g WHERE m.investigation_id IS NOT NULL) || ']')::vector,
		       now()
		FROM memory_entries AS m`)
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), "ANALYZE memory_embeddings")
	require.NoError(t, err)

	// busy is one of the seeded investigations, in the middle of the table.
	var busy uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('inv' || 10000)::uuid").Scan(&busy))

	const query = "[0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8]"

	tests := []struct {
		name string
		run  func(ctx context.Context, q *db.Queries) error
		// wantPlan are texts the plan must hold.
		wantPlan []string
		// noSeqScanOn are tables the plan must not scan in order.
		noSeqScanOn []string
	}{
		{
			name: "ListNearestMemory",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListNearestMemory(ctx, db.ListNearestMemoryParams{
					Query: query, Candidates: nearestCandidates, Exclude: []uuid.UUID{busy}, MaxRows: fusionDepth,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantPlan:    []string{"Index Scan using memory_embeddings_embedding_hnsw on memory_embeddings"},
			noSeqScanOn: []string{"memory_embeddings"},
		},
		{
			name: "ListNearestMemoryOfAService",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListNearestMemory(ctx, db.ListNearestMemoryParams{
					Query: query, Candidates: nearestCandidates, Exclude: []uuid.UUID{busy}, MaxRows: fusionDepth,
					Service: new("svc-42"), Verdict: new("true_positive"),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantPlan:    []string{"Index Scan using memory_embeddings_embedding_hnsw on memory_embeddings"},
			noSeqScanOn: []string{"memory_embeddings"},
		},
		{
			name: "GetMemoryEmbedText",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetMemoryEmbedText(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantPlan:    []string{"memory_entries_pkey"},
			noSeqScanOn: []string{"memory_entries"},
		},
		{
			name: "UpsertMemoryEmbedding",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpsertMemoryEmbedding(ctx, db.UpsertMemoryEmbeddingParams{
					Model: "test-model", Dimensions: planDimensions, Embedding: query, InvestigationID: busy, TextHash: "x",
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantPlan:    []string{"memory_entries_pkey", "Conflict Arbiter Indexes: memory_embeddings_pkey"},
			noSeqScanOn: []string{"memory_entries"},
		},
		{
			// The start-up backfill scans the table once, in batches, so it
			// reads every entry; the plan is published, not checked for an
			// index.
			name: "ListMemoryNeedingEmbedding",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListMemoryNeedingEmbedding(ctx, db.ListMemoryNeedingEmbeddingParams{After: uuid.Nil, Model: "other-model", MaxRows: backfillBatch})

				return fmt.Errorf("run query: %w", err)
			},
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

			for _, want := range tt.wantPlan {
				require.Contains(t, explain.Plan, want)
			}

			for _, table := range tt.noSeqScanOn {
				require.NotContains(t, explain.Plan, "Seq Scan on "+table)
			}
		})
	}
}
