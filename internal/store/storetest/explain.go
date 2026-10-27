//go:build integration

package storetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

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
}

// NewExplainer returns an Explainer for the test t that explains queries in
// tx. EXPLAIN ANALYZE runs the query, so give it a transaction that the
// test rolls back.
func NewExplainer(t testing.TB, tx pgx.Tx) *Explainer {
	return &Explainer{t: t, tx: tx}
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
	rows, err := e.tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		return fmt.Errorf("explain: %w", err)
	}

	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
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

// errorRow is a pgx.Row whose Scan returns err.
type errorRow struct {
	err error
}

func (r errorRow) Scan(...any) error {
	return r.err
}
