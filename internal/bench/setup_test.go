//go:build integration

package bench

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// testMaxConns is the pool size of the connections the tests open
// themselves.
const testMaxConns = 2

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// newRunner loads the test config with conn as its scripted connector and
// returns a Runner for scenarios.
func newRunner(t *testing.T, conn *modeltest.Connector, repeat int, scenarios ...Scenario) *Runner {
	t.Helper()

	cfg, regs := loadTestConfig(t, conn)

	r, err := New(t.Context(), scenarios, Options{Config: cfg, Registries: regs, Repeat: repeat})
	require.NoError(t, err)

	return r
}

// loadSample returns the sample scenario.
func loadSample(t *testing.T) Scenario {
	t.Helper()

	got, err := LoadScenarios(sampleScenario, LoadOptions{BaseDir: repoRoot})
	require.NoError(t, err)
	require.Len(t, got, 1)

	return got[0]
}

// inDefaultSchema returns a new migrated database whose tables are in the
// default schema, as with a temporary Postgres.
func inDefaultSchema(t *testing.T) *Database {
	t.Helper()

	return &Database{Pool: storetest.New(t)}
}

// inOwnSchema returns a schema of its own on a new database, as a --db run
// makes, and drops it when the test ends.
func inOwnSchema(t *testing.T) *Database {
	t.Helper()

	d, err := OpenSchema(t.Context(), storetest.NewURL(t), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close(context.WithoutCancel(t.Context()))) })

	return d
}

// withoutCreate returns the URL of a new database as a new role that may
// connect to it but not create schemas in it. The role is dropped when the
// test ends.
func withoutCreate(t *testing.T) string {
	t.Helper()

	dbURL := storetest.NewURL(t)

	admin, err := store.Open(t.Context(), dbURL, testMaxConns)
	require.NoError(t, err)
	t.Cleanup(admin.Close)

	role := "bench_" + strings.ReplaceAll(ids.New().String(), "-", "")
	password := strings.ReplaceAll(ids.New().String(), "-", "")

	_, err = admin.Exec(t.Context(), "CREATE ROLE "+role+" LOGIN PASSWORD '"+password+"'")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, err := admin.Exec(context.WithoutCancel(t.Context()), "DROP ROLE IF EXISTS "+role)
		require.NoError(t, err)
	})

	u, err := url.Parse(dbURL)
	require.NoError(t, err)

	u.User = url.UserPassword(role, password)

	return u.String()
}
