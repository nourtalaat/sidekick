//go:build integration

package storetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrExplained is what every query through an Explainer returns, once the
// Explainer has kept its plan.
var ErrExplained = errors.New("explained")

// Explainer is a db.DBTX that runs EXPLAIN (ANALYZE, BUFFERS) on each query
// instead of the query itself, keeps the plan in Plan, logs it on the test,
// and returns ErrExplained. Query plan tests run each generated query
// through db.New(explainer) and check the plan for the index it should
// use.
//
// When the variable named by EnvPlansDir is set, the Explainer also writes
// the query and its plan to a file in that directory.
type Explainer struct {
	Plan string

	t  testing.TB
	tx pgx.Tx
	// generic makes the Explainer plan each query as a prepared statement
	// with a generic plan.
	generic bool
}

// genericStatement is the name of the statement a generic Explainer
// prepares for each query.
const genericStatement = "storetest_generic"

// NewExplainer returns an Explainer for the test t that explains queries in
// tx. EXPLAIN ANALYZE runs the query, so give it a transaction that the
// test rolls back.
func NewExplainer(t testing.TB, tx pgx.Tx) *Explainer {
	return &Explainer{t: t, tx: tx}
}

// NewGenericExplainer is like NewExplainer, but plans each query as a
// prepared statement with a generic plan, which does not know the argument
// values. The pgx pool prepares statements, so a query that runs often may
// end up with this plan.
func NewGenericExplainer(t testing.TB, tx pgx.Tx) *Explainer {
	return &Explainer{t: t, tx: tx, generic: true}
}

// Exec explains a query that returns no rows.
func (e *Explainer) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, e.explain(ctx, sql, args)
}

// Query explains a query that returns rows.
func (e *Explainer) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, e.explain(ctx, sql, args)
}

// QueryRow explains a query that returns one row.
func (e *Explainer) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return errorRow{err: e.explain(ctx, sql, args)}
}

func (e *Explainer) explain(ctx context.Context, sql string, args []any) error {
	lines, err := e.planLines(ctx, sql, args)
	if err != nil {
		return fmt.Errorf("plan the query: %w", err)
	}

	e.Plan = strings.Join(lines, "\n")
	e.t.Logf("EXPLAIN ANALYZE of %s:\n%s", e.t.Name(), e.Plan)

	if dir := os.Getenv(EnvPlansDir); dir != "" {
		if err := writePlan(dir, e.t.Name(), sql, e.Plan); err != nil {
			return fmt.Errorf("save plan: %w", err)
		}
	}

	return ErrExplained
}

// planLines runs EXPLAIN (ANALYZE, BUFFERS) on the query and returns the
// lines of its plan.
func (e *Explainer) planLines(ctx context.Context, sql string, args []any) ([]string, error) {
	if e.generic {
		return e.genericPlanLines(ctx, sql, args)
	}

	rows, err := e.tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}

	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}

	return lines, nil
}

// genericPlanLines prepares the query and explains an execution of it with
// the generic plan, which is what a pooled connection may switch to after a
// few executions. The arguments are written into the EXECUTE statement,
// because EXPLAIN of a bound query is planned with the argument values.
func (e *Explainer) genericPlanLines(ctx context.Context, sql string, args []any) ([]string, error) {
	literals := make([]string, len(args))

	for i, arg := range args {
		lit, err := literal(arg)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", i+1, err)
		}

		literals[i] = lit
	}

	// A prepared statement belongs to the connection, not to the
	// transaction, so it is dropped before the connection goes back.
	defer func() { _, _ = e.tx.Exec(ctx, "DEALLOCATE "+genericStatement) }()

	if _, err := e.tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_generic_plan"); err != nil {
		return nil, fmt.Errorf("force the generic plan: %w", err)
	}

	if _, err := e.tx.Exec(ctx, "PREPARE "+genericStatement+" AS "+sql); err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}

	rows, err := e.tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) EXECUTE "+genericStatement+"("+strings.Join(literals, ", ")+")")
	if err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}

	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}

	return lines, nil
}

// literal writes a query argument as a SQL literal. It knows the types the
// generated queries take: text, integers, times, UUIDs and nil for NULL.
func literal(arg any) (string, error) {
	switch v := arg.(type) {
	case nil:
		return "NULL", nil
	case string:
		return quoteLiteral(v), nil
	case *string:
		if v == nil {
			return "NULL", nil
		}

		return quoteLiteral(*v), nil
	case int32, int64, int:
		return fmt.Sprint(v), nil
	case time.Time:
		return quoteLiteral(v.Format(time.RFC3339Nano)), nil
	case uuid.UUID:
		return quoteLiteral(v.String()), nil
	default:
		return "", fmt.Errorf("a %T has no literal", arg)
	}
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// errorRow is a pgx.Row whose Scan returns err.
type errorRow struct {
	err error
}

func (r errorRow) Scan(...any) error {
	return r.err
}
