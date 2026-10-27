package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/source"
)

// serverSQL reads whether the server is a standby and, when it is, how far
// its replay is behind: the last transaction it replayed, the received WAL
// it has not replayed yet, and its connection to the server it streams
// from. On a primary every standby column is null. The CASE keeps
// pg_is_wal_replay_paused, which fails outside recovery, from running on a
// primary.
const serverSQL = `SELECT pg_catalog.pg_is_in_recovery() AS in_recovery,
	CASE WHEN pg_catalog.pg_is_in_recovery() THEN pg_catalog.pg_is_wal_replay_paused() END AS replay_paused,
	pg_catalog.pg_last_xact_replay_timestamp() AS last_replay_at,
	(extract(epoch FROM now() - pg_catalog.pg_last_xact_replay_timestamp()) * 1000)::bigint AS replay_delay_ms,
	pg_catalog.pg_last_wal_receive_lsn()::text AS receive_lsn,
	pg_catalog.pg_last_wal_replay_lsn()::text AS replay_lsn,
	pg_catalog.pg_wal_lsn_diff(pg_catalog.pg_last_wal_receive_lsn(), pg_catalog.pg_last_wal_replay_lsn())::bigint AS replay_behind_bytes,
	w.status AS receiver_status,
	w.sender_host,
	w.sender_port,
	(extract(epoch FROM now() - w.last_msg_receipt_time) * 1000)::bigint AS last_message_ms
FROM (SELECT 1) AS one
LEFT JOIN pg_catalog.pg_stat_wal_receiver w ON true`

// standbysSQL lists the standbys that stream from this server, furthest
// behind first, with how far each is behind this server's WAL position in
// bytes and in time. On a standby that position is the WAL it received,
// since it writes none of its own. Parameter: $1 the most rows.
const standbysSQL = `SELECT r.pid,
	r.usename::text AS role,
	r.application_name,
	host(r.client_addr) AS client_addr,
	r.state,
	r.sync_state,
	r.sent_lsn::text AS sent_lsn,
	r.replay_lsn::text AS replay_lsn,
	pg_catalog.pg_wal_lsn_diff(cur.lsn, r.sent_lsn)::bigint AS send_behind_bytes,
	pg_catalog.pg_wal_lsn_diff(cur.lsn, r.flush_lsn)::bigint AS flush_behind_bytes,
	pg_catalog.pg_wal_lsn_diff(cur.lsn, r.replay_lsn)::bigint AS replay_behind_bytes,
	(extract(epoch FROM r.write_lag) * 1000)::float8 AS write_lag_ms,
	(extract(epoch FROM r.flush_lag) * 1000)::float8 AS flush_lag_ms,
	(extract(epoch FROM r.replay_lag) * 1000)::float8 AS replay_lag_ms,
	r.reply_time
FROM pg_catalog.pg_stat_replication r
CROSS JOIN (
	SELECT CASE WHEN pg_catalog.pg_is_in_recovery() THEN pg_catalog.pg_last_wal_receive_lsn()
		ELSE pg_catalog.pg_current_wal_lsn() END AS lsn
) AS cur
ORDER BY replay_behind_bytes DESC NULLS FIRST
LIMIT $1`

// Roles a server can have in replication.
const (
	rolePrimary = "primary"
	roleStandby = "standby"
)

// replicationNote explains the replay delay, which people often misread.
const replicationNote = "replayDelayMs is the time since the last transaction this standby replayed was committed " +
	"on the primary. It also grows while the primary has no writes, so check replayBehindBytes and " +
	"lastMessageMs too: zero bytes behind and a recent message mean the standby is caught up."

