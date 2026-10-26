package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "postgres"

const (
	// lockTimeout bounds how long a query waits for a lock. The statistics
	// views take only light locks, so a longer wait means something holds
	// a strong lock on a system catalog, and the tool should say so rather
	// than queue behind it.
	lockTimeout = time.Second
	// rollbackTimeout bounds the rollback that ends each transaction, which
	// runs even when the call's context has ended.
	rollbackTimeout = 5 * time.Second
	// maxMessageLen bounds a message from Postgres in a tool error.
	maxMessageLen = 512
)

// timeoutsSQL sets the limits of the current transaction: the statement
// timeout, the lock timeout, and how long the session may sit idle inside
// it.
const timeoutsSQL = `SELECT pg_catalog.set_config('statement_timeout', $1, true),
	pg_catalog.set_config('lock_timeout', $2, true),
	pg_catalog.set_config('idle_in_transaction_session_timeout', $1, true)`

// errUnknownTool is returned for a call to a tool the source does not have.
// The guard only sends calls for tools the source listed, so this means a
// bug.
var errUnknownTool = errors.New("unknown tool")

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads the statistics views of the databases its config lists. It
// is safe for concurrent use.
type Source struct {
	cfg   Config
	pools *pools
	tools []source.Tool
}

var _ source.Source = (*Source)(nil)

// querier runs the fixed queries of a tool. A pgx.Tx is one; tests can
// pass another.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// request is one call of a tool, after its input is checked.
type request struct {
	// database is the configured name of the database.
	database string
	in       input
	// queryText says whether results hold the text of queries.
	queryText bool
}

// reader runs the queries of one tool and builds its result.
type reader func(ctx context.Context, q querier, req request) (source.Result, error)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(s.tools), nil
}

// Call runs one tool in a read-only transaction. A view with nothing in it
// returns a result with NoData set. An error Postgres reports for the
// query, such as a statement timeout or a missing privilege, returns a
// *source.ToolError; a database that cannot be reached is a failure.
func (s *Source) Call(ctx context.Context, call source.Call) (source.Result, error) {
	res, err := s.run(ctx, call)
	if err != nil {
		return s.failure(ctx, call.Tool, err)
	}

	return res, nil
}

// Close closes the connection pools. It waits for running calls to end.
func (s *Source) Close() error {
	s.pools.close()

	return nil
}

// run checks a call's input, gets the database's pool and runs the tool.
func (s *Source) run(ctx context.Context, call source.Call) (source.Result, error) {
	read, err := readerOf(call.Tool)
	if err != nil {
		return source.Result{}, fmt.Errorf("pick the tool: %w", err)
	}

	in, err := decode(call.Input)
	if err != nil {
		return source.Result{}, fmt.Errorf("decode the input: %w", err)
	}

	db, ok := s.cfg.database(in.Database)
	if !ok {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("database %q is not one Sidekick may read; known databases: %s",
			in.Database, strings.Join(s.cfg.databaseNames(), ", "))}
	}

	pool, err := s.pools.get(ctx, db)
	if err != nil {
		return source.Result{}, fmt.Errorf("connect: %w", err)
	}

	req := request{database: db.Name, in: in, queryText: !s.cfg.OmitQueryText}

	var res source.Result

	err = s.readOnly(ctx, pool, func(q querier) error {
		got, err := read(ctx, q, req)
		if err != nil {
			return fmt.Errorf("run %s: %w", call.Tool, err)
		}

		res = got

		return nil
	})
	if err != nil {
		return source.Result{}, fmt.Errorf("database %s: %w", db.Name, err)
	}

	return res, nil
}

// readOnly runs fn in a READ ONLY transaction with the statement timeout
// and the lock timeout set, and rolls the transaction back after it.
func (s *Source) readOnly(ctx context.Context, pool *pgxpool.Pool, fn func(q querier) error) (err error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin a read-only transaction: %w", err)
	}

	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()

		if rbErr := tx.Rollback(rollbackCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	statement := strconv.FormatInt(s.cfg.statementTimeout().Milliseconds(), 10)
	lock := strconv.FormatInt(lockTimeout.Milliseconds(), 10)

	if _, err := tx.Exec(ctx, timeoutsSQL, statement, lock); err != nil {
		return fmt.Errorf("set the timeouts: %w", err)
	}

	if err := fn(tx); err != nil {
		return fmt.Errorf("query: %w", err)
	}

	return nil
}

// failure turns the error of a call into what Call returns. An error
// Postgres reports for the query itself becomes a tool error with its
// message, so the model can read it. Errors of the connection, the login,
// the server's resources and Sidekick's own checks stay failures of the
// source.
func (s *Source) failure(ctx context.Context, tool string, err error) (source.Result, error) {
	if ctx.Err() != nil {
		return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
	}

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && !sourceFault(pgErr.Code) {
		return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: s.pgMessage(pgErr)})
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// pgMessage returns the tool error message for an error Postgres reported.
func (s *Source) pgMessage(pgErr *pgconn.PgError) string {
	message := source.CutText(pgErr.Message, maxMessageLen)

	switch pgErr.Code {
	case "57014":
		return fmt.Sprintf("the query ran longer than the statement timeout of %s and was cancelled, which can mean the database is overloaded: %s",
			s.cfg.statementTimeout(), message)
	case "55P03":
		return fmt.Sprintf("the query waited longer than the lock timeout of %s and was cancelled, so a session holds a strong lock on a system catalog: %s",
			lockTimeout, message)
	case "42501":
		return fmt.Sprintf("Postgres answered %s: %s. The source's role needs pg_monitor; see the Postgres source docs", pgErr.Code, message)
	default:
		return fmt.Sprintf("Postgres answered %s: %s", pgErr.Code, message)
	}
}

// New builds a Postgres source from its validated config. It makes no
// network call and reads no secret: each database's pool is made on the
// first call for it, so a database that is down blocks neither start-up
// nor readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	maxConns := int32(min(cfg.MaxConcurrency(), math.MaxInt32)) //nolint:gosec // Capped at math.MaxInt32.

	return &Source{
		cfg: cfg,
		pools: &pools{
			secrets:  deps.Secrets,
			logger:   cmp.Or(deps.Logger, slog.Default()),
			maxConns: maxConns,
			open:     map[string]openPool{},
		},
		tools: toolList(cfg.databaseNames()),
	}, nil
}

// readerOf returns the reader of a tool.
func readerOf(tool string) (reader, error) {
	switch tool {
	case toolRunningQueries:
		return runningQueries, nil
	default:
		return nil, fmt.Errorf("tool %q: %w", tool, errUnknownTool)
	}
}

// sourceFault reports whether a Postgres error code is about the
// connection, the login, the server's resources or the server itself,
// rather than the query: classes 08, 28, 3D, 53, 57P, 58 and XX.
func sourceFault(code string) bool {
	for _, prefix := range []string{"08", "28", "3D", "53", "57P", "58", "XX"} {
		if strings.HasPrefix(code, prefix) {
			return true
		}
	}

	return false
}
