//go:build integration

package bench

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestOpenSchemaNamed(t *testing.T) {
	t.Parallel()

	t.Run("makes the schema with the name it is given and drops it on Close", func(t *testing.T) {
		t.Parallel()

		dbURL := storetest.NewURL(t)
		name := SchemaPrefix + "gate_0123456789abcdef"

		d, err := OpenSchemaNamed(t.Context(), dbURL, name, nil)
		require.NoError(t, err)
		require.Equal(t, name, d.Schema)

		admin, err := store.Open(t.Context(), dbURL, testMaxConns)
		require.NoError(t, err)
		t.Cleanup(admin.Close)

		require.Equal(t, 1, schemas(t, admin, name))
		require.NoError(t, d.Close(context.WithoutCancel(t.Context())))
		require.Equal(t, 0, schemas(t, admin, name))
	})

	t.Run("replaces a schema of that name that a killed run left", func(t *testing.T) {
		t.Parallel()

		dbURL := storetest.NewURL(t)
		name := SchemaPrefix + "gate_fedcba9876543210"

		left, err := OpenSchemaNamed(t.Context(), dbURL, name, nil)
		require.NoError(t, err)

		_, err = left.Pool.Exec(t.Context(), "INSERT INTO jobs (kind, payload) VALUES ('rollup', '{}')")
		require.NoError(t, err)

		// The first run is killed: its connections go, its schema stays.
		left.Pool.Close()

		again, err := OpenSchemaNamed(t.Context(), dbURL, name, nil)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, again.Close(context.WithoutCancel(t.Context()))) })

		var jobs int
		require.NoError(t, again.Pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs").Scan(&jobs))
		require.Zero(t, jobs, "the new schema starts empty")
	})

	t.Run("refuses a name that is not a benchmark's", func(t *testing.T) {
		t.Parallel()

		_, err := OpenSchemaNamed(t.Context(), storetest.NewURL(t), "public", nil)
		require.ErrorIs(t, err, ErrBadSchemaName)
	})
}

func TestNewResultStoreLeavesThePoolOpen(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)

	NewResultStore(pool).Close()

	require.NoError(t, pool.Ping(t.Context()))
}
