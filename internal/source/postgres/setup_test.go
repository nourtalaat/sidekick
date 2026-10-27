//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// The test server: its image, its superuser, and the roles the source
// connects as. The image is the one storetest uses. pg_stat_statements must
// be preloaded at start-up, which a CI service container cannot do, so
// these tests always start their own container.
const (
	testImage         = "postgres:16-alpine"
	testAdminUser     = "sidekick"
	testAdminPassword = "sidekick"
	testAdminDatabase = "sidekick"
	// testMonitorRole has pg_monitor and nothing else, as the docs say.
	testMonitorRole     = "sidekick_monitor"
	testMonitorPassword = "monitor"
	// testPlainRole can log in and nothing else.
	testPlainRole     = "sidekick_plain"
	testPlainPassword = "plain"

	standbyPort        = "5433/tcp"
	standbyScript      = "/usr/local/bin/start-standby.sh"
	standbyLogPath     = "/tmp/standby.log"
	testDatabasePrefix = "sk57_"

	setupTimeout    = 3 * time.Minute
	shutdownTimeout = time.Minute
	cleanupTimeout  = 30 * time.Second
	adminMaxConns   = 4
	// waitTimeout bounds how long a test waits for a session to reach the
	// state it set up, such as waiting for a lock.
	waitTimeout = 20 * time.Second
	// standbyWaitTimeout bounds how long a test waits for the standby,
	// which also replays the other tests' databases being made and dropped.
	standbyWaitTimeout = time.Minute
	pollEvery          = 50 * time.Millisecond
)

// testServer is the Postgres container shared by every test in the test
// binary. The first test that needs it starts it, and the first test that
// needs the standby starts that.
type testServer struct {
	once      sync.Once
	err       error
	container *tcpostgres.PostgresContainer
	host      string
	port      string
	admin     *pgxpool.Pool

	standbyOnce sync.Once
	standbyErr  error
	standbyPort string
}

var server testServer

// testDB is one test's own database on the test server.
type testDB struct {
	// name is the database's name on the server.
	name string
	// admin is a superuser pool on the database, for setting up sessions.
	admin *pgxpool.Pool
}

// TestMain runs the tests, then stops the Postgres container if one was
// started.
func TestMain(m *testing.M) {
	code := m.Run()

	if err := server.stop(); err != nil {
		fmt.Fprintf(os.Stderr, "postgres source tests: %v\n", err)

		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

// start starts the container, opens the admin pool and makes the test
// roles.
func (s *testServer) start() error {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	script, err := filepath.Abs("fixture/start-standby.sh")
	if err != nil {
		return fmt.Errorf("find the standby script: %w", err)
	}

	container, err := tcpostgres.Run(ctx, testImage,
		tcpostgres.WithUsername(testAdminUser),
		tcpostgres.WithPassword(testAdminPassword),
		tcpostgres.WithDatabase(testAdminDatabase),
		tcpostgres.BasicWaitStrategies(),
		testcontainers.WithCmdArgs("-c", "shared_preload_libraries=pg_stat_statements"),
		testcontainers.WithExposedPorts(standbyPort),
		testcontainers.WithFiles(testcontainers.ContainerFile{HostFilePath: script, ContainerFilePath: standbyScript, FileMode: 0o755}),
	)
	if err != nil {
		return errors.Join(fmt.Errorf("start a Postgres container: %w", err), testcontainers.TerminateContainer(container))
	}

	s.container = container

	if s.host, err = container.Host(ctx); err != nil {
		return fmt.Errorf("read the container's host: %w", err)
	}

	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return fmt.Errorf("read the container's port: %w", err)
	}

	s.port = port.Port()

	if s.admin, err = pgxpool.New(ctx, s.url(testAdminUser, testAdminPassword, s.port, testAdminDatabase)); err != nil {
		return fmt.Errorf("connect as the superuser: %w", err)
	}

	for _, sql := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", testMonitorRole, testMonitorPassword),
		fmt.Sprintf("GRANT pg_monitor TO %s", testMonitorRole),
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", testPlainRole, testPlainPassword),
	} {
		if _, err := s.admin.Exec(ctx, sql); err != nil {
			return fmt.Errorf("make the test roles: %w", err)
		}
	}

	return nil
}

// startStandby starts the standby in the container and returns its mapped
// port.
func (s *testServer) startStandby() error {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	code, out, err := s.container.Exec(ctx, []string{standbyScript, testAdminUser}, tcexec.WithUser("postgres"), tcexec.Multiplexed())
	if err != nil {
		return fmt.Errorf("run the standby script: %w", err)
	}

	if code != 0 {
		output, readErr := io.ReadAll(out)

		return errors.Join(fmt.Errorf("the standby script exited with %d: %s", code, output), readErr)
	}

	port, err := s.container.MappedPort(ctx, standbyPort)
	if err != nil {
		return fmt.Errorf("read the standby's port: %w", err)
	}

	s.standbyPort = port.Port()

	return nil
}

