package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/source"
)

// extensionSchemaSQL finds the schema pg_stat_statements is installed in,
// so the statements query names its view exactly rather than trusting the
// search path.
const extensionSchemaSQL = `SELECT n.nspname::text
FROM pg_catalog.pg_extension e
JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
WHERE e.extname = 'pg_stat_statements'`

// statementsSQL lists the statements of the current database by mean or
// total execution time. %s is the view, schema-qualified and quoted by
// pgx.Identifier. Parameters: $1 whether to return query text, $2 the
// longest text in characters, $3 the shortest mean time in milliseconds,
// $4 the order (mean or total), $5 the most rows.
const statementsSQL = `SELECT s.queryid AS query_id,
	COALESCE(r.rolname::text, s.userid::text) AS role,
	s.calls,
	round(s.total_exec_time::numeric, 1)::float8 AS total_ms,
	round(s.mean_exec_time::numeric, 1)::float8 AS mean_ms,
	round(s.max_exec_time::numeric, 1)::float8 AS max_ms,
	s.rows,
	s.shared_blks_hit,
	s.shared_blks_read,
	s.temp_blks_written,
	CASE WHEN $1::boolean THEN left(s.query, $2) END AS query
FROM %s s
LEFT JOIN pg_catalog.pg_roles r ON r.oid = s.userid
WHERE s.dbid = (SELECT d.oid FROM pg_catalog.pg_database d WHERE d.datname = current_database())
	AND s.mean_exec_time >= $3::float8
ORDER BY CASE WHEN $4::text = 'total' THEN s.total_exec_time ELSE s.mean_exec_time END DESC
LIMIT $5`

// statement is one row of pg_stat_statements.
type statement struct {
	QueryID           *int64  `db:"query_id"          json:"queryId,omitempty"`
	Role              string  `db:"role"              json:"role"`
	Calls             int64   `db:"calls"             json:"calls"`
	TotalMs           float64 `db:"total_ms"          json:"totalMs"`
	MeanMs            float64 `db:"mean_ms"           json:"meanMs"`
	MaxMs             float64 `db:"max_ms"            json:"maxMs"`
	Rows              int64   `db:"rows"              json:"rows"`
	SharedBlocksHit   int64   `db:"shared_blks_hit"   json:"sharedBlocksHit"`
	SharedBlocksRead  int64   `db:"shared_blks_read"  json:"sharedBlocksRead"`
	TempBlocksWritten int64   `db:"temp_blks_written" json:"tempBlocksWritten"`
	Query             *string `db:"query"             json:"query,omitempty"`
}

// statementsResult is the result of slow_queries.
type statementsResult struct {
	Database         string      `json:"database"`
	OrderBy          string      `json:"orderBy"`
	QueryTextOmitted bool        `json:"queryTextOmitted,omitempty"`
	Statements       []statement `json:"statements"`
}

// slowQueries runs slow_queries.
func slowQueries(ctx context.Context, q querier, req request) (source.Result, error) {
	var schema string

	err := q.QueryRow(ctx, extensionSchemaSQL).Scan(&schema)
	if errors.Is(err, pgx.ErrNoRows) {
		return noData("The pg_stat_statements extension is not installed in database %s, so there are no statement "+
			"statistics. Use running_queries to see the queries running now.", req.database), nil
	}

	if err != nil {
		return source.Result{}, fmt.Errorf("find pg_stat_statements: %w", err)
	}

	order := req.in.OrderBy
	if order != orderTotal {
		order = orderMean
	}

	limit := req.in.limit()
	view := pgx.Identifier{schema, "pg_stat_statements"}.Sanitize()

	rows, err := q.Query(ctx, fmt.Sprintf(statementsSQL, view), req.queryText, maxQueryLen, float64(req.in.MinMeanMs), order, limit+1)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_stat_statements: %w", err)
	}

	statements, more, err := collect[statement](rows, limit)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_stat_statements: %w", err)
	}

	if len(statements) == 0 {
		return noData("No statement in database %s has a mean execution time of at least %d ms since the statistics were last reset.",
			req.database, req.in.MinMeanMs), nil
	}

	for i := range statements {
		statements[i].Query = cutQuery(statements[i].Query)
	}

	res := statementsResult{Database: req.database, OrderBy: order, QueryTextOmitted: !req.queryText, Statements: statements}

	return structured(req, "pg_stat_statements", res, more)
}
