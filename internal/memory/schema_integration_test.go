//go:build integration

package memory

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Postgres error codes of the constraints memory_uses has.
const (
	checkViolation      = "23514"
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
)

// The investigations of fixture/investigations.sql that the schema tests
// use: used is the one whose prompt showed the examples.
var (
	usedInvestigation  = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c3")
	otherInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c2")
	unknownExample     = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000ef")
)

const insertMemoryUse = "INSERT INTO memory_uses (investigation_id, run, example_id, match, rank) VALUES ($1, $2, $3, $4, $5)"

func TestMemoryUsesConstraints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// first is a row inserted before the one under test.
		first    bool
		insert   func(t *testing.T) []any
		wantCode string
	}{
		{
			name:   "a row for an example that has no memory entry",
			insert: func(t *testing.T) []any { return []any{usedInvestigation, 1, unknownExample, "text", 1} },
		},
		{
			name:     "a match that no prompt uses",
			insert:   func(t *testing.T) []any { return []any{usedInvestigation, 1, entryE1, "random", 1} },
			wantCode: checkViolation,
		},
		{
			name:     "a rank of zero",
			insert:   func(t *testing.T) []any { return []any{usedInvestigation, 1, entryE1, "text", 0} },
			wantCode: checkViolation,
		},
		{
			name:     "an investigation that does not exist",
			insert:   func(t *testing.T) []any { return []any{unknownExample, 1, entryE1, "text", 1} },
			wantCode: foreignKeyViolation,
		},
		{
			name:     "the same example twice in one run",
			first:    true,
			insert:   func(t *testing.T) []any { return []any{usedInvestigation, 1, entryE1, "similar", 2} },
			wantCode: uniqueViolation,
		},
		{
			name:   "the same example in another run",
			first:  true,
			insert: func(t *testing.T) []any { return []any{usedInvestigation, 2, entryE1, "fingerprint", 1} },
		},
		{
			name:   "the same example for another investigation",
			first:  true,
			insert: func(t *testing.T) []any { return []any{otherInvestigation, 1, entryE1, "fingerprint", 1} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t, "fixture/investigations.sql", "fixture/entries.sql")

			if tt.first {
				_, err := pool.Exec(t.Context(), insertMemoryUse, usedInvestigation, 1, entryE1, "fingerprint", 1)
				require.NoError(t, err)
			}

			_, err := pool.Exec(t.Context(), insertMemoryUse, tt.insert(t)...)
			if tt.wantCode == "" {
				require.NoError(t, err)

				return
			}

			var pgErr *pgconn.PgError

			require.ErrorAs(t, err, &pgErr)
			require.Equal(t, tt.wantCode, pgErr.Code)
		})
	}
}

// TestMemoryUsesOutliveEntries checks that deleting a memory entry leaves
// the uses of it, and that deleting an investigation takes its uses and
// leaves those of the others.
func TestMemoryUsesOutliveEntries(t *testing.T) {
	t.Parallel()

	pool := setup(t, "fixture/investigations.sql", "fixture/entries.sql")

	for _, inv := range []uuid.UUID{usedInvestigation, otherInvestigation} {
		_, err := pool.Exec(t.Context(), insertMemoryUse, inv, 1, entryE1, "fingerprint", 1)
		require.NoError(t, err)
	}

	count := func(inv uuid.UUID) (n int) {
		require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM memory_uses WHERE investigation_id = $1", inv).Scan(&n))

		return n
	}

	_, err := pool.Exec(t.Context(), "DELETE FROM memory_entries WHERE investigation_id = $1", entryE1)
	require.NoError(t, err)
	require.Equal(t, 1, count(usedInvestigation), "deleting the entry leaves its uses")

	_, err = pool.Exec(t.Context(), "DELETE FROM investigations WHERE id = $1", usedInvestigation)
	require.NoError(t, err)
	require.Equal(t, 0, count(usedInvestigation), "deleting the investigation takes its uses")
	require.Equal(t, 1, count(otherInvestigation), "the uses of another investigation stay")
}
