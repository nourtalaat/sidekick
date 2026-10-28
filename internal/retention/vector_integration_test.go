//go:build integration

package retention

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestMemoryRetentionRemovesEmbeddings checks that deleting old memory
// entries takes their embeddings with them, by cascade, and leaves those of
// the entries that stay.
func TestMemoryRetentionRemovesEmbeddings(t *testing.T) {
	t.Parallel()

	const vectorDimensions = 3

	pool := storetest.NewVector(t, vectorDimensions)
	storetest.LoadFixture(t, pool, "fixture/records.sql")

	// e1 and e2 are old, c1 is new; each has an embedding.
	_, err := pool.Exec(t.Context(), `
		INSERT INTO memory_embeddings (investigation_id, model, text_hash, dimensions, embedding, embedded_at)
		SELECT investigation_id, 'test-model', 'x', 3, '[1,2,3]', now() FROM memory_entries`)
	require.NoError(t, err)

	handler := New(Options{Pool: pool, Periods: testPeriods, BatchSize: 100, Now: func() time.Time { return testNow }})

	deleted, err := handler.deleteMemoryEntries(t.Context(), testNow.Add(-testPeriods.MemoryEntries), 100)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)

	rows, err := pool.Query(t.Context(), "SELECT right(investigation_id::text, 2) FROM memory_embeddings ORDER BY 1")
	require.NoError(t, err)

	var left []string

	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))

		left = append(left, id)
	}

	require.NoError(t, rows.Err())
	require.Equal(t, []string{"c1"}, left)
}
