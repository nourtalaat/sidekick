//go:generate mockgen -source database.go -destination mocks_database.go -package doctor -write_package_comment=false

package doctor

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store"
)

// databaseMaxConns is the pool size of the database check, which runs one
// query at a time.
const databaseMaxConns = 1

// Database is the part of the database the check reads.
type Database interface {
	// SchemaVersion returns the newest migration the database has and the
	// newest one this binary embeds.
	SchemaVersion(ctx context.Context) (current, target int64, err error)
	// Close closes the connection.
	Close()
}

// OpenDatabase connects to the database at url, failing when it cannot
// reach it.
type OpenDatabase func(ctx context.Context, url string) (Database, error)

// checkDatabase reads the database URL, connects, and compares the schema
// with the migrations this release needs.
func (d *doctor) checkDatabase(ctx context.Context) Result {
	const name = "database"

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	ref := d.cfg.Database.URLSecret

	url, err := d.cfg.DatabaseURL(ctx, d.secrets)
	if err != nil {
		return Result{Check: name, Status: StatusFail, Message: secretProblem("database.urlSecret", ref, err), Fix: secretFix(ref)}
	}

	d.secrets.recordURL(url)

	db, err := d.open(ctx, url)
	if err != nil {
		return Result{
			Check: name, Status: StatusFail,
			Message: "cannot connect: " + err.Error(),
			Fix:     fmt.Sprintf("check the URL in the Secret %s, key %s, and that Postgres 15 or newer accepts connections from Sidekick's pod", ref.Name, ref.Key),
		}
	}

	defer db.Close()

	current, target, err := db.SchemaVersion(ctx)
	if err != nil {
		return Result{Check: name, Status: StatusFail, Message: "connected, but cannot read the schema version: " + err.Error(), Fix: "check that the database user can read the goose_db_version table"}
	}

	switch {
	case current == target:
		return Result{Check: name, Status: StatusOK, Message: fmt.Sprintf("connected; the schema has every migration this release needs (%d)", target)}
	case current > target:
		return Result{Check: name, Status: StatusOK, Message: fmt.Sprintf("connected; the schema is at migration %d, newer than the %d this release needs, which is fine during a rolling update", current, target)}
	}

	fix := "restart Sidekick, which migrates at start-up, or run `sidekick migrate up`"
	if d.cfg.Database.Migrations == config.MigrationsManual {
		fix = "database.migrations is manual, so run `sidekick migrate up`"
	}

	return Result{
		Check: name, Status: StatusFail,
		Message: fmt.Sprintf("connected, but the database has migrations up to %d and this release needs %d", current, target),
		Fix:     fix,
	}
}

// postgres is a Database on a pgx pool.
type postgres struct {
	pool *pgxpool.Pool
}

// SchemaVersion implements Database.
func (p postgres) SchemaVersion(ctx context.Context) (current, target int64, err error) {
	current, target, err = store.SchemaVersion(ctx, p.pool)
	if err != nil {
		return 0, 0, fmt.Errorf("read the schema version: %w", err)
	}

	return current, target, nil
}

// Close implements Database.
func (p postgres) Close() {
	p.pool.Close()
}

// openPostgres is the default OpenDatabase.
func openPostgres(ctx context.Context, url string) (Database, error) {
	pool, err := store.Open(ctx, url, databaseMaxConns)
	if err != nil {
		return nil, fmt.Errorf("open the database: %w", err)
	}

	return postgres{pool: pool}, nil
}
