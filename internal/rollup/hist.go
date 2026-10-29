package rollup

import "math"

const (
	// histBase and histGrowth define the buckets of the histograms of
	// stats_hourly: bucket 0 holds times up to histBase milliseconds, and
	// each later bucket ends histGrowth times as late as the one before.
	// They are the numbers rollup_hist_bucket uses.
	histBase   = 500.0
	histGrowth = 1.6
	// HistBuckets is the number of buckets of a histogram of stats_hourly.
	HistBuckets = 24
)

// HistCounts puts the rows of a histogram, whose buckets count from 1, in
// a slice of HistBuckets counts.
func HistCounts[T any](rows []T, of func(T) (int32, int64)) []int64 {
	counts := make([]int64, HistBuckets)

	for _, r := range rows {
		bucket, n := of(r)
		if bucket >= 1 && bucket <= HistBuckets {
			counts[bucket-1] += n
		}
	}

	return counts
}

// HistMedian returns about the median of the values a histogram counts, in
// milliseconds, or nil when it counts none. It finds the bucket the middle
// value is in and places the value in it in proportion to its rank among
// the bucket's values, so it is exact only to the width of a bucket.
func HistMedian(counts []int64) *float64 {
	var total int64
	for _, n := range counts {
		total += n
	}

	if total == 0 {
		return nil
	}

	target := float64(total) / 2

	var before int64

	for i, n := range counts {
		if n == 0 || float64(before+n) < target {
			before += n

			continue
		}

		lo, hi := histBounds(i)
		fraction := (target - float64(before)) / float64(n)

		return new(lo + fraction*(hi-lo))
	}

	return nil
}

// histBounds returns the lower and upper end of bucket i, in milliseconds.
func histBounds(i int) (float64, float64) {
	hi := histBase * math.Pow(histGrowth, float64(i))
	if i == 0 {
		return 0, hi
	}

	return histBase * math.Pow(histGrowth, float64(i-1)), hi
}
