package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/nourtalaat/sidekick/internal/store/migrations/vector"
)

// VectorVersionTable is the goose version table of the vector migration
// set, next to the main set's goose_db_version.
const VectorVersionTable = "goose_db_version_vector"

// Names in the vector set.
const (
	embeddingsTable = "memory_embeddings"
	embeddingColumn = "embedding"
	embeddingsIndex = "memory_embeddings_embedding_hnsw"
)

// untypedVector is the type modifier of a vector column with no length.
const untypedVector = -1

// VectorDimensionsError is returned when memory_embeddings holds vectors
// of another length than the embeddings model gives. The vectors are
// derived data, so `sidekick migrate vector-reset` empties the table, and
// the backfill embeds the entries again.
type VectorDimensionsError struct {
	// Stored is the length of the vectors in the table.
	Stored int
	// Configured is memory.vector.embeddings.dimensions.
	Configured int
}

// Error implements error.
func (e *VectorDimensionsError) Error() string {
	return fmt.Sprintf("memory_embeddings holds %d-dimension vectors but memory.vector.embeddings.dimensions is %d; "+
		"run `sidekick migrate vector-reset` to empty the table and embed the memory entries again", e.Stored, e.Configured)
}

// MigrateVector applies the vector migrations that the database does not
// have yet, then gives the embedding column its length and builds the HNSW
// index for it. Run it after Migrate, and only when memory.vector.enabled
// is true: the migrations create the pgvector extension, which a Postgres
// without pgvector does not have. It holds the migration lock while it
// runs, as Migrate does. It returns a *VectorDimensionsError when the
// table holds vectors of another length than dimensions.
func MigrateVector(ctx context.Context, pool *pgxpool.Pool, dimensions int) error {
	if err := migrate(ctx, pool, vector.FS, lock.DefaultLockID, goose.WithTableName(VectorVersionTable)); err != nil {
		return fmt.Errorf("migrate vector memory: %w", err)
	}

	if err := ensureVectorColumn(ctx, pool, dimensions); err != nil {
		return fmt.Errorf("set up the embedding column: %w", err)
	}

	return nil
}

// VectorMigrationStatus reports each vector migration and whether the
// database has it.
func VectorMigrationStatus(ctx context.Context, pool *pgxpool.Pool) (statuses []*goose.MigrationStatus, err error) {
	provider, err := newProvider(pool, vector.FS, lock.DefaultLockID, goose.WithTableName(VectorVersionTable))
	if err != nil {
		return nil, fmt.Errorf("read vector migration status: %w", err)
	}

	defer func() {
		err = closeProvider(provider, err)
	}()

	statuses, err = provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read vector migration status: %w", err)
	}

	return statuses, nil
}

// VectorSchemaVersion is SchemaVersion for the vector migrations.
func VectorSchemaVersion(ctx context.Context, pool *pgxpool.Pool) (current, target int64, err error) {
	provider, err := newProvider(pool, vector.FS, lock.DefaultLockID, goose.WithTableName(VectorVersionTable))
	if err != nil {
		return 0, 0, fmt.Errorf("read vector schema version: %w", err)
	}

	defer func() {
		err = closeProvider(provider, err)
	}()

	current, target, err = provider.GetVersions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read vector schema version: %w", err)
	}

	return current, target, nil
}

// CheckVectorDimensions returns a *VectorDimensionsError when
// memory_embeddings holds vectors of another length than dimensions. It
// only reads, so a replica that does not migrate (database.migrations is
// manual) can refuse to start without changing anything. A column that has
// no length yet, or no table at all, is not a mismatch.
func CheckVectorDimensions(ctx context.Context, pool *pgxpool.Pool, dimensions int) error {
	stored, err := storedDimensions(ctx, pool)
	if err != nil {
		return fmt.Errorf("read the embedding length: %w", err)
	}

	if stored != untypedVector && stored != dimensions {
		return &VectorDimensionsError{Stored: stored, Configured: dimensions}
	}

	return nil
}

// ResetVectors empties memory_embeddings and takes the length off its
// column, in one transaction under the migration lock, so the next
// MigrateVector gives it the configured length and the backfill embeds
// every memory entry again. record is called in the same transaction with
// the length the column had, zero when it had none, for the caller's audit
// entry. It returns that old length.
func ResetVectors(ctx context.Context, pool *pgxpool.Pool, record func(ctx context.Context, tx pgx.Tx, oldDimensions int) error) (oldDimensions int, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("reset vector memory: begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("reset vector memory: roll back: %w", rbErr))
		}
	}()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(lock.DefaultLockID)); err != nil {
		return 0, fmt.Errorf("reset vector memory: take the migration lock: %w", err)
	}

	stored, err := storedDimensions(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("reset vector memory: %w", err)
	}

	for _, stmt := range []string{
		"DROP INDEX IF EXISTS " + embeddingsIndex,
		"TRUNCATE " + embeddingsTable,
		"ALTER TABLE " + embeddingsTable + " ALTER COLUMN " + embeddingColumn + " TYPE vector",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return 0, fmt.Errorf("reset vector memory: %s: %w", stmt, err)
		}
	}

	oldDimensions = max(stored, 0)

	if err := record(ctx, tx, oldDimensions); err != nil {
		return 0, fmt.Errorf("reset vector memory: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("reset vector memory: commit: %w", err)
	}

	return oldDimensions, nil
}

// ensureVectorColumn gives the embedding column the length dimensions and
// builds the HNSW index, under the migration lock. A column that has the
// length already is left alone, and a column of another length is a
// *VectorDimensionsError.
func ensureVectorColumn(ctx context.Context, pool *pgxpool.Pool, dimensions int) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(lock.DefaultLockID)); err != nil {
		return fmt.Errorf("take the migration lock: %w", err)
	}

	stored, err := storedDimensions(ctx, tx)
	if err != nil {
		return fmt.Errorf("read the embedding length: %w", err)
	}

	switch {
	case stored == untypedVector:
		// The statement text holds only an integer, never user input.
		alter := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE vector(%d)", embeddingsTable, embeddingColumn, dimensions)
		if _, err := tx.Exec(ctx, alter); err != nil {
			return fmt.Errorf("%s: %w", alter, err)
		}
	case stored != dimensions:
		return &VectorDimensionsError{Stored: stored, Configured: dimensions}
	}

	index := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (%s vector_cosine_ops)", embeddingsIndex, embeddingsTable, embeddingColumn)
	if _, err := tx.Exec(ctx, index); err != nil {
		return fmt.Errorf("%s: %w", index, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// querier is the part of a pool or a transaction that storedDimensions needs.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// storedDimensions returns the length of the vectors the embedding column
// takes, or untypedVector when the column has no length or the table does
// not exist. For the vector type, Postgres keeps the length as the type
// modifier.
func storedDimensions(ctx context.Context, q querier) (int, error) {
	var typmod int

	err := q.QueryRow(ctx, `
		SELECT a.atttypmod
		FROM pg_attribute AS a
		WHERE a.attrelid = to_regclass($1) AND a.attname = $2 AND NOT a.attisdropped`,
		embeddingsTable, embeddingColumn).Scan(&typmod)
	if errors.Is(err, pgx.ErrNoRows) {
		return untypedVector, nil
	}

	if err != nil {
		return 0, fmt.Errorf("read the column type: %w", err)
	}

	return typmod, nil
}
