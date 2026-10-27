package bench

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store"
)

const (
	// postgresImage is the image of the temporary Postgres.
	postgresImage = "postgres:16-alpine"
	// postgresUser and postgresDatabase name the temporary Postgres's user
	// and database. Its password is new for each run.
	postgresUser     = "sidekick"
	postgresDatabase = "sidekick"
	// maxConns is the size of a benchmark's pool: the queue's listener,
	// a claim, a running job and the runner's own reads.
	maxConns = 8
	// adminMaxConns is the size of the pool that creates and drops the
	// schema of a --db run.
	adminMaxConns = 1
	// insufficientPrivilege is the Postgres error code of a statement the
	// role may not run.
	insufficientPrivilege = "42501"
	// SchemaPrefix starts the name of the schema of each --db run.
	SchemaPrefix = "sidekick_bench_"
)

// ErrCannotCreateSchema is returned when the database role of a --db run
// may not create the run's schema, which needs CREATE on the database.
var ErrCannotCreateSchema = errors.New("cannot create the benchmark's schema; the database role needs CREATE on the database")

// Database is the Postgres a benchmark runs in, with every migration
// applied. Close it when the benchmark ends.
type Database struct {
	// Pool is the pool the runs use.
	Pool *pgxpool.Pool
	// Schema is the schema the tables are in, and empty for the default
	// one.
	Schema string

	close func(ctx context.Context) error
}

// StartTemporary starts Postgres in a container, with testcontainers, and
// migrates it. Close stops and removes the container. A container that
// cannot start gives an error that wraps ErrInfrastructure.
func StartTemporary(ctx context.Context, log *slog.Logger) (*Database, error) {
	log = orDiscard(log)
	log.InfoContext(ctx, "starting a temporary Postgres container", "image", postgresImage)

	container, err := postgres.Run(ctx, postgresImage,
		postgres.WithUsername(postgresUser),
		postgres.WithPassword(strings.ReplaceAll(ids.New().String(), "-", "")),
		postgres.WithDatabase(postgresDatabase),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("%w: start a temporary Postgres container (is Docker running?): %w", ErrInfrastructure, err),
			testcontainers.TerminateContainer(container),
		)
	}

	stop := func(ctx context.Context) error {
		if err := container.Terminate(ctx); err != nil {
			return fmt.Errorf("stop the temporary Postgres container: %w", err)
		}

		log.InfoContext(ctx, "stopped the temporary Postgres container")

		return nil
	}

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: read the temporary Postgres's URL: %w", ErrInfrastructure, err), stop(context.WithoutCancel(ctx)))
	}

	pool, err := store.Open(ctx, url, maxConns)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: connect to the temporary Postgres: %w", ErrInfrastructure, err), stop(context.WithoutCancel(ctx)))
	}

	d := &Database{Pool: pool, close: func(ctx context.Context) error {
		pool.Close()

		return stop(ctx)
	}}

	if err := store.Migrate(ctx, pool); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: migrate the temporary Postgres: %w", ErrInfrastructure, err), d.Close(context.WithoutCancel(ctx)))
	}

	return d, nil
}

// OpenSchema creates a schema of the benchmark's own, named SchemaPrefix
// and a random part, on the database at url, and migrates it. The schema
// keeps the benchmark's jobs away from a Sidekick that serves from the same
// database. Close drops the schema with everything in it. A role that may
// not create the schema gives an error that wraps ErrCannotCreateSchema;
// a database that cannot be reached one that wraps ErrInfrastructure.
func OpenSchema(ctx context.Context, url string, log *slog.Logger) (*Database, error) {
	log = orDiscard(log)
	schema := SchemaPrefix + strings.ReplaceAll(ids.New().String(), "-", "")

	admin, err := store.Open(ctx, url, adminMaxConns)
	if err != nil {
		return nil, fmt.Errorf("%w: connect to the database: %w", ErrInfrastructure, err)
	}

	if err := store.CreateSchema(ctx, admin, schema); err != nil {
		admin.Close()

		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == insufficientPrivilege {
			return nil, fmt.Errorf("%w: %w", ErrCannotCreateSchema, err)
		}

		return nil, fmt.Errorf("%w: %w", ErrInfrastructure, err)
	}

	log.InfoContext(ctx, "created the benchmark's schema; it is dropped when the run ends, and can be dropped by hand with DROP SCHEMA ... CASCADE if the run is killed", "schema", schema)

	d := &Database{Schema: schema}
	d.close = func(ctx context.Context) error {
		if d.Pool != nil {
			d.Pool.Close()
		}

		defer admin.Close()

		if err := store.DropSchema(ctx, admin, schema); err != nil {
			return fmt.Errorf("drop the benchmark's schema %s: %w", schema, err)
		}

		log.InfoContext(ctx, "dropped the benchmark's schema", "schema", schema)

		return nil
	}

	d.Pool, err = store.OpenSchema(ctx, url, schema, maxConns)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: connect to the database: %w", ErrInfrastructure, err), d.Close(context.WithoutCancel(ctx)))
	}

	if err := store.MigrateSchema(ctx, d.Pool, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: migrate the benchmark's schema: %w", ErrInfrastructure, err), d.Close(context.WithoutCancel(ctx)))
	}

	return d, nil
}

// Close stops the temporary Postgres, or drops the benchmark's schema.
// Give it a context that has not ended, so it still runs after Ctrl-C.
func (d *Database) Close(ctx context.Context) error {
	if d.close == nil {
		return nil
	}

	if err := d.close(ctx); err != nil {
		return fmt.Errorf("close the benchmark's database: %w", err)
	}

	return nil
}
