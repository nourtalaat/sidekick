//go:build integration

package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/rules"
)

func TestCacheSeesChangesFromAnotherReplica(t *testing.T) {
	t.Parallel()

	// notifyDeadline is how soon replica B must use a rule replica A
	// saved.
	const notifyDeadline = time.Second

	pool, storeA := setup(t)

	// The interval is long, so only the notification can make B reload
	// in time.
	cacheB := rules.NewCache(t.Context(), rules.Options{File: fileConfig(), Pool: pool, Interval: time.Hour})

	applied := make(chan []string, 10)
	cacheB.Subscribe(func(_ context.Context, e *rules.Effective) error {
		names := make([]string, len(e.Routes))
		for i, r := range e.Routes {
			names[i] = r.Config.Name
		}

		applied <- names

		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- cacheB.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	require.Equal(t, []string{"checkout", "payments", "default"}, <-applied)

	_, err := storeA.SetRoute(t.Context(), actor, "search", func(r *rules.Route) error {
		r.Config.Priority = 1
		r.Config.Match = &config.Match{Service: "search-*"}

		return nil
	})
	require.NoError(t, err)

	saved := time.Now()

	require.Eventually(t, func() bool {
		_, ok := findRoute(cacheB.Current(), "search")

		return ok
	}, notifyDeadline, 10*time.Millisecond)

	t.Logf("replica B used the route %s after it was saved", time.Since(saved))

	select {
	case names := <-drainLast(applied):
		require.Equal(t, []string{"search", "checkout", "payments", "default"}, names)
	case <-time.After(notifyDeadline):
		t.Fatal("the subscriber was not called with the new route")
	}
}

func TestCacheKeepsRulesWhenNothingChanged(t *testing.T) {
	t.Parallel()

	pool, _ := setup(t)
	cache := rules.NewCache(t.Context(), rules.Options{File: fileConfig(), Pool: pool})

	calls := 0
	cache.Subscribe(func(context.Context, *rules.Effective) error {
		calls++

		return nil
	})

	require.NoError(t, cache.Reload(t.Context()))
	first := cache.Current()
	require.NoError(t, cache.Reload(t.Context()))

	require.Equal(t, 1, calls)
	require.Same(t, first, cache.Current())
}

func findRoute(e *rules.Effective, name string) (rules.EffectiveRoute, bool) {
	for _, r := range e.Routes {
		if r.Config.Name == name {
			return r, true
		}
	}

	return rules.EffectiveRoute{}, false
}

// drainLast returns a channel that yields the last value waiting in c, or
// the next one to arrive when none waits.
func drainLast(c chan []string) <-chan []string {
	out := make(chan []string, 1)

	go func() {
		last := <-c
		for {
			select {
			case v := <-c:
				last = v
			default:
				out <- last

				return
			}
		}
	}()

	return out
}
