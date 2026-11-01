//go:build integration

package strategystore_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/strategystore"
	"github.com/nourtalaat/sidekick/strategies"
)

// reloadWait is how long a test waits for a replica to notice a save.
const reloadWait = 10 * time.Second

// reloadTick is how often a test checks a replica.
const reloadTick = 20 * time.Millisecond

// newCache returns a Cache on pool that has not loaded yet, over the
// built-in strategies and the file strategies in dir.
func newCache(t *testing.T, pool *pgxpool.Pool, dir string) *strategystore.Cache {
	t.Helper()

	base, err := strategy.Load(strategies.FS, dir, nil)
	require.NoError(t, err)

	return strategystore.NewCache(strategystore.Options{Base: base, Pool: pool})
}

// run starts the cache's reload loop and stops it when the test ends.
func run(t *testing.T, c *strategystore.Cache) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- c.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
}

func TestCacheReload(t *testing.T) {
	t.Parallel()

	t.Run("runtime strategies are used over built-in ones", func(t *testing.T) {
		t.Parallel()

		pool, store := setup(t)
		cache := newCache(t, pool, "")

		_, ok := cache.Current().Get("checkout-latency")
		require.False(t, ok, "nothing is loaded before the first reload")

		require.NoError(t, cache.Reload(t.Context()))

		st, ok := cache.Current().Get("checkout-latency")
		require.True(t, ok)
		require.Equal(t, strategy.Runtime, st.Origin)
		require.Equal(t, "runtime:2", st.Version)

		_, err := store.Save(t.Context(), strategystore.SaveInput{
			Actor: sam, Name: strategy.GenericAlert, Body: body(strategy.GenericAlert, "Replaced at runtime."), Reason: "Override",
		})
		require.NoError(t, err)
		require.NoError(t, cache.Reload(t.Context()))

		st, _ = cache.Current().Get(strategy.GenericAlert)
		require.Equal(t, strategy.Runtime, st.Origin)
		require.Equal(t, "Replaced at runtime.", st.Description)
	})

	t.Run("a file strategy wins over a runtime one", func(t *testing.T) {
		t.Parallel()

		pool, _ := setup(t)
		cache := newCache(t, pool, filepath.Join("..", "strategy", "fixture", "valid"))

		require.NoError(t, cache.Reload(t.Context()))

		st, ok := cache.Current().Get("checkout-latency")
		require.True(t, ok)
		require.Equal(t, strategy.File, st.Origin, "checkout-latency is also a file strategy")
	})

	t.Run("subscribers hear about changes only", func(t *testing.T) {
		t.Parallel()

		pool, store := setup(t)
		cache := newCache(t, pool, "")

		var calls atomic.Int32

		cache.Subscribe(func(context.Context, *strategy.Set) error {
			calls.Add(1)

			return nil
		})

		require.NoError(t, cache.Reload(t.Context()))
		require.NoError(t, cache.Reload(t.Context()))
		require.EqualValues(t, 1, calls.Load(), "the second reload found nothing new")

		_, err := store.Save(t.Context(), strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: "New"})
		require.NoError(t, err)
		require.NoError(t, cache.Reload(t.Context()))
		require.EqualValues(t, 2, calls.Load())
	})

	t.Run("a stored version that fails the checks is not used", func(t *testing.T) {
		t.Parallel()

		pool, _ := setup(t)

		_, err := pool.Exec(t.Context(), `UPDATE strategy_versions SET body = 'name: checkout-latency' WHERE version = 2`)
		require.NoError(t, err)

		cache := newCache(t, pool, "")
		require.NoError(t, cache.Reload(t.Context()))

		_, ok := cache.Current().Get("checkout-latency")
		require.False(t, ok)
	})
}

func TestCacheFollowsOtherReplicas(t *testing.T) {
	t.Parallel()

	pool, store := setup(t)

	writer := newCache(t, pool, "")
	reader := newCache(t, pool, "")

	run(t, writer)
	run(t, reader)

	require.Eventually(t, func() bool {
		_, ok := reader.Current().Get("checkout-latency")

		return ok
	}, reloadWait, reloadTick, "the reader loads at start")

	_, err := store.Save(t.Context(), strategystore.SaveInput{Actor: sam, Name: "checkout-latency", Body: body("checkout-latency", "Third version."), Reason: "Again"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		st, _ := reader.Current().Get("checkout-latency")

		return st.Version == "runtime:3"
	}, reloadWait, reloadTick, "the reader hears the notification, well before the interval")
}

func TestCacheVersion(t *testing.T) {
	t.Parallel()

	pool, store := setup(t)
	cache := newCache(t, pool, "")
	require.NoError(t, cache.Reload(t.Context()))

	_, err := store.Save(t.Context(), strategystore.SaveInput{Actor: sam, Name: "checkout-latency", Body: body("checkout-latency", "Third version."), Reason: "Again"})
	require.NoError(t, err)
	require.NoError(t, cache.Reload(t.Context()))

	generic, _ := cache.Current().Get(strategy.GenericAlert)

	tests := []struct {
		name      string
		strategy  string
		version   string
		wantExact bool
		// wantVersion is the version of the strategy returned.
		wantVersion string
		wantErr     error
	}{
		{name: "the current version", strategy: "checkout-latency", version: "runtime:3", wantExact: true, wantVersion: "runtime:3"},
		{name: "an older version is read from the database", strategy: "checkout-latency", version: "runtime:1", wantExact: true, wantVersion: "runtime:1"},
		{name: "a version that does not exist uses the current one", strategy: "checkout-latency", version: "runtime:9", wantVersion: "runtime:3"},
		{name: "a hash that is not the current one uses the current one", strategy: "checkout-latency", version: "0123456789abcdef", wantVersion: "runtime:3"},
		{name: "a built-in strategy", strategy: strategy.GenericAlert, version: generic.Version, wantExact: true, wantVersion: generic.Version},
		{name: "a runtime version of a name with none stored", strategy: strategy.GenericAlert, version: "runtime:1", wantVersion: generic.Version},
		{name: "an unknown strategy", strategy: "missing", version: "runtime:1", wantErr: strategy.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, exact, err := cache.Version(t.Context(), tt.strategy, tt.version)
			require.ErrorIs(t, err, tt.wantErr)

			if tt.wantErr != nil {
				return
			}

			require.Equal(t, tt.wantExact, exact)
			require.Equal(t, tt.wantVersion, st.Version)
		})
	}

	t.Run("the first version keeps its text", func(t *testing.T) {
		t.Parallel()

		st, _, err := cache.Version(t.Context(), "checkout-latency", "runtime:1")
		require.NoError(t, err)
		require.Equal(t, "Checkout is slow.", st.Description)
		require.Empty(t, st.First)
	})
}
