package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// step is one thing that happens to a breaker in a test.
type step struct {
	// wait moves the clock on before the step.
	wait time.Duration
	// wantAllow and wantTrial are what allow must return.
	wantAllow bool
	wantTrial bool
	// result is reported with done when the call was allowed.
	result callResult
}

func TestBreaker(t *testing.T) {
	t.Parallel()

	fail := step{wantAllow: true, result: callFailed}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name:  "four failures keep the circuit closed",
			steps: []step{fail, fail, fail, fail, {wantAllow: true, result: callOK}},
		},
		{
			name:  "five failures open the circuit",
			steps: []step{fail, fail, fail, fail, fail, {wantAllow: false}, {wait: breakerOpenFor - time.Second, wantAllow: false}},
		},
		{
			name: "a good trial call closes the circuit",
			steps: []step{
				fail, fail, fail, fail, fail,
				{wait: breakerOpenFor, wantAllow: true, wantTrial: true, result: callOK},
				{wantAllow: true, result: callOK},
			},
		},
		{
			name: "a failed trial call opens the circuit again",
			steps: []step{
				fail, fail, fail, fail, fail,
				{wait: breakerOpenFor, wantAllow: true, wantTrial: true, result: callFailed},
				{wantAllow: false},
				{wait: breakerOpenFor, wantAllow: true, wantTrial: true, result: callOK},
			},
		},
		{
			name: "a success resets the count",
			steps: []step{
				fail, fail, fail, fail, {wantAllow: true, result: callOK},
				fail, fail, fail, fail, {wantAllow: true, result: callOK},
			},
		},
		{
			name: "neutral calls do not count",
			steps: []step{
				fail, fail, fail, fail,
				{wantAllow: true, result: callNeutral},
				{wantAllow: true, result: callNeutral},
				{wantAllow: true, result: callOK},
			},
		},
		{
			name: "a neutral trial lets the next call try again",
			steps: []step{
				fail, fail, fail, fail, fail,
				{wait: breakerOpenFor, wantAllow: true, wantTrial: true, result: callNeutral},
				{wantAllow: true, wantTrial: true, result: callOK},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
			b := &breaker{now: func() time.Time { return now }}

			for i, s := range tt.steps {
				now = now.Add(s.wait)

				ok, trial := b.allow()
				require.Equal(t, s.wantAllow, ok, "step %d allow", i)
				require.Equal(t, s.wantTrial, trial, "step %d trial", i)

				if ok {
					b.done(s.result, trial)
				}
			}
		})
	}
}

func TestBreakerOneTrialAtATime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a second call during the trial fails at once"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
			b := &breaker{now: func() time.Time { return now }}

			for range breakerFailures {
				_, _ = b.allow()
				b.done(callFailed, false)
			}

			now = now.Add(breakerOpenFor)

			ok, trial := b.allow()
			require.True(t, ok)
			require.True(t, trial)

			ok, _ = b.allow()
			require.False(t, ok, tt.name)
		})
	}
}