// stop closes the admin pool and stops the container, if any.
func (s *testServer) stop() error {
	if s.admin != nil {
		s.admin.Close()
	}

	if s.container == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.container.Terminate(ctx); err != nil {
		return fmt.Errorf("stop the Postgres container: %w", err)
	}

	return nil
}

// url returns the URL of a database on the test server.
func (s *testServer) url(user, password, port, database string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     s.host + ":" + port,
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}

	return u.String()
}

// setup starts the test server the first time it is called and returns a
// new database of the test's own, seeded from fixture/seed.sql. The
// database is dropped when the test ends.
func setup(t *testing.T) testDB {
	t.Helper()

	return setupWith(t, "fixture/seed.sql")
}

// setupWith is setup with another seed file, or none when seed is empty.
func setupWith(t *testing.T, seed string) testDB {
	t.Helper()

	server.once.Do(func() {
		server.err = server.start()
	})
	require.NoError(t, server.err, "set up the test Postgres")

	name := testDatabasePrefix + strings.ReplaceAll(ids.New().String(), "-", "")

	_, err := server.admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0")
	require.NoError(t, err, "create the test database")

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		if _, err := server.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop the test database %s: %v", name, err)
		}
	})

	admin, err := pgxpool.New(t.Context(), server.url(testAdminUser, testAdminPassword, server.port, name))
	require.NoError(t, err)
	t.Cleanup(admin.Close)

	if seed != "" {
		sql, err := os.ReadFile(filepath.Clean(seed))
		require.NoError(t, err)

		_, err = admin.Exec(t.Context(), string(sql), pgx.QueryExecModeSimpleProtocol)
		require.NoError(t, err, "load %s", seed)
	}

	return testDB{name: name, admin: admin}
}

// startedStandby starts the standby the first time it is called and returns its
// mapped port.
func startedStandby(t *testing.T) string {
	t.Helper()

	server.standbyOnce.Do(func() {
		server.standbyErr = server.startStandby()
	})
	require.NoError(t, server.standbyErr, "start the standby")

	return server.standbyPort
}

// target is one database of a test source, with the role it connects as.
type target struct {
	name     string
	user     string
	password string
	// port is the server's mapped port. Empty means the primary.
	port string
	// database is the database's name on the server.
	database string
}

// newSource returns a source whose databases are the targets, with their
// URLs in a secrets directory of the test's own, and the buffer its logs
// go to.
func newSource(t *testing.T, cfg Config, targets ...target) (*Source, *bytes.Buffer) {
	t.Helper()

	dir := t.TempDir()

	for _, tg := range targets {
		port := tg.port
		if port == "" {
			port = server.port
		}

		require.NoError(t, os.MkdirAll(filepath.Join(dir, tg.name), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, tg.name, "url"), []byte(server.url(tg.user, tg.password, port, tg.database)+"\n"), 0o600))

		cfg.Databases = append(cfg.Databases, Database{
			Name:          tg.name,
			URLSecret:     registry.SecretRef{Name: tg.name, Key: "url"},
			AllowInsecure: !loopback(server.host),
		})
	}

	var logs bytes.Buffer

	built, err := New(t.Context(), "db", cfg, registry.Deps{Secrets: secrets.NewDir(dir), Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	require.NoError(t, err)

	s, ok := built.(*Source)
	require.True(t, ok)

	t.Cleanup(func() { require.NoError(t, s.Close()) })

	return s, &logs
}

// monitor returns the target of a test database as the monitor role.
func monitor(db testDB) target {
	return target{name: "orders", user: testMonitorRole, password: testMonitorPassword, database: db.name}
}

// holdSession holds one connection of the admin pool, for a test that needs a
// session to stay in a state, such as holding a lock.
func holdSession(t *testing.T, db testDB) (*pgxpool.Conn, int32) {
	t.Helper()

	conn, err := db.admin.Acquire(t.Context())
	require.NoError(t, err)
	t.Cleanup(conn.Release)

	var pid int32
	require.NoError(t, conn.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&pid))

	return conn, pid
}

// background runs sql on conn until it ends or the test ends, for
// statements that wait, such as pg_sleep or a blocked update. When the test
// ends, the statement must have succeeded or been cancelled.
func background(t *testing.T, conn *pgxpool.Conn, sql string) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := conn.Exec(ctx, sql)
		done <- err
	}()

	t.Cleanup(func() {
		cancel()

		err := <-done
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "57014" {
			return
		}

		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v", sql, err)
		}
	})
}

// waitFor polls the test database as the superuser until the query returns
// true.
func waitFor(t *testing.T, db testDB, what, sql string, args ...any) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	for {
		var ok bool
		require.NoError(t, db.admin.QueryRow(t.Context(), sql, args...).Scan(&ok))

		if ok {
			return
		}

		require.True(t, time.Now().Before(deadline), "timed out waiting for %s", what)
		time.Sleep(pollEvery)
	}
}
