//go:build integration

package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
)

// wantScore is a VerdictScore with its numbers worked out by hand. A nil
// precision or recall must be left out of the answer.
type wantScore struct {
	verdict                 string
	predicted, actual, hits int64
	precision, recall       *float64
}

// wantDay is a day of the answer with its rated count and scores.
type wantDay struct {
	day    string
	rated  int64
	scores []wantScore
}

// The days of stats_quality_days.sql with their numbers worked out by hand.
var (
	dayTwenty = wantDay{day: "2026-10-20", rated: 13, scores: []wantScore{
		{"true_positive", 3, 5, 3, new(1.0), new(0.6)},
		{"false_positive", 10, 8, 8, new(0.8), new(1.0)},
	}}

	// Unknown counts for neither precision nor recall, so the two
	// unknown ratings are in rated and in no score.
	dayTwentyOne = wantDay{day: "2026-10-21", rated: 7, scores: []wantScore{
		{"true_positive", 1, 1, 1, new(1.0), new(1.0)},
		{"false_positive", 4, 4, 4, new(1.0), new(1.0)},
	}}

	// Duplicate was confirmed three times and never given: recall is
	// defined and precision is absent.
	dayTwentyTwo = wantDay{day: "2026-10-22", rated: 4, scores: []wantScore{
		{"true_positive", 4, 1, 1, new(0.25), new(1.0)},
		{"duplicate", 0, 3, 0, nil, new(0.0)},
	}}

	// Only unknown ratings: the day stays in the answer without scores.
	dayTwentyThree = wantDay{day: "2026-10-23", rated: 2, scores: []wantScore{}}
)

func TestGetQualityScoresByDay(t *testing.T) {
	t.Parallel()

	s := setupWith(t, func(o *api.Options) { o.Now = func() time.Time { return statsNow } }, "fixture/stats_quality_days.sql")

	tests := []struct {
		name  string
		query string
		// wantDays is nil when only the sum over the days is checked.
		wantDays []wantDay
		wantLen  int
	}{
		{
			name:     "the default range",
			wantDays: []wantDay{dayTwenty, dayTwentyOne, dayTwentyTwo, dayTwentyThree},
			wantLen:  4,
		},
		{
			name:  "a route",
			query: "?route=r2",
			wantDays: []wantDay{
				{day: "2026-10-20", rated: 3, scores: []wantScore{{"true_positive", 3, 3, 3, new(1.0), new(1.0)}}},
				{day: "2026-10-21", rated: 1, scores: []wantScore{{"true_positive", 1, 1, 1, new(1.0), new(1.0)}}},
				dayTwentyThree,
			},
			wantLen: 3,
		},
		{
			name:  "a service",
			query: "?service=payments",
			wantDays: []wantDay{
				{day: "2026-10-20", rated: 3, scores: []wantScore{{"true_positive", 3, 3, 3, new(1.0), new(1.0)}}},
				{day: "2026-10-21", rated: 1, scores: []wantScore{{"true_positive", 1, 1, 1, new(1.0), new(1.0)}}},
				dayTwentyThree,
			},
			wantLen: 3,
		},
		{
			name:  "a model",
			query: "?model=m1",
			wantDays: []wantDay{
				{day: "2026-10-20", rated: 10, scores: []wantScore{
					{"true_positive", 0, 2, 0, nil, new(0.0)},
					{"false_positive", 10, 8, 8, new(0.8), new(1.0)},
				}},
				{day: "2026-10-21", rated: 6, scores: []wantScore{{"false_positive", 4, 4, 4, new(1.0), new(1.0)}}},
				dayTwentyTwo,
			},
			wantLen: 3,
		},
		{
			// Only the investigations where Sidekick gave the verdict are
			// read, so a day's recall does not count the other verdicts.
			name:  "the verdict Sidekick gave",
			query: "?verdict=false_positive",
			wantDays: []wantDay{
				{day: "2026-10-20", rated: 10, scores: []wantScore{
					{"true_positive", 0, 2, 0, nil, new(0.0)},
					{"false_positive", 10, 8, 8, new(0.8), new(1.0)},
				}},
				{day: "2026-10-21", rated: 6, scores: []wantScore{{"false_positive", 4, 4, 4, new(1.0), new(1.0)}}},
				dayTwentyThree,
			},
			wantLen: 3,
		},
		{
			name:     "one day",
			query:    "?from=2026-10-22&to=2026-10-22",
			wantDays: []wantDay{dayTwentyTwo},
			wantLen:  1,
		},
		{
			name:     "filters together",
			query:    "?route=r1&verdict=true_positive",
			wantDays: []wantDay{{day: "2026-10-22", rated: 4, scores: dayTwentyTwo.scores}},
			wantLen:  1,
		},
		{name: "nothing matches", query: "?route=nowhere"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got qualityBody

			decodeStats(t, getStats(t, s, "/stats/quality"+tt.query), &got)

			require.Len(t, got.Days, tt.wantLen)

			for i, want := range tt.wantDays {
				require.Equal(t, want.day, got.Days[i].Day)
				require.Equal(t, want.rated, got.Days[i].Rated, want.day)
				requireScores(t, want.scores, got.Days[i].Scores, want.day)
			}

			requireDaysSumToRange(t, got)
		})
	}

	t.Run("a day without a score has an empty list, not null", func(t *testing.T) {
		t.Parallel()

		var got struct {
			Days []struct {
				Scores json.RawMessage `json:"scores"`
			} `json:"days"`
		}

		decodeStats(t, getStats(t, s, "/stats/quality?route=r2"), &got)

		require.Len(t, got.Days, 3)
		require.Equal(t, "[]", string(got.Days[2].Scores))
	})
}

// requireScores checks the scores of a day against the ones worked out by
// hand, in order.
func requireScores(t *testing.T, want []wantScore, got []verdictScoreBody, day string) {
	t.Helper()

	require.Len(t, got, len(want), day)

	for i, w := range want {
		g := got[i]
		require.Equal(t, w.verdict, g.Verdict, day)
		require.Equal(t, [3]int64{w.predicted, w.actual, w.hits}, [3]int64{g.Predicted, g.Actual, g.Hits}, day+" "+w.verdict)
		requireRatio(t, w.precision, g.Precision, day+" "+w.verdict+" precision")
		requireRatio(t, w.recall, g.Recall, day+" "+w.verdict+" recall")
	}
}

func requireRatio(t *testing.T, want, got *float64, msg string) {
	t.Helper()

	if want == nil {
		require.Nil(t, got, msg)

		return
	}

	require.NotNil(t, got, msg)
	require.InDelta(t, *want, *got, 1e-9, msg)
}

// requireDaysSumToRange checks that, for each verdict, the predicted, actual
// and hits of the days add up to the entry of perVerdict for the range.
func requireDaysSumToRange(t *testing.T, got qualityBody) {
	t.Helper()

	sums := map[string][3]int64{}

	for _, d := range got.Days {
		for _, sc := range d.Scores {
			sum := sums[sc.Verdict]
			sums[sc.Verdict] = [3]int64{sum[0] + sc.Predicted, sum[1] + sc.Actual, sum[2] + sc.Hits}
		}
	}

	want := map[string][3]int64{}
	for _, sc := range got.PerVerdict {
		want[sc.Verdict] = [3]int64{sc.Predicted, sc.Actual, sc.Hits}
	}

	require.Equal(t, want, sums)
}
