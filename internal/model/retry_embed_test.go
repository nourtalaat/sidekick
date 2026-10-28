package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRetrierEmbed(t *testing.T) {
	t.Parallel()

	ok := EmbedResult{Vectors: [][]float32{{1, 2}}, Usage: Usage{InputTokens: 3}}
	overloaded := &Error{Kind: ErrOverloaded, Err: errors.New("503")}
	invalid := &Error{Kind: ErrInvalid, Err: errors.New("400")}

	tests := []struct {
		name       string
		retries    int
		results    []error
		wantErr    error
		wantDelays []time.Duration
	}{
		{name: "first call succeeds", retries: 2, results: []error{nil}},
		{name: "overloaded, then succeeds", retries: 2, results: []error{overloaded, nil}, wantDelays: []time.Duration{time.Second}},
		{name: "retries run out", retries: 1, results: []error{overloaded, overloaded}, wantErr: overloaded, wantDelays: []time.Duration{time.Second}},
		{name: "an invalid request is not repeated", retries: 2, results: []error{invalid}, wantErr: invalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			e := NewMockEmbedder(ctrl)

			var calls []any

			for _, err := range tt.results {
				res := ok
				if err != nil {
					res = EmbedResult{}
				}

				calls = append(calls, e.EXPECT().Embed(gomock.Any(), []string{"a"}).Return(res, err))
			}

			gomock.InOrder(calls...)

			var delays []time.Duration

			r := Retrier{Retries: tt.retries}
			r.jitter = func(d time.Duration) time.Duration { return d }
			r.wait = func(_ context.Context, d time.Duration) error {
				delays = append(delays, d)

				return nil
			}

			got, err := r.Embed(t.Context(), e, []string{"a"})
			require.Equal(t, tt.wantDelays, delays)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, EmbedResult{}, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, ok, got)
		})
	}
}
