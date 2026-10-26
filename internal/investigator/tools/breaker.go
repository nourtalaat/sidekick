package tools

import (
	"sync"
	"time"
)

// Circuit breaker settings (RFC 9.2).
const (
	// breakerFailures is how many failures in a row open the circuit.
	breakerFailures = 5
	// breakerOpenFor is how long an open circuit fails calls at once
	// before one trial call may go through.
	breakerOpenFor = 60 * time.Second
)

// callResult is what a finished call tells the breaker.
type callResult int

const (
	// callOK: the source answered, even with a tool error of its own.
	callOK callResult = iota
	// callFailed: the source failed, timed out or panicked.
	callFailed
	// callNeutral: the call says nothing about the source, for example
	// because the investigation was cancelled.
	callNeutral
)

// breaker is one source's circuit breaker. After breakerFailures failures
// in a row it opens: calls fail at once for breakerOpenFor. Then one trial
// call goes through; success closes the circuit, failure opens it again.
// It is safe for concurrent use.
type breaker struct {
	now func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	trial     bool
}

// allow reports whether a call may go to the source, and whether it is
// the trial call of an open circuit. The caller reports the call's result
// with done.
func (b *breaker) allow() (ok, trial bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch {
	case b.failures < breakerFailures:
		return true, false
	case b.trial || b.now().Before(b.openUntil):
		return false, false
	default:
		b.trial = true

		return true, true
	}
}

// done records the result of a call that allow let through.
func (b *breaker) done(r callResult, trial bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if trial {
		b.trial = false
	}

	switch r {
	case callOK:
		b.failures = 0
	case callFailed:
		b.failures++
		if b.failures >= breakerFailures {
			b.openUntil = b.now().Add(breakerOpenFor)
		}
	case callNeutral:
	}
}
