package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRetrierGenerate(t *testing.T) {
	t.Parallel()

	ok := Response{Model: "m", StopReason: StopEndTurn}
	rateLimited := &Error{Kind: ErrRateLimited, Err: errors.New("429")}
	rateLimitedAfter := &Error{Kind: ErrRateLimited, RetryAfter: 7 * time.Second, Err: errors.New("429")}
	overloaded := &Error{Kind: ErrOverloaded, Err: errors.New("529")}
	timedOut := &Error{Kind: ErrTimeout, Err: errors.New("timeout")}
	auth := &Error{Kind: ErrAuth, Err: errors.New("401")}
	plain := errors.New("connector bug")

	tests := []struct {
		name    string
		retrier Retrier
		// results are what the connector returns on each call, in order.
		results    []error
		wantErr    error
		wantDelays []time.Duration
	}{
		{
			name:    "first call succeeds",
			retrier: Retrier{Retries: 3},
			results: []error{nil},
		},
		{
			name:       "rate limited, then succeeds",
			retrier:    Retrier{Retries: 3},
			results:    []error{rateLimited, nil},
			wantDelays: []time.Duration{time.Second},
		},
		{
			name:       "retry-after wins over the backoff",
			retrier:    Retrier{Retries: 3},
			results:    []error{rateLimitedAfter, nil},
			wantDelays: []time.Duration{7 * time.Second},
		},
		{
			name:       "backoff doubles for each retry",
			retrier:    Retrier{Retries: 3, BaseDelay: 2 * time.Second},
			results:    []error{overloaded, timedOut, overloaded, nil},
			wantDelays: []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second},
		},
		{
			name:       "backoff stops at the cap",
			retrier:    Retrier{Retries: 3, BaseDelay: 2 * time.Second, MaxDelay: 5 * time.Second},
			results:    []error{overloaded, overloaded, overloaded, nil},
			wantDelays: []time.Duration{2 * time.Second, 4 * time.Second, 5 * time.Second},
		},
		{
			name:       "retries run out",
			retrier:    Retrier{Retries: 2},
			results:    []error{overloaded, overloaded, overloaded},
			wantErr:    overloaded,
			wantDelays: []time.Duration{time.Second, 2 * time.Second},
		},
		{
			name:    "zero retries means one call",
			retrier: Retrier{},
			results: []error{rateLimited},
			wantErr: rateLimited,
		},
		{
			name:    "an error that is not retryable is returned at once",
			retrier: Retrier{Retries: 3},
			results: []error{auth},
			wantErr: auth,
		},
		{
			name:    "an error that is not a model error is returned at once",
			retrier: Retrier{Retries: 3},
			results: []error{plain},
			wantErr: plain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			c := NewMockConnector(ctrl)

			var calls []any
			for _, err := range tt.results {
				resp := ok
				if err != nil {
					resp = Response{}
				}

				calls = append(calls, c.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(resp, err))
			}

			gomock.InOrder(calls...)

			var delays []time.Duration

			r := tt.retrier
			r.jitter = func(d time.Duration) time.Duration { return d }
			r.wait = func(_ context.Context, d time.Duration) error {
				delays = append(delays, d)

				return nil
			}

			resp, err := r.Generate(t.Context(), c, Request{})
			require.Equal(t, tt.wantDelays, delays)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, Response{}, resp)

				return
			}

			require.NoError(t, err)
			require.Equal(t, ok, resp)
		})
	}
}

func TestRetrierGenerateStopsWhenContextIsDone(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	c := NewMockConnector(ctrl)
	overloaded := &Error{Kind: ErrOverloaded, RetryAfter: time.Hour, Err: errors.New("529")}
	c.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(Response{}, overloaded)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Generate(ctx, c, Request{}, 3)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, overloaded)

	var merr *Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, ErrOverloaded, merr.Kind)
}

func TestHalfJitter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
	}{
		{name: "one second", d: time.Second},
		{name: "one nanosecond", d: time.Nanosecond},
		{name: "zero", d: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for range 100 {
				got := halfJitter(tt.d)
				require.GreaterOrEqual(t, got, tt.d/2)
				require.LessOrEqual(t, got, tt.d)
			}
		})
	}
}
