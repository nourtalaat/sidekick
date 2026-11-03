//go:build integration

package memory

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/prompt"
)

// pastInvestigation is the investigation of fixture/investigations.sql that
// the use tests read an example from; seedUses gives it a memory entry.
var pastInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")

func TestRecordAndUsedBy(t *testing.T) {
	t.Parallel()

	pool, examples, want := seedUses(t)

	got, err := UsedBy(t.Context(), pool, usedInvestigation, 2, MaxExamples)
	require.NoError(t, err)
	require.Equal(t, want, got)

	require.NoError(t, Record(t.Context(), pool, usedInvestigation, 2, examples), "recording again")

	got, err = UsedBy(t.Context(), pool, usedInvestigation, 2, MaxExamples)
	require.NoError(t, err)
	require.Equal(t, want, got, "recording again adds none")
}

func TestUsedByReadsOneRun(t *testing.T) {
	t.Parallel()

	pool, examples, _ := seedUses(t)

	got, err := UsedBy(t.Context(), pool, usedInvestigation, 1, MaxExamples)
	require.NoError(t, err)
	require.Empty(t, got, "run 1 recorded nothing")

	require.NoError(t, Record(t.Context(), pool, usedInvestigation, 3, examples[2:]))

	got, err = UsedBy(t.Context(), pool, usedInvestigation, 3, MaxExamples)
	require.NoError(t, err)
	require.Equal(t, []Use{{ExampleID: unknownExample, Match: prompt.MatchSimilar, Rank: 1}}, got, "run 3 has its own rows, ranked from 1")
}

func TestUsedByLimit(t *testing.T) {
	t.Parallel()

	pool, _, want := seedUses(t)

	got, err := UsedBy(t.Context(), pool, usedInvestigation, 2, 2)
	require.NoError(t, err)
	require.Equal(t, want[:2], got)
}

func TestUsedByAfterTheEntryIsDeleted(t *testing.T) {
	t.Parallel()

	pool, _, want := seedUses(t)

	_, err := pool.Exec(t.Context(), "DELETE FROM memory_entries WHERE investigation_id = $1", pastInvestigation)
	require.NoError(t, err)

	got, err := UsedBy(t.Context(), pool, usedInvestigation, 2, MaxExamples)
	require.NoError(t, err)
	require.Len(t, got, 3, "the use stays")
	require.Equal(t, Use{ExampleID: pastInvestigation, Match: prompt.MatchFingerprint, Rank: 1, Title: want[0].Title}, got[0],
		"it is not present, and the title still comes from the investigation")
}

func TestRecordWithNoExamples(t *testing.T) {
	t.Parallel()

	pool := setup(t, "fixture/investigations.sql")

	require.NoError(t, Record(t.Context(), pool, usedInvestigation, 1, nil))

	var n int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM memory_uses").Scan(&n))
	require.Zero(t, n)
}

func TestRecordRefusesABadExampleID(t *testing.T) {
	t.Parallel()

	pool := setup(t, "fixture/investigations.sql")

	err := Record(t.Context(), pool, usedInvestigation, 1, []prompt.Example{{InvestigationID: "nope", Match: prompt.MatchText}})
	require.Error(t, err)
}

// seedUses returns a database where the prompt of run 2 of usedInvestigation
// showed three examples, with the examples and the uses UsedBy should
// return. The first example has a memory entry and an investigation with a
// title; the second is a disabled entry with no investigation; the third has
// neither.
func seedUses(t *testing.T) (*pgxpool.Pool, []prompt.Example, []Use) {
	t.Helper()

	pool := setup(t, "fixture/investigations.sql", "fixture/entries.sql")

	_, err := pool.Exec(t.Context(), "UPDATE investigations SET alert_title = 'Checkout slow' WHERE id = $1", pastInvestigation)
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), `INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at)
VALUES ($1, 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'true_positive', 'Slow after a deploy.', 'Deploy 4f2c1a.', '2026-10-26 14:10:00+00')`, pastInvestigation)
	require.NoError(t, err)

	examples := []prompt.Example{
		{InvestigationID: pastInvestigation.String(), Match: prompt.MatchFingerprint},
		{InvestigationID: entryE5.String(), Match: prompt.MatchServiceAlert},
		{InvestigationID: unknownExample.String(), Match: prompt.MatchSimilar},
	}

	require.NoError(t, Record(t.Context(), pool, usedInvestigation, 2, examples))

	want := []Use{
		{ExampleID: pastInvestigation, Match: prompt.MatchFingerprint, Rank: 1, Present: true, Summary: "Slow after a deploy.", Cause: "Deploy 4f2c1a.", Verdict: "true_positive", Title: "Checkout slow"},
		{ExampleID: entryE5, Match: prompt.MatchServiceAlert, Rank: 2, Present: true, Summary: "Disabled: p99 latency rose in the US region.", Verdict: "true_positive", Disabled: true},
		{ExampleID: unknownExample, Match: prompt.MatchSimilar, Rank: 3},
	}

	return pool, examples, want
}
