package tools

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// maxKeys is how many keys Do keeps limits for at once. A watcher host
// pattern such as *.example.com allows any number of names, so the map of
// keys needs a bound.
const maxKeys = 1024

// keyed is the concurrency limit and circuit breaker of one Do key.
type keyed struct {
	slots   chan struct{}
	breaker *breaker
	// inFlight counts the Do calls using the key. Guarded by Guard.mu.
	inFlight int
}

// Do runs fn with the same failure isolation as a source tool call, for
// guarded work that is not one, such as the HTTP probes of watchers. Calls
// with the same key share a limit of limit calls at once, set by the first
// call that uses the key, and a circuit breaker. fn gets a context that ends
// after timeout, and must return soon after it ends: Do waits for fn, so
// whatever fn writes is safe to read once Do returns.
//
// Do returns nil when fn returned nil. Otherwise the ToolError says why, with
// the key as its Source: KindTimeout when the timeout ended first,
// KindUnavailable when the key's circuit is open or the guard holds limits
// for maxKeys other keys that are all in use, and KindFailed when fn failed,
// panicked or ctx ended. An error from fn counts as a failure for the
// breaker, so fn should return nil for an answer it got, even a bad one.
func (g *Guard) Do(ctx context.Context, key string, limit int, timeout time.Duration, fn func(ctx context.Context) error) *ToolError {
	k := g.acquireKey(key, limit)
	if k == nil {
		g.metrics.recordKeysFull()

		return &ToolError{Kind: KindUnavailable, Source: key, Message: "too many other targets are in use, so this one is skipped for now"}
	}

	defer g.releaseKey(k)

	ok, trial := k.breaker.allow()
	if !ok {
		return &ToolError{Kind: KindUnavailable, Source: key, Message: fmt.Sprintf("the target failed %d times in a row, so it is skipped for now", breakerFailures)}
	}

	o := g.doLimited(ctx, key, k, timeout, fn)
	k.breaker.done(o.health, trial)

	return o.err
}

// acquireKey returns the limits of key, adding them when they are new, and
// counts a call in flight. When the guard already holds maxKeys keys, it
// first drops those with no call in flight and a closed circuit; it returns
// nil when none can be dropped.
func (g *Guard) acquireKey(key string, limit int) *keyed {
	g.mu.Lock()
	defer g.mu.Unlock()

	k, ok := g.keys[key]
	if !ok {
		if len(g.keys) >= g.maxKeys {
			g.dropIdleKeys()
		}

		if len(g.keys) >= g.maxKeys {
			return nil
		}

		k = &keyed{slots: make(chan struct{}, max(limit, 1)), breaker: &breaker{now: g.now}}
		g.keys[key] = k
	}

	k.inFlight++

	return k
}

// releaseKey ends a call that acquireKey counted.
func (g *Guard) releaseKey(k *keyed) {
	g.mu.Lock()
	defer g.mu.Unlock()

	k.inFlight--
}

// dropIdleKeys drops the keys with no call in flight and a closed circuit.
// The caller holds g.mu.
func (g *Guard) dropIdleKeys() {
	for key, k := range g.keys {
		if k.inFlight == 0 && !k.breaker.open() {
			delete(g.keys, key)
		}
	}
}

// doLimited waits for a free slot of the key and runs fn within timeout.
func (g *Guard) doLimited(ctx context.Context, key string, k *keyed, timeout time.Duration, fn func(ctx context.Context) error) outcome {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case k.slots <- struct{}{}:
	case <-callCtx.Done():
		o := g.doEnded(ctx, key, timeout)
		o.health = callNeutral

		return o
	}

	defer func() { <-k.slots }()

	err := safeDo(callCtx, fn)

	switch {
	case err == nil:
		return outcome{health: callOK}
	case callCtx.Err() != nil:
		return g.doEnded(ctx, key, timeout)
	case errors.Is(err, errPanic):
		g.logger.Error("a guarded call panicked", "key", key, "error", err)

		return outcome{err: &ToolError{Kind: KindFailed, Source: key, Message: "the call failed unexpectedly"}, health: callFailed}
	default:
		return outcome{err: &ToolError{Kind: KindFailed, Source: key, Message: g.redactMessage(err.Error())}, health: callFailed}
	}
}

// doEnded returns the outcome of a Do call whose context ended: cancelled
// when the caller's context ended, a timeout otherwise.
func (g *Guard) doEnded(ctx context.Context, key string, timeout time.Duration) outcome {
	if ctx.Err() != nil {
		return outcome{err: &ToolError{Kind: KindFailed, Source: key, Message: "the call was cancelled"}, health: callNeutral}
	}

	return outcome{
		err:    &ToolError{Kind: KindTimeout, Source: key, Message: fmt.Sprintf("the target did not answer within %s", timeout)},
		health: callFailed,
	}
}

// safeDo calls fn and turns a panic into an error.
func safeDo(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	defer recoverInto(&err)

	return fn(ctx)
}
