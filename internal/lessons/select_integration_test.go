//go:build integration

package lessons

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// eventText is the text of the event the lessons of fixture/lessons.sql are
// ranked against.
const eventText = "checkout-api p99 latency above 2s. p99 is 2.6 s. The database connection pool is exhausted."

// noLimits lets every lesson that applies through.
var noLimits = Limits{MaxPerPrompt: MaxPerPromptLimit, MaxTokens: MaxTokensLimit, GlobalPercent: 100}

// lessonID returns the ID of lesson n in fixture/lessons.sql.
func lessonID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("0192d3a4-0000-7000-8000-%012d", n))
}

// lessonIDs returns the IDs of the lessons with the numbers.
func lessonIDs(numbers ...int) []uuid.UUID {
	out := make([]uuid.UUID, len(numbers))
	for i, n := range numbers {
		out[i] = lessonID(n)
	}

	return out
}

func TestSelectRanking(t *testing.T) {
	t.Parallel()

	checkout := Subject{Service: "checkout-api", AlertName: "CheckoutLatencyP99", Route: "payments", Strategy: "latency-alert", Text: eventText}

	tests := []struct {
		name    string
		subject Subject
		// want are the numbers of the lessons, in rank order.
		want []int
	}{
		{
			// 14 sets all four fields, then 16 and 1 set two (16 is newer),
			// then those with one field by helpful minus unhelpful (2, then
			// 3), then, at 0, 21 for its words in common with the event,
			// then the newest first, and the global ones last, 8 with the
			// best count first, though it is higher than any scoped one.
			name:    "all the ways to rank",
			subject: checkout,
			want:    []int{14, 16, 1, 2, 3, 21, 31, 32, 7, 6, 5, 8, 9},
		},
		{
			name:    "without event text the words count for nothing",
			subject: Subject{Service: checkout.Service, AlertName: checkout.AlertName, Route: checkout.Route, Strategy: checkout.Strategy},
			want:    []int{14, 16, 1, 2, 3, 31, 32, 7, 6, 5, 21, 8, 9},
		},
		{
			name:    "a lesson applies only when all its fields match",
			subject: Subject{Service: "checkout-api", AlertName: "CheckoutLatencyP99", Route: "billing", Strategy: "other", Text: eventText},
			want:    []int{1, 2, 3, 21, 31, 32, 5, 8, 9},
		},
		{
			name:    "another service gets its own lesson and the global ones",
			subject: Subject{Service: "search-api", AlertName: "X", Route: "r", Strategy: "s", Text: eventText},
			want:    []int{10, 8, 9},
		},
		{
			name:    "a subject with no values gets only the global lessons",
			subject: Subject{},
			want:    []int{8, 9},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			storetest.LoadFixture(t, pool, "fixture/lessons.sql")

			got, err := NewSelector(noLimits).Select(t.Context(), pool, tt.subject)
			require.NoError(t, err)
			require.Equal(t, lessonIDs(tt.want...), idsOf(got.Used))
			require.Equal(t, len(tt.want), got.Candidates)
			require.Empty(t, got.LeftOut)
		})
	}
}

func TestSelectLimits(t *testing.T) {
	t.Parallel()

	subject := Subject{Service: "checkout-api", AlertName: "CheckoutLatencyP99", Route: "payments", Strategy: "latency-alert", Text: eventText}

	pool := setup(t)
	storetest.LoadFixture(t, pool, "fixture/lessons.sql")

	got, err := NewSelector(Limits{MaxPerPrompt: 3, MaxTokens: 1500, GlobalPercent: 30}).Select(t.Context(), pool, subject)
	require.NoError(t, err)
	require.Equal(t, lessonIDs(14, 16, 1), idsOf(got.Used))
	require.Equal(t, 13, got.Candidates)
	require.Equal(t, 10, got.LeftOutTotal)
	require.Len(t, got.LeftOut, 10)
	require.Equal(t, LeftOut{ID: lessonID(2), Reason: ReasonMaxPerPrompt, Scope: Scope{Service: "checkout-api"}}, got.LeftOut[0])

	for _, l := range got.LeftOut {
		require.Equal(t, ReasonMaxPerPrompt, l.Reason)
	}
}

func TestRecord(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	storetest.LoadFixture(t, pool, "fixture/lessons.sql")

	sel, err := NewSelector(noLimits).Select(t.Context(), pool, Subject{Service: "search-api", Text: eventText})
	require.NoError(t, err)
	require.Equal(t, lessonIDs(10, 8, 9), idsOf(sel.Used))

	// Recording twice, as a retried step would, keeps one row per lesson.
	for range 2 {
		require.NoError(t, Record(t.Context(), pool, finalInvestigation, sel))
	}

	var used []uuid.UUID

	rows, err := pool.Query(t.Context(), "SELECT lesson_id FROM lesson_uses WHERE investigation_id = $1 ORDER BY lesson_id", finalInvestigation)
	require.NoError(t, err)

	for rows.Next() {
		var id uuid.UUID

		require.NoError(t, rows.Scan(&id))

		used = append(used, id)
	}

	require.NoError(t, rows.Err())
	require.Equal(t, lessonIDs(8, 9, 10), used)

	// The time of use is set for the lessons used, and for no other.
	var stamped, unstamped int

	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT count(*) FILTER (WHERE last_used_at IS NOT NULL), count(*) FILTER (WHERE last_used_at IS NULL) FROM lessons").Scan(&stamped, &unstamped))
	require.Equal(t, 3, stamped)
	require.Equal(t, 16, unstamped)

	// Another investigation using the same lessons adds its own rows.
	require.NoError(t, Record(t.Context(), pool, runningInvestigation, sel))

	var count int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM lesson_uses").Scan(&count))
	require.Equal(t, 6, count)
}

func TestRecordNothing(t *testing.T) {
	t.Parallel()

	pool := setup(t)

	require.NoError(t, Record(t.Context(), pool, finalInvestigation, Selection{}))

	var count int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM lesson_uses").Scan(&count))
	require.Zero(t, count)
}

func TestNilSelectorChoosesNothing(t *testing.T) {
	t.Parallel()

	var s *Selector

	got, err := s.Select(t.Context(), nil, Subject{Service: "x"})
	require.NoError(t, err)
	require.Empty(t, got.Used)
}
