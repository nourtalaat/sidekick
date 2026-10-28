//go:build integration

package memory

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// searchQuery matches the summaries of fixture/vector.sql by words, best
// first: f1, f2, f3, f4. "or" makes any word enough, as in memory__search.
const searchQuery = "checkout or latency or deploy or timeout"

// TestSearchFusesTheNearestEntries checks memory.Search, the search behind
// memory__search, on the entries of fixture/vector.sql: the fused order, the
// filters on the vector list, the limit and the fall back to full text. The
// orders are worked out as in TestExamplesFuseTheNearestEntries: the
// full-text list is f1, f2, f3, f4 and the vector list f3, f1, f5, f2, f4,
// so the fused order is f1, f3, f2, f4, f5. f6 is nearer than all of them,
// but its vector is from another model, so it is never found.
func TestSearchFusesTheNearestEntries(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/vector.sql")

	tests := []struct {
		name   string
		params SearchParams
		limit  int32
		// vectors is false for a search without vector memory.
		vectors bool
		want    []uuid.UUID
	}{
		{
			name:   "without vectors, the full-text order",
			params: SearchParams{Query: searchQuery},
			limit:  10,
			want:   []uuid.UUID{fusionF1, fusionF2, fusionF3, fusionF4},
		},
		{
			name:    "with vectors, the fused order, with the entry only the vectors found",
			params:  SearchParams{Query: searchQuery},
			limit:   10,
			vectors: true,
			want:    []uuid.UUID{fusionF1, fusionF3, fusionF2, fusionF4, fusionF5},
		},
		{
			name:    "the limit keeps the best of the fused order",
			params:  SearchParams{Query: searchQuery},
			limit:   3,
			vectors: true,
			want:    []uuid.UUID{fusionF1, fusionF3, fusionF2},
		},
		{
			name:    "a service filter applies to the vector list too",
			params:  SearchParams{Query: searchQuery, Service: new("billing")},
			limit:   10,
			vectors: true,
			// f3 is in both lists, f5 only in the vector list.
			want: []uuid.UUID{fusionF3, fusionF5},
		},
		{
			name:    "a verdict filter that keeps only an entry the vectors found",
			params:  SearchParams{Query: searchQuery, Verdict: new("false_positive")},
			limit:   10,
			vectors: true,
			want:    []uuid.UUID{fusionF5},
		},
		{
			name:    "a verdict filter that leaves out an entry the vectors found",
			params:  SearchParams{Query: searchQuery, Verdict: new("true_positive")},
			limit:   10,
			vectors: true,
			// The vector list is f3, f1, f2, f4 without f5.
			want: []uuid.UUID{fusionF1, fusionF3, fusionF2, fusionF4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var vec *Vectors
			if tt.vectors {
				vec = NewVectors(&recordingEmbedder{}, 0, testModel, nil)
			}

			rows, err := Search(t.Context(), pool, tt.params, tt.limit, vec)
			require.NoError(t, err)

			var got []uuid.UUID
			for _, r := range rows {
				got = append(got, r.InvestigationID)
			}

			require.Equal(t, tt.want, got)
		})
	}
}

// TestSearchFallsBackWhenTheEmbedderFails checks that a failing embeddings
// call gives the full-text rows alone, with a warning that does not hold the
// query.
func TestSearchFallsBackWhenTheEmbedderFails(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/vector.sql")

	var logs bytes.Buffer

	embedder := &recordingEmbedder{err: &model.Error{Kind: model.ErrOverloaded, Err: errors.New("503")}}
	vec := NewVectors(embedder, 0, testModel, slog.New(slog.NewTextHandler(&logs, nil)))

	rows, err := Search(t.Context(), pool, SearchParams{Query: searchQuery}, 10, vec)
	require.NoError(t, err)

	var got []uuid.UUID
	for _, r := range rows {
		got = append(got, r.InvestigationID)
	}

	require.Equal(t, []uuid.UUID{fusionF1, fusionF2, fusionF3, fusionF4}, got)
	require.Contains(t, logs.String(), "level=WARN")
	require.Contains(t, logs.String(), "using full text only")
	require.NotContains(t, logs.String(), "checkout")
}
