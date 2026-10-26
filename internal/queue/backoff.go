package queue

import (
	"math/rand/v2"
	"time"
)

const (
	// backoffBase is the wait before the first retry.
	backoffBase = 5 * time.Second
	// backoffCap is the longest wait between retries, before jitter.
	backoffCap = 10 * time.Minute
	// backoffJitter is the random share added to or taken from the wait:
	// 0.2 means within 20% either way.
	backoffJitter = 0.2
	// backoffMaxDoublings is the number of doublings that already passes
	// backoffCap, so larger attempt counts skip the shift and cannot
	// overflow.
	backoffMaxDoublings = 8
)

// Backoff returns how long a job waits before its next try after a failure,
// given the attempts it had used before that failure: min(5s × 2^attempts,
// 10m), made up to 20% shorter or longer at random so that jobs that failed
// together do not retry together.
func Backoff(attempts int32) time.Duration {
	return jitteredBackoff(attempts, rand.Float64()) //nolint:gosec // Jitter needs no secure random numbers.
}

// jitteredBackoff is Backoff with the random number r, in [0, 1), passed
// in: 0 gives the shortest wait and values near 1 the longest.
func jitteredBackoff(attempts int32, r float64) time.Duration {
	wait := backoffCap
	if attempts < backoffMaxDoublings {
		wait = min(backoffBase<<max(attempts, 0), backoffCap)
	}

	factor := 1 - backoffJitter + 2*backoffJitter*r

	return time.Duration(float64(wait) * factor)
}
