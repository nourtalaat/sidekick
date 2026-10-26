//go:build integration

package storetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store"
)

// EnvDatabaseURL names the environment variable with the URL of an existing
// Postgres server to test against. The user in it must be allowed to create
// databases. When it is empty, storetest starts Postgres in a container.
const EnvDatabaseURL = "SIDEKICK_TEST_DATABASE_URL"

const (
	testMaxConns       int32 = 4
	adminMaxConns      int32 = 4
	cleanupTimeout           = 30 * time.Second
	testDatabasePrefix       = "sidekick_test_"
)

// sharedServer is the Postgres server shared by every test in the test
// binary. The first test that needs it sets it up.
type sharedServer struct {
	once     sync.Once
	err      error
	adminURL string
	admin    *pgxpool.Pool
	template string
	stop     func(context.Context) error
}

var server sharedServer

// Main runs the tests of a package, then closes the shared connections and
// stops the Postgres container if one was started. Call it from TestMain in
// the package's setup_test.go:
//
//	func TestMain(m *testing.M) {
//		os.Exit(storetest.Main(m))
//	}
func Main(m *testing.M) int {
	code := m.Run()

	if err := shutdown(); err != nil {
		fmt.Fprintf(os.Stderr, "storetest: %v\n", err)

		if code == 0 {
			code = 1
		}
	}

	return code
}

// New returns a pool on a new database that has every migration applied and
// that no other test uses. The database is dropped when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()

	pool, err := store.Open(t.Context(), NewURL(t), testMaxConns)
	require.NoError(t, err, "open the test database")
	t.Cleanup(pool.Close)

	return pool
}

// NewURL is like New but returns the database URL, for a test that opens
// its own pools, for example one per simulated replica. Close each of those
// pools with t.Cleanup.
func NewURL(t testing.TB) string {
	t.Helper()

	setup(t)

	return createDatabase(t, server.template)
}

// NewEmptyURL returns the URL of a new database with no migrations applied,
// for tests of migrating itself. The database is dropped when the test ends.
func NewEmptyURL(t testing.TB) string {
	t.Helper()

	setup(t)

	return createDatabase(t, "template0")
}

// LoadFixture runs the SQL file at path, relative to the test's package
// directory, for example "fixture/jobs.sql". A file may hold several
// statements.
func LoadFixture(t testing.TB, pool *pgxpool.Pool, path string) {
	t.Helper()

	sql, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err, "read fixture %s", path)

	_, err = pool.Exec(t.Context(), string(sql), pgx.QueryExecModeSimpleProtocol)
	require.NoError(t, err, "load fixture %s", path)
}

// setup starts or connects to the shared server and builds the template
// database the first time it is called, and fails the test if that failed.
func setup(t testing.TB) {
	t.Helper()

	server.once.Do(func() {
		server.err = start()
	})
	require.NoError(t, server.err, "set up the test Postgres")
}

func createDatabase(t testing.TB, template string) string {
	t.Helper()

	name := testDatabasePrefix + strings.ReplaceAll(ids.New().String(), "-", "")

	_, err := server.admin.Exec(t.Context(), "CREATE DATABASE "+quote(name)+" TEMPLATE "+quote(template))
	require.NoError(t, err, "create test database %s", name)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		if _, err := server.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(name)+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})

	dbURL, err := databaseURL(server.adminURL, name)
	require.NoError(t, err)

	return dbURL
}

func quote(name string) string {
	return pgx.Identifier{name}.Sanitize()
}
