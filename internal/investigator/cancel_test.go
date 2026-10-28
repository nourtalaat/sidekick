package investigator

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCancellerWatch(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	other := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c2")

	tests := []struct {
		name string
		// act watches a run of id and acts on the Canceller, and returns
		// whether the run's context should have ended.
		act func(c *Canceller, unwatch func()) bool
	}{
		{
			name: "a stop for the run ends its context",
			act: func(c *Canceller, _ func()) bool {
				c.cancel(id)

				return true
			},
		},
		{
			name: "a stop for another investigation does not",
			act: func(c *Canceller, _ func()) bool {
				c.cancel(other)

				return false
			},
		},
		{
			name: "a stop after the run ended does not",
			act: func(c *Canceller, unwatch func()) bool {
				unwatch()
				c.cancel(id)

				return false
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := NewCanceller(nil, nil)

			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)

			unwatch := c.watch(id, 1, cancel)

			if tt.act(c, unwatch) {
				require.ErrorIs(t, context.Cause(ctx), errCancelled)
				require.True(t, stopped(ctx))
			} else {
				require.NoError(t, ctx.Err())
			}

			unwatch()
			require.Empty(t, c.runs, "nothing is left watched")
		})
	}
}

func TestNilCancellerWatchesNothing(t *testing.T) {
	t.Parallel()

	var c *Canceller

	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)

	c.watch(uuid.New(), 1, cancel)()
	require.NoError(t, ctx.Err())
}
