package api

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// customPlan is a db.DBTX that runs each query with a plan made for the
// values it gets, instead of a prepared statement whose plan the driver
// may reuse after a few runs. A query with optional filters, written as
// `$1 IS NULL OR column = $1`, gets a plan that uses the index of the
// filter only when the plan knows the value; a reused plan has to work for
// every combination of filters and uses none of their indexes.
type customPlan struct {
	db db.DBTX
}

// Exec runs the statement with a plan made for args.
func (c customPlan) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := c.db.Exec(ctx, sql, withExecMode(args)...)
	if err != nil {
		return tag, fmt.Errorf("exec: %w", err)
	}

	return tag, nil
}

// Query runs the query with a plan made for args.
func (c customPlan) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := c.db.Query(ctx, sql, withExecMode(args)...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	return rows, nil
}

// QueryRow runs the query with a plan made for args.
func (c customPlan) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.db.QueryRow(ctx, sql, withExecMode(args)...)
}

// withExecMode puts pgx.QueryExecModeExec in front of args, which makes
// pgx send the query in one unnamed statement. Postgres plans an unnamed
// statement for the values of each run.
func withExecMode(args []any) []any {
	return append([]any{pgx.QueryExecModeExec}, args...)
}
