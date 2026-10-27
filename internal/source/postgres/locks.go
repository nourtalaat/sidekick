package postgres

import (
	"context"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/source"
)

// waitingSQL lists the sessions of the current database that wait for a
// lock, at least the given time, longest first, with the lock and the
// sessions that block them. A waiting session waits for one lock at a
// time, so each has one row. Parameters: $1 whether to return query text,
// $2 the longest text in characters, $3 the shortest wait in milliseconds,
// $4 the most rows.
const waitingSQL = `SELECT a.pid,
	a.usename::text AS role,
	a.application_name,
	host(a.client_addr) AS client_addr,
	l.locktype,
	l.mode,
	CASE WHEN l.relation IS NOT NULL THEN l.relation::regclass::text END AS relation,
	l.transactionid::text AS transaction_id,
	(extract(epoch FROM now() - COALESCE(l.waitstart, a.query_start)) * 1000)::bigint AS wait_ms,
	(extract(epoch FROM now() - a.xact_start) * 1000)::bigint AS transaction_ms,
	pg_catalog.pg_blocking_pids(a.pid) AS blocked_by,
	CASE WHEN $1::boolean THEN left(a.query, $2) END AS query
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
WHERE NOT l.granted
	AND a.datname = current_database()
	AND now() - COALESCE(l.waitstart, a.query_start) >= $3::float8 * interval '1 millisecond'
ORDER BY COALESCE(l.waitstart, a.query_start) ASC NULLS LAST
LIMIT $4`

// blockersSQL describes the given sessions, the ones that block others,
// oldest transaction first, and says whether each waits for a lock too.
// Parameters: $1 whether to return query text, $2 the longest text in
// characters, $3 the process IDs, $4 the most rows.
const blockersSQL = `SELECT a.pid,
	a.usename::text AS role,
	a.application_name,
	host(a.client_addr) AS client_addr,
	a.state,
	a.wait_event_type,
	a.wait_event,
	(extract(epoch FROM now() - a.xact_start) * 1000)::bigint AS transaction_ms,
	(extract(epoch FROM now() - a.state_change) * 1000)::bigint AS state_ms,
	cardinality(pg_catalog.pg_blocking_pids(a.pid)) > 0 AS waiting,
	CASE WHEN $1::boolean THEN left(a.query, $2) END AS query
FROM pg_catalog.pg_stat_activity a
WHERE a.pid = ANY($3::integer[])
ORDER BY a.xact_start ASC NULLS LAST
LIMIT $4`

// preparedPID is the process ID pg_blocking_pids gives a prepared
// transaction that holds a lock.
const preparedPID = 0

// lockWait is a session that waits for a lock.
type lockWait struct {
	PID           int32   `db:"pid"              json:"pid"`
	Role          *string `db:"role"             json:"role,omitempty"`
	Application   string  `db:"application_name" json:"application,omitempty"`
	ClientAddr    *string `db:"client_addr"      json:"clientAddr,omitempty"`
	LockType      string  `db:"locktype"         json:"lockType"`
	Mode          string  `db:"mode"             json:"mode"`
	Relation      *string `db:"relation"         json:"relation,omitempty"`
	TransactionID *string `db:"transaction_id"   json:"transactionId,omitempty"`
	WaitMs        *int64  `db:"wait_ms"          json:"waitMs,omitempty"`
	TransactionMs *int64  `db:"transaction_ms"   json:"transactionMs,omitempty"`
	BlockedBy     []int32 `db:"blocked_by"       json:"blockedBy"`
	Query         *string `db:"query"            json:"query,omitempty"`
}

// blocker is a session that blocks another.
type blocker struct {
	PID           int32   `db:"pid"              json:"pid"`
	Role          *string `db:"role"             json:"role,omitempty"`
	Application   string  `db:"application_name" json:"application,omitempty"`
	ClientAddr    *string `db:"client_addr"      json:"clientAddr,omitempty"`
	State         *string `db:"state"            json:"state,omitempty"`
	WaitEventType *string `db:"wait_event_type"  json:"waitEventType,omitempty"`
	WaitEvent     *string `db:"wait_event"       json:"waitEvent,omitempty"`
	TransactionMs *int64  `db:"transaction_ms"   json:"transactionMs,omitempty"`
	StateMs       *int64  `db:"state_ms"         json:"inStateMs,omitempty"`
	Waiting       bool    `db:"waiting"          json:"waiting"`
	Query         *string `db:"query"            json:"query,omitempty"`
}

// lockWaitsResult is the result of lock_waits.
type lockWaitsResult struct {
	Database         string     `json:"database"`
	QueryTextOmitted bool       `json:"queryTextOmitted,omitempty"`
	Waiting          []lockWait `json:"waiting"`
	Blockers         []blocker  `json:"blockers"`
	// RootBlockers block others without waiting themselves: the heads of
	// the blocking chains, which ending would free the others.
	RootBlockers []int32 `json:"rootBlockers"`
	Note         string  `json:"note,omitempty"`
}

// lockWaits runs lock_waits.
func lockWaits(ctx context.Context, q querier, req request) (source.Result, error) {
	limit := req.in.limit()

	rows, err := q.Query(ctx, waitingSQL, req.queryText, maxQueryLen, float64(req.in.MinWaitMs), limit+1)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_locks: %w", err)
	}

	waiting, more, err := collect[lockWait](rows, limit)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_locks: %w", err)
	}

	if len(waiting) == 0 {
		return noData("No session in database %s has waited for a lock for at least %d ms.", req.database, req.in.MinWaitMs), nil
	}

	pids := blockingPIDs(waiting)

	rows, err = q.Query(ctx, blockersSQL, req.queryText, maxQueryLen, pids, maxLimit+1)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the blocking sessions: %w", err)
	}

	blockers, moreBlockers, err := collect[blocker](rows, maxLimit)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the blocking sessions: %w", err)
	}

	res := buildLockWaits(req, waiting, blockers, slices.Contains(pids, preparedPID))

	return structured(req, "pg_locks and pg_stat_activity", res, more || moreBlockers)
}

// buildLockWaits builds the result of lock_waits, with query text cut and
// the root blockers found.
func buildLockWaits(req request, waiting []lockWait, blockers []blocker, prepared bool) lockWaitsResult {
	res := lockWaitsResult{
		Database:         req.database,
		QueryTextOmitted: !req.queryText,
		Waiting:          waiting,
		Blockers:         blockers,
		RootBlockers:     []int32{},
	}

	for i := range res.Waiting {
		res.Waiting[i].Query = cutQuery(res.Waiting[i].Query)
	}

	for i := range res.Blockers {
		res.Blockers[i].Query = cutQuery(res.Blockers[i].Query)

		if !res.Blockers[i].Waiting {
			res.RootBlockers = append(res.RootBlockers, res.Blockers[i].PID)
		}
	}

	if prepared {
		res.Note = "A blocking pid of 0 is a prepared transaction, which has no session; pg_prepared_xacts lists them."
	}

	return res
}

// blockingPIDs returns the process IDs that block the waiting sessions,
// sorted, each once.
func blockingPIDs(waiting []lockWait) []int32 {
	var pids []int32
	for _, w := range waiting {
		pids = append(pids, w.BlockedBy...)
	}

	slices.Sort(pids)

	return slices.Compact(pids)
}
