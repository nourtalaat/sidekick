package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// activitySQL lists the sessions of the current database that are not
// idle, other than Sidekick's own, whose query or transaction has run at
// least the given time, longest first. Parameters: $1 whether to return
// query text, $2 the longest text in characters, $3 the shortest time in
// milliseconds, $4 the most rows.
const activitySQL = `SELECT a.pid,
	a.usename::text AS role,
	a.application_name,
	host(a.client_addr) AS client_addr,
	a.backend_type,
	a.state,
	a.wait_event_type,
	a.wait_event,
	a.xact_start,
	a.query_start,
	(extract(epoch FROM now() - a.query_start) * 1000)::bigint AS query_ms,
	(extract(epoch FROM now() - a.xact_start) * 1000)::bigint AS transaction_ms,
	(extract(epoch FROM now() - a.state_change) * 1000)::bigint AS state_ms,
	pg_catalog.pg_blocking_pids(a.pid) AS blocked_by,
	CASE WHEN $1::boolean THEN left(a.query, $2) END AS query
FROM pg_catalog.pg_stat_activity a
WHERE a.datname = current_database()
	AND a.pid <> pg_catalog.pg_backend_pid()
	AND a.state <> 'idle'
	AND now() - COALESCE(a.xact_start, a.query_start) >= $3::float8 * interval '1 millisecond'
ORDER BY COALESCE(a.xact_start, a.query_start) ASC NULLS LAST
LIMIT $4`

// session is one row of pg_stat_activity.
type session struct {
	PID           int32      `db:"pid"             json:"pid"`
	Role          *string    `db:"role"            json:"role,omitempty"`
	Application   string     `db:"application_name" json:"application,omitempty"`
	ClientAddr    *string    `db:"client_addr"     json:"clientAddr,omitempty"`
	BackendType   string     `db:"backend_type"    json:"backendType"`
	State         *string    `db:"state"           json:"state,omitempty"`
	WaitEventType *string    `db:"wait_event_type" json:"waitEventType,omitempty"`
	WaitEvent     *string    `db:"wait_event"      json:"waitEvent,omitempty"`
	TransactionAt *time.Time `db:"xact_start"      json:"transactionStart,omitempty"`
	QueryAt       *time.Time `db:"query_start"     json:"queryStart,omitempty"`
	QueryMs       *int64     `db:"query_ms"        json:"queryMs,omitempty"`
	TransactionMs *int64     `db:"transaction_ms"  json:"transactionMs,omitempty"`
	StateMs       *int64     `db:"state_ms"        json:"inStateMs,omitempty"`
	BlockedBy     []int32    `db:"blocked_by"      json:"blockedBy,omitempty"`
	Query         *string    `db:"query"           json:"query,omitempty"`
}

// activityResult is the result of running_queries.
type activityResult struct {
	Database         string    `json:"database"`
	QueryTextOmitted bool      `json:"queryTextOmitted,omitempty"`
	Sessions         []session `json:"sessions"`
}

// runningQueries runs running_queries.
func runningQueries(ctx context.Context, q querier, req request) (source.Result, error) {
	limit := req.in.limit()

	rows, err := q.Query(ctx, activitySQL, req.queryText, maxQueryLen, float64(req.in.MinDurationMs), limit+1)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_stat_activity: %w", err)
	}

	sessions, more, err := collect[session](rows, limit)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_stat_activity: %w", err)
	}

	if len(sessions) == 0 {
		return noData("No session in database %s has run a query or held a transaction open for at least %d ms.",
			req.database, req.in.MinDurationMs), nil
	}

	for i := range sessions {
		sessions[i].Query = cutQuery(sessions[i].Query)
	}

	res := activityResult{Database: req.database, QueryTextOmitted: !req.queryText, Sessions: sessions}

	return structured(req, "pg_stat_activity", res, more)
}
