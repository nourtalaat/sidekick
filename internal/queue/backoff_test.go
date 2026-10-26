package queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// backoffSamples is how many random waits TestBackoffStaysWithinJitter
// checks for each attempt count.
const backoffSamples = 1000

func TestJitteredBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		attempts int32
		r        float64
		want     time.Duration
	}{
		{name: "first retry, shortest", attempts: 0, r: 0, want: 4 * time.Second},
		{name: "first retry, middle", attempts: 0, r: 0.5, want: 5 * time.Second},
		{name: "first retry, near longest", attempts: 0, r: 0.999, want: 5998 * time.Millisecond},
		{name: "second retry doubles", attempts: 1, r: 0.5, want: 10 * time.Second},
		{name: "fourth retry", attempts: 3, r: 0.5, want: 40 * time.Second},
		{name: "last doubling under the cap", attempts: 6, r: 0.5, want: 320 * time.Second},
		{name: "capped at ten minutes", attempts: 7, r: 0.5, want: 10 * time.Minute},
		{name: "capped, shortest", attempts: 7, r: 0, want: 8 * time.Minute},
		{name: "capped, near longest", attempts: 7, r: 0.999, want: 719760 * time.Millisecond},
		{name: "many attempts stay capped", attempts: 1000, r: 0.5, want: 10 * time.Minute},
		{name: "negative attempts act as zero", attempts: -3, r: 0.5, want: 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.InDelta(t, tt.want, jitteredBackoff(tt.attempts, tt.r), float64(time.Millisecond))
		})
	}
}

func TestBackoffStaysWithinJitter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		attempts int32
		min, max time.Duration
	}{
		{name: "first retry", attempts: 0, min: 4 * time.Second, max: 6 * time.Second},
		{name: "third retry", attempts: 2, min: 16 * time.Second, max: 24 * time.Second},
		{name: "capped", attempts: 20, min: 8 * time.Minute, max: 12 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for range backoffSamples {
				wait := Backoff(tt.attempts)
				require.GreaterOrEqual(t, wait, tt.min)
				require.Less(t, wait, tt.max)
			}
		})
	}
}
