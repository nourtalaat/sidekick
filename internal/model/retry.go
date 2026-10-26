package model

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// Default backoff of the retry helper, used when a Retrier leaves them zero.
const (
	DefaultRetryBaseDelay = time.Second
	DefaultRetryMaxDelay  = time.Minute
)

// maxBackoffDoublings bounds the shift in Retrier.delay so it cannot
// overflow; the cap is reached long before.
const maxBackoffDoublings = 30

// Retrier repeats a model call that failed with a retryable *Error: rate
// limited, overloaded or timed out. It waits for the error's
// RetryAfter when the provider gave one, and otherwise for an exponential
// backoff with jitter. Other errors are returned at once.
type Retrier struct {
	// Retries is how many times a failed call is repeated, so a call runs at
	// most Retries+1 times. It is the retries setting of the connector.
	Retries int
	// BaseDelay is the backoff before the first retry. It doubles for each
	// later retry. Zero means DefaultRetryBaseDelay.
	BaseDelay time.Duration
	// MaxDelay caps the backoff, not RetryAfter. Zero means DefaultRetryMaxDelay.
	MaxDelay time.Duration

	// wait and jitter are replaced in tests.
	wait   func(ctx context.Context, d time.Duration) error
	jitter func(d time.Duration) time.Duration
}

// Generate runs req on c and repeats it up to retries times while it fails
// with a retryable *Error, using the default backoff. It stops when ctx is
// done. The returned error wraps the last *Error, so errors.As finds it.
func Generate(ctx context.Context, c Connector, req Request, retries int) (Response, error) {
	return Retrier{Retries: retries}.Generate(ctx, c, req)
}

// Generate runs req on c with the retry policy of r.
func (r Retrier) Generate(ctx context.Context, c Connector, req Request) (Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}

		var merr *Error
		if !errors.As(err, &merr) || !merr.Retryable() || attempt >= r.Retries {
			return Response{}, fmt.Errorf("generate (attempt %d of %d): %w", attempt+1, r.Retries+1, err)
		}

		if werr := r.waitFor(ctx, r.delay(attempt, merr)); werr != nil {
			return Response{}, fmt.Errorf("wait to retry after %w: %w", err, werr)
		}
	}
}

// delay returns how long to wait before retry number attempt+1.
func (r Retrier) delay(attempt int, err *Error) time.Duration {
	if err.RetryAfter > 0 {
		return err.RetryAfter
	}

	base := r.BaseDelay
	if base <= 0 {
		base = DefaultRetryBaseDelay
	}

	maxDelay := r.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultRetryMaxDelay
	}

	d := maxDelay
	if attempt < maxBackoffDoublings && base<<attempt < maxDelay {
		d = base << attempt
	}

	if r.jitter != nil {
		return r.jitter(d)
	}

	return halfJitter(d)
}

// waitFor waits for d or until ctx is done.
func (r Retrier) waitFor(ctx context.Context, d time.Duration) error {
	if r.wait != nil {
		return r.wait(ctx, d)
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("context done: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

// halfJitter returns a random duration between d/2 and d, so replicas that
// failed together do not retry together.
func halfJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}

	return half + rand.N(half+1) //nolint:gosec // Jitter needs no cryptographic randomness.
}
