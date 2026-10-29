package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistMedian(t *testing.T) {
	t.Parallel()

	counted := func(perBucket map[int]int64) []int64 {
		c := make([]int64, histBuckets)
		for bucket, n := range perBucket {
			c[bucket] = n
		}

		return c
	}

	tests := []struct {
		name   string
		counts []int64
		// want is nil for no median.
		want *float64
	}{
		{name: "no values", counts: make([]int64, histBuckets)},
		{name: "one value in the first bucket", counts: counted(map[int]int64{0: 1}), want: new(250.0)},
		{name: "two values in the first bucket", counts: counted(map[int]int64{0: 2}), want: new(250.0)},
		{name: "one value in the second bucket", counts: counted(map[int]int64{1: 1}), want: new(650.0)},
		{name: "four values in the third bucket, the middle one is the second", counts: counted(map[int]int64{2: 4}), want: new(800 + 0.5*(1280-800))},
		{
			name:   "the middle value is in the later bucket, the first of its three",
			counts: counted(map[int]int64{0: 1, 1: 3}),
			want:   new(500 + (1.0/3.0)*(800-500)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := histMedian(tt.counts)

			if tt.want == nil {
				require.Nil(t, got)

				return
			}

			require.NotNil(t, got)
			require.InDelta(t, *tt.want, *got, 1e-9)
		})
	}
}

func TestHistBounds(t *testing.T) {
	t.Parallel()

	lo, hi := histBounds(0)
	require.InDelta(t, 0, lo, 0)
	require.InDelta(t, 500, hi, 1e-9)

	lo, hi = histBounds(1)
	require.InDelta(t, 500, lo, 1e-9)
	require.InDelta(t, 800, hi, 1e-9)

	// The last bucket ends near four and a half hours, as the rollup's
	// histogram of downtimes needs.
	lo, hi = histBounds(histBuckets - 1)
	require.Greater(t, lo, float64(4*time.Hour/time.Millisecond))
	require.Greater(t, hi, lo)
}

func TestStatsWindow(t *testing.T) {
	t.Parallel()

	w := statsWindow{
		from: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		to:   time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC),
	}

	require.Equal(t, 7, w.days())
	require.Equal(t, statsWindow{
		from: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		to:   time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
	}, w.before())
	require.Equal(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), w.end())
	require.Equal(t, 1, statsWindow{from: w.from, to: w.from}.days())
}
