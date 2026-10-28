//go:build integration

package memory

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a memory source on a database of the test's own, seeded
// with fixture/memory.sql.
func setup(t *testing.T) source.Source {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/memory.sql")

	src, err := New(t.Context(), "memory", Config{}, registry.Deps{DB: pool})
	require.NoError(t, err)

	return src
}
