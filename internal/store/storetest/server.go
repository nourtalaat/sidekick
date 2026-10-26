//go:build integration

package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/migrations"
)

const (
	postgresImage     = "postgres:16-alpine"
	containerUser     = "sidekick"
	containerPassword = "sidekick"
	containerDatabase = "sidekick"

	setupTimeout    = 3 * time.Minute
	shutdownTimeout = time.Minute

	templatePrefix     = "sidekick_template_"
	templateHashLength = 16
	templateMaxConns   = 2

	// templateLockKey is the advisory lock key that test binaries take while
	// one of them builds the template database. It is "sk-tmpl!" as bytes.
	templateLockKey int64 = 0x736b2d746d706c21
)

// start finds or starts the Postgres server, opens the admin pool and makes
// sure the template database exists.
func start() error {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	adminURL, stop, err := startServer(ctx)
	if err != nil {
		return fmt.Errorf("find the test Postgres: %w", err)
	}

	server.adminURL, server.stop = adminURL, stop

	server.admin, err = store.Open(ctx, adminURL, adminMaxConns)
	if err != nil {
		return fmt.Errorf("connect to the test Postgres: %w", err)
	}

	server.template, err = buildTemplate(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("build the template database: %w", err)
	}

	return nil
}

// shutdown closes the admin pool and stops the container, if any.
func shutdown() error {
	if server.admin != nil {
		server.admin.Close()
	}

	if server.stop == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.stop(ctx); err != nil {
		return fmt.Errorf("stop the Postgres container: %w", err)
	}

	return nil
}

// startServer returns the admin URL of the Postgres server, and a function
// that stops it when storetest started it.
func startServer(ctx context.Context) (string, func(context.Context) error, error) {
	if envURL := os.Getenv(EnvDatabaseURL); envURL != "" {
		return envURL, nil, nil
	}

	container, err := postgres.Run(ctx, postgresImage,
		postgres.WithUsername(containerUser),
		postgres.WithPassword(containerPassword),
		postgres.WithDatabase(containerDatabase),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return "", nil, errors.Join(
			fmt.Errorf("start a Postgres container (or set %s): %w", EnvDatabaseURL, err),
			testcontainers.TerminateContainer(container),
		)
	}

	stop := func(ctx context.Context) error {
		if err := container.Terminate(ctx); err != nil {
			return fmt.Errorf("terminate container: %w", err)
		}

		return nil
	}

	containerURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", nil, errors.Join(fmt.Errorf("read the container's database URL: %w", err), stop(ctx))
	}

	return containerURL, stop, nil
}

// buildTemplate makes sure a database with every migration applied exists
// and returns its name. The name holds a hash of the migrations, so a
// template is built again only when they change and later runs reuse it.
// Test binaries of several packages run at the same time, so the build
// happens under an advisory lock, in a database that is renamed into place
// only once it is complete.
func buildTemplate(ctx context.Context, adminURL string) (name string, err error) {
	hash, err := migrationsHash()
	if err != nil {
		return "", fmt.Errorf("name the template: %w", err)
	}

	name = templatePrefix + hash
	building := name + "_building"

	// The lock belongs to this connection's session, so closing the
	// connection releases it.
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}

	defer func() {
		if closeErr := conn.Close(context.WithoutCancel(ctx)); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close connection: %w", closeErr))
		}
	}()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", templateLockKey); err != nil {
		return "", fmt.Errorf("lock: %w", err)
	}

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", fmt.Errorf("look for template %s: %w", name, err)
	}

	if exists {
		return name, nil
	}

	for _, sql := range []string{
		"DROP DATABASE IF EXISTS " + quote(building) + " WITH (FORCE)",
		"CREATE DATABASE " + quote(building) + " TEMPLATE template0",
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return "", fmt.Errorf("%s: %w", sql, err)
		}
	}

	if err := migrateDatabase(ctx, adminURL, building); err != nil {
		return "", fmt.Errorf("migrate %s: %w", building, err)
	}

	if _, err := conn.Exec(ctx, "ALTER DATABASE "+quote(building)+" RENAME TO "+quote(name)); err != nil {
		return "", fmt.Errorf("rename %s to %s: %w", building, name, err)
	}

	return name, nil
}

// migrateDatabase applies every migration to the named database and closes
// its connections, so that it can serve as a template.
func migrateDatabase(ctx context.Context, adminURL, name string) error {
	dbURL, err := databaseURL(adminURL, name)
	if err != nil {
		return fmt.Errorf("build the database URL: %w", err)
	}

	pool, err := store.Open(ctx, dbURL, templateMaxConns)
	if err != nil {
		return fmt.Errorf("open the database: %w", err)
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("apply the migrations: %w", err)
	}

	return nil
}

// migrationsHash returns a short hash of the names and contents of every
// migration file.
func migrationsHash() (string, error) {
	sum := sha256.New()

	err := fs.WalkDir(migrations.FS, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}

		if entry.IsDir() {
			return nil
		}

		content, err := fs.ReadFile(migrations.FS, path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		sum.Write(fmt.Appendf(nil, "%s\x00%d\x00", path, len(content)))
		sum.Write(content)

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash migrations: %w", err)
	}

	return hex.EncodeToString(sum.Sum(nil))[:templateHashLength], nil
}

// databaseURL returns adminURL with its database name replaced by name.
func databaseURL(adminURL, name string) (string, error) {
	// The parse error is left out because it repeats the URL, password
	// included.
	u, err := url.Parse(adminURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("%s must be a postgres:// URL", EnvDatabaseURL)
	}

	u.Path = "/" + name

	return u.String(), nil
}
