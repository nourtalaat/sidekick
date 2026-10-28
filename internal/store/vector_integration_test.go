//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// vectorTestDimensions is the length of the vectors in these tests.
const vectorTestDimensions = 3

// insertEmbeddedEntry adds a memory entry with an embedding of the test
// length and returns its ID.
func insertEmbeddedEntry(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	id := ids.New().String()

	_, err := pool.Exec(t.Context(),
		`INSERT INTO memory_entries (investigation_id, fingerprint, verdict, summary, confirmed_at) VALUES ($1, 'fp', 'true_positive', 'summary', now())`, id)
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(),
		`INSERT INTO memory_embeddings (investigation_id, model, dimensions, embedding, embedded_at) VALUES ($1, 'm', 3, '[1,2,3]', now())`, id)
	require.NoError(t, err)

	return id
}

func TestMigrateVector(t *testing.T) {
	t.Parallel()

	pool := storetest.NewVector(t, vectorTestDimensions)

	require.Equal(t, 1, count(t, pool, "SELECT count(*) FROM pg_extension WHERE extname = 'vector'"))
	require.Equal(t, 1, count(t, pool, "SELECT count(*) FROM pg_indexes WHERE indexname = 'memory_embeddings_embedding_hnsw'"), "the HNSW index exists")
	require.Equal(t, vectorTestDimensions, count(t, pool,
		"SELECT atttypmod FROM pg_attribute WHERE attrelid = 'memory_embeddings'::regclass AND attname = 'embedding'"), "the column has the length")

	current, target, err := store.VectorSchemaVersion(t.Context(), pool)
	require.NoError(t, err)
	require.Equal(t, int64(1), current)
	require.Equal(t, int64(1), target)

	statuses, err := store.VectorMigrationStatus(t.Context(), pool)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, 1, count(t, pool, "SELECT count(*) FROM "+store.VectorVersionTable+" WHERE version_id = 1"))

	// Running it again changes nothing.
	require.NoError(t, store.MigrateVector(t.Context(), pool, vectorTestDimensions))
	require.NoError(t, store.CheckVectorDimensions(t.Context(), pool, vectorTestDimensions))

	// Another length is refused, with the way to embed again.
	for _, err := range []error{
		store.MigrateVector(t.Context(), pool, vectorTestDimensions+1),
		store.CheckVectorDimensions(t.Context(), pool, vectorTestDimensions+1),
	} {
		var dimErr *store.VectorDimensionsError
		require.ErrorAs(t, err, &dimErr)
		require.Equal(t, vectorTestDimensions, dimErr.Stored)
		require.Equal(t, vectorTestDimensions+1, dimErr.Configured)
		require.ErrorContains(t, err, "sidekick migrate vector-reset")
	}
}

// TestVectorsOffNeedNoExtension checks that a database that never ran the
// vector migrations has no pgvector and no embeddings table, and that the
// check of the length finds nothing wrong with it.
func TestVectorsOffNeedNoExtension(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)

	require.Zero(t, count(t, pool, "SELECT count(*) FROM pg_extension WHERE extname = 'vector'"))
	require.Zero(t, count(t, pool, "SELECT count(*) FROM pg_tables WHERE tablename = 'memory_embeddings'"))
	require.Zero(t, count(t, pool, "SELECT count(*) FROM pg_tables WHERE tablename = $1", store.VectorVersionTable))
	require.NoError(t, store.CheckVectorDimensions(t.Context(), pool, vectorTestDimensions))
}

func TestEmbeddingsGoWithTheirEntry(t *testing.T) {
	t.Parallel()

	pool := storetest.NewVector(t, vectorTestDimensions)
	id := insertEmbeddedEntry(t, pool)

	require.Equal(t, 1, count(t, pool, "SELECT count(*) FROM memory_embeddings"))

	_, err := pool.Exec(t.Context(), "DELETE FROM memory_entries WHERE investigation_id = $1", id)
	require.NoError(t, err)

	require.Zero(t, count(t, pool, "SELECT count(*) FROM memory_embeddings"), "the cascade removed the embedding")
}

func TestResetVectors(t *testing.T) {
	t.Parallel()

	pool := storetest.NewVector(t, vectorTestDimensions)
	insertEmbeddedEntry(t, pool)

	var recorded []int

	old, err := store.ResetVectors(t.Context(), pool, func(_ context.Context, _ pgx.Tx, oldDimensions int) error {
		recorded = append(recorded, oldDimensions)

		return nil
	})
	require.NoError(t, err)
	require.Equal(t, vectorTestDimensions, old)
	require.Equal(t, []int{vectorTestDimensions}, recorded)

	require.Zero(t, count(t, pool, "SELECT count(*) FROM memory_embeddings"), "the table is empty")
	require.Zero(t, count(t, pool, "SELECT count(*) FROM pg_indexes WHERE indexname = 'memory_embeddings_embedding_hnsw'"), "the index is gone")
	require.NoError(t, store.CheckVectorDimensions(t.Context(), pool, vectorTestDimensions+2), "the column has no length")

	// The next start gives the column the new length.
	require.NoError(t, store.MigrateVector(t.Context(), pool, vectorTestDimensions+1))
	require.Equal(t, vectorTestDimensions+1, count(t, pool,
		"SELECT atttypmod FROM pg_attribute WHERE attrelid = 'memory_embeddings'::regclass AND attname = 'embedding'"))
	require.Equal(t, 1, count(t, pool, "SELECT count(*) FROM pg_indexes WHERE indexname = 'memory_embeddings_embedding_hnsw'"))
}

func TestResetVectorsRollsBackWhenRecordFails(t *testing.T) {
	t.Parallel()

	pool := storetest.NewVector(t, vectorTestDimensions)
	insertEmbeddedEntry(t, pool)

	_, err := store.ResetVectors(t.Context(), pool, func(context.Context, pgx.Tx, int) error {
		return context.Canceled
	})
	require.ErrorIs(t, err, context.Canceled)

	require.Equal(t, 1, count(t, pool, "SELECT count(*) FROM memory_embeddings"), "nothing was emptied")
	require.NoError(t, store.CheckVectorDimensions(t.Context(), pool, vectorTestDimensions))
}
