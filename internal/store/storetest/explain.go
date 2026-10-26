//go:build integration

package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrExplained is what every query through an Explainer returns, once the
// Explainer has kept its plan.
var ErrExplained = errors.New("explained")

// Explainer is a db.DBTX that runs EXPLAIN (ANALYZE, BUFFERS) on each query
// instead of the query itself, keeps the plan in Plan, and returns
// ErrExplained. EXPLAIN ANALYZE runs the query, so give it a transaction
// that the test rolls back. Query plan tests run each generated query
// through db.New(explainer) and check the plan for the index it should
// use.
type Explainer struct {
	Tx   pgx.Tx
	Plan string
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
	rows, err := e.Tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		return fmt.Errorf("explain: %w", err)
	}

	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}

	e.Plan = strings.Join(lines, "\n")

	return ErrExplained
}

// errorRow is a pgx.Row whose Scan returns err.
type errorRow struct {
	err error
}

func (r errorRow) Scan(...any) error {
	return r.err
}