// serverState is how a server takes part in replication. Every field but
// InRecovery is null on a primary.
type serverState struct {
	InRecovery        bool       `db:"in_recovery"         json:"-"`
	ReplayPaused      *bool      `db:"replay_paused"       json:"replayPaused,omitempty"`
	LastReplayAt      *time.Time `db:"last_replay_at"      json:"lastReplayAt,omitempty"`
	ReplayDelayMs     *int64     `db:"replay_delay_ms"     json:"replayDelayMs,omitempty"`
	ReceiveLSN        *string    `db:"receive_lsn"         json:"receiveLsn,omitempty"`
	ReplayLSN         *string    `db:"replay_lsn"          json:"replayLsn,omitempty"`
	ReplayBehindBytes *int64     `db:"replay_behind_bytes" json:"replayBehindBytes,omitempty"`
	ReceiverStatus    *string    `db:"receiver_status"     json:"receiverStatus,omitempty"`
	SenderHost        *string    `db:"sender_host"         json:"senderHost,omitempty"`
	SenderPort        *int32     `db:"sender_port"         json:"senderPort,omitempty"`
	LastMessageMs     *int64     `db:"last_message_ms"     json:"lastMessageMs,omitempty"`
}

// standby is one row of pg_stat_replication.
type standby struct {
	PID               int32      `db:"pid"                 json:"pid"`
	Role              *string    `db:"role"                json:"role,omitempty"`
	Application       string     `db:"application_name"    json:"application,omitempty"`
	ClientAddr        *string    `db:"client_addr"         json:"clientAddr,omitempty"`
	State             *string    `db:"state"               json:"state,omitempty"`
	SyncState         *string    `db:"sync_state"          json:"syncState,omitempty"`
	SentLSN           *string    `db:"sent_lsn"            json:"sentLsn,omitempty"`
	ReplayLSN         *string    `db:"replay_lsn"          json:"replayLsn,omitempty"`
	SendBehindBytes   *int64     `db:"send_behind_bytes"   json:"sendBehindBytes,omitempty"`
	FlushBehindBytes  *int64     `db:"flush_behind_bytes"  json:"flushBehindBytes,omitempty"`
	ReplayBehindBytes *int64     `db:"replay_behind_bytes" json:"replayBehindBytes,omitempty"`
	WriteLagMs        *float64   `db:"write_lag_ms"        json:"writeLagMs,omitempty"`
	FlushLagMs        *float64   `db:"flush_lag_ms"        json:"flushLagMs,omitempty"`
	ReplayLagMs       *float64   `db:"replay_lag_ms"       json:"replayLagMs,omitempty"`
	ReplyAt           *time.Time `db:"reply_time"          json:"replyAt,omitempty"`
}

// replicationResult is the result of replication_lag.
type replicationResult struct {
	Database string `json:"database"`
	// ServerRole is primary or standby.
	ServerRole string `json:"serverRole"`
	// Standby is this server's own replay state, when it is a standby.
	Standby *serverState `json:"standby,omitempty"`
	// Standbys stream from this server.
	Standbys []standby `json:"standbys"`
	Note     string    `json:"note,omitempty"`
}

// replicationLag runs replication_lag.
func replicationLag(ctx context.Context, q querier, req request) (source.Result, error) {
	rows, err := q.Query(ctx, serverSQL)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the recovery state: %w", err)
	}

	state, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[serverState])
	if err != nil {
		return source.Result{}, fmt.Errorf("read the recovery state: %w", err)
	}

	rows, err = q.Query(ctx, standbysSQL, maxLimit+1)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_stat_replication: %w", err)
	}

	standbys, more, err := collect[standby](rows, maxLimit)
	if err != nil {
		return source.Result{}, fmt.Errorf("read pg_stat_replication: %w", err)
	}

	return buildReplication(req, state, standbys, more)
}

// buildReplication builds the result of replication_lag. A primary that no
// standby streams from has no replication to show.
func buildReplication(req request, state serverState, standbys []standby, more bool) (source.Result, error) {
	if !state.InRecovery && len(standbys) == 0 {
		return noData("The server of database %s is a primary, and no standby streams from it: pg_stat_replication is empty.", req.database), nil
	}

	if standbys == nil {
		standbys = []standby{}
	}

	res := replicationResult{Database: req.database, ServerRole: rolePrimary, Standbys: standbys}

	views := "pg_stat_replication"

	if state.InRecovery {
		res.ServerRole = roleStandby
		res.Note = replicationNote
		res.Standby = &state
		views = "pg_stat_wal_receiver and pg_stat_replication"
	}

	return structured(req, views, res, more)
}
