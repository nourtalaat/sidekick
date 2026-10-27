//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// sleepSQL is the slow query of the tests. Its comment holds an email
// address, the kind of literal that query text can carry.
const sleepSQL = "SELECT pg_sleep(30) /* ada@example.com */"

func TestRunningQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		sleep    bool
		minMs    int64
		wantNone bool
	}{
		{name: "a slow query shows with its text and how long it has run", sleep: true, minMs: 200},
		{name: "omitQueryText leaves the text out", cfg: Config{OmitQueryText: true}, sleep: true, minMs: 200},
		{name: "a query that has run less than the minimum is left out", sleep: true, minMs: 60_000, wantNone: true},
		{name: "a database with only idle sessions has no data", minMs: 0, wantNone: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := setup(t)

			var pid int32

			if tt.sleep {
				var conn *pgxpool.Conn

				conn, pid = holdSession(t, db)
				background(t, conn, sleepSQL)
				waitFor(t, db, "the sleep to start", "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1 AND wait_event = 'PgSleep' AND now() - query_start > interval '300 ms')", pid)
			}

			s, _ := newSource(t, tt.cfg, monitor(db))

			res, err := s.Call(t.Context(), source.Call{Tool: toolRunningQueries, Input: toolInput(t, map[string]any{"database": "orders", "minDurationMs": tt.minMs})})
			require.NoError(t, err)

			if tt.wantNone {
				require.True(t, res.NoData)
				require.Contains(t, res.Text, "No session in database orders")

				return
			}

			var got activityResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Len(t, got.Sessions, 1)

			sess := got.Sessions[0]
			require.Equal(t, pid, sess.PID)
			require.Equal(t, testAdminUser, *sess.Role)
			require.Equal(t, "active", *sess.State)
			require.Equal(t, "PgSleep", *sess.WaitEvent)
			require.GreaterOrEqual(t, *sess.QueryMs, int64(200))
			require.Equal(t, []source.Evidence{{Kind: source.EvidenceOther, Title: "pg_stat_activity in database orders"}}, res.Evidence)

			if tt.cfg.OmitQueryText {
				require.True(t, got.QueryTextOmitted)
				require.Nil(t, sess.Query)
				require.NotContains(t, string(res.Structured), "pg_sleep")
			} else {
				require.Equal(t, sleepSQL, *sess.Query)
			}
		})
	}
}

func TestLockWaits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// chain adds a third session that waits for a table lock the first
		// two hold.
		chain bool
		none  bool
	}{
		{name: "an update waits for the row another transaction updated"},
		{name: "a chain: a table lock waits behind both", chain: true},
		{name: "no session waits, so no data", none: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := setup(t)
			s, _ := newSource(t, Config{}, monitor(db))

			if tt.none {
				res, err := s.Call(t.Context(), source.Call{Tool: toolLockWaits, Input: toolInput(t, map[string]any{"database": "orders"})})
				require.NoError(t, err)
				require.True(t, res.NoData)

				return
			}

			holder, holderPID := holdSession(t, db)
			_, err := holder.Exec(t.Context(), "BEGIN; UPDATE accounts SET balance = balance + 1 WHERE id = 1")
			require.NoError(t, err)

			waiter, waiterPID := holdSession(t, db)
			background(t, waiter, "UPDATE accounts SET balance = balance - 1 WHERE id = 1")
			waitFor(t, db, "the update to wait", "SELECT cardinality(pg_blocking_pids($1)) > 0", waiterPID)

			want := map[int32][]int32{waiterPID: {holderPID}}

			if tt.chain {
				third, thirdPID := holdSession(t, db)
				background(t, third, "BEGIN; LOCK TABLE accounts IN ACCESS EXCLUSIVE MODE")
				waitFor(t, db, "the table lock to wait", "SELECT cardinality(pg_blocking_pids($1)) = 2", thirdPID)

				want[thirdPID] = []int32{holderPID, waiterPID}
			}

			res, err := s.Call(t.Context(), source.Call{Tool: toolLockWaits, Input: toolInput(t, map[string]any{"database": "orders"})})
			require.NoError(t, err)
			require.False(t, res.NoData)

			var got lockWaitsResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Len(t, got.Waiting, len(want))

			for _, w := range got.Waiting {
				require.ElementsMatch(t, want[w.PID], w.BlockedBy, "pid %d", w.PID)
				require.NotNil(t, w.Query)
				require.NotNil(t, w.WaitMs)

				if w.PID == waiterPID {
					require.Equal(t, "transactionid", w.LockType)
					require.Equal(t, "ShareLock", w.Mode)
				} else {
					require.Equal(t, "relation", w.LockType)
					require.Equal(t, "AccessExclusiveLock", w.Mode)
					require.Equal(t, "accounts", *w.Relation)
				}
			}

			require.Equal(t, []int32{holderPID}, got.RootBlockers)

			for _, b := range got.Blockers {
				if b.PID == holderPID {
					require.Equal(t, "idle in transaction", *b.State)
					require.False(t, b.Waiting)
				} else {
					require.True(t, b.Waiting)
				}
			}
		})
	}
}

func TestSlowQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// noExtension leaves the seed out, and with it pg_stat_statements.
		noExtension bool
		cfg         Config
		args        map[string]any
		wantText    string
		wantCut     bool
	}{
		{name: "the slowest statement first", args: map[string]any{"minMeanMs": 150}},
		{name: "by total time", args: map[string]any{"orderBy": "total", "limit": 1}, wantCut: true},
		{name: "omitQueryText leaves the text out", cfg: Config{OmitQueryText: true}, args: map[string]any{"minMeanMs": 150}},
		{name: "a minimum no statement reaches", args: map[string]any{"minMeanMs": 60_000}, wantText: "No statement in database orders"},
		{name: "a database without the extension", noExtension: true, wantText: "The pg_stat_statements extension is not installed in database orders"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			seed := "fixture/seed.sql"
			if tt.noExtension {
				seed = ""
			}

			db := setupWith(t, seed)

			for range 2 {
				_, err := db.admin.Exec(t.Context(), "SELECT pg_sleep(0.2)")
				require.NoError(t, err)
			}

			s, _ := newSource(t, tt.cfg, monitor(db))

			args := map[string]any{"database": "orders"}
			for k, v := range tt.args {
				args[k] = v
			}

			res, err := s.Call(t.Context(), source.Call{Tool: toolSlowQueries, Input: toolInput(t, args)})
			require.NoError(t, err)

			if tt.wantText != "" {
				require.True(t, res.NoData)
				require.Contains(t, res.Text, tt.wantText)

				return
			}

			var got statementsResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.NotEmpty(t, got.Statements)
			require.Equal(t, tt.wantCut, res.Truncated)

			first := got.Statements[0]
			require.Equal(t, testAdminUser, first.Role)
			require.EqualValues(t, 2, first.Calls)
			require.GreaterOrEqual(t, first.MeanMs, 190.0)
			require.NotNil(t, first.QueryID)

			if tt.cfg.OmitQueryText {
				require.True(t, got.QueryTextOmitted)
				require.Nil(t, first.Query)
			} else {
				require.Equal(t, "SELECT pg_sleep($1)", *first.Query)
			}
		})
	}
}

// TestReplicationLag runs against a real standby with its replay paused,
// so both the primary and the standby report it behind.
func TestReplicationLag(t *testing.T) {
	t.Parallel()

	db := setup(t)
	port := startedStandby(t)

	standbyAdmin, err := pgx.Connect(t.Context(), server.url(testAdminUser, testAdminPassword, port, testAdminDatabase))
	require.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()

		_, err := standbyAdmin.Exec(ctx, "SELECT pg_wal_replay_resume()")
		require.NoError(t, err)
		require.NoError(t, standbyAdmin.Close(ctx))
	})

	waitForStandby(t, standbyAdmin, "the standby to stream from the primary", func(st standbyStatus) bool { return st.receiver == "streaming" })

	_, err = standbyAdmin.Exec(t.Context(), "SELECT pg_wal_replay_pause()")
	require.NoError(t, err)

	// Each poll writes on the primary again, so WAL keeps arriving until the
	// paused standby has received some it has not replayed.
	waitForStandby(t, standbyAdmin, "the standby to fall behind", func(st standbyStatus) bool {
		_, err := db.admin.Exec(t.Context(), "UPDATE accounts SET balance = balance + 1 WHERE id = 1")
		require.NoError(t, err)

		return st.behind
	})

	tests := []struct {
		name     string
		target   target
		wantRole string
	}{
		{name: "the primary lists the standby that is behind", target: monitor(db), wantRole: rolePrimary},
		{
			name:     "the standby reports its paused replay",
			target:   target{name: "orders", user: testMonitorRole, password: testMonitorPassword, port: port, database: testAdminDatabase},
			wantRole: roleStandby,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _ := newSource(t, Config{}, tt.target)

			res, err := s.Call(t.Context(), source.Call{Tool: toolReplicationLag, Input: toolInput(t, map[string]any{"database": "orders"})})
			require.NoError(t, err)
			require.False(t, res.NoData)

			var got replicationResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Equal(t, tt.wantRole, got.ServerRole)

			if tt.wantRole == rolePrimary {
				require.Nil(t, got.Standby)
				require.Len(t, got.Standbys, 1)
				require.Equal(t, "streaming", *got.Standbys[0].State)
				require.Positive(t, *got.Standbys[0].ReplayBehindBytes)

				return
			}

			require.Empty(t, got.Standbys)
			require.NotNil(t, got.Standby)
			require.True(t, *got.Standby.ReplayPaused)
			require.Positive(t, *got.Standby.ReplayBehindBytes)
			require.Equal(t, "streaming", *got.Standby.ReceiverStatus)
			require.NotEmpty(t, got.Note)
		})
	}
}

// standbyStatus is what waitForStandby reads on the standby.
type standbyStatus struct {
	behind   bool
	pause    string
	receive  string
	replay   string
	receiver string
}

// waitForStandby polls the standby until done returns true, and fails
// with the standby's state when it does not in time.
func waitForStandby(t *testing.T, standby *pgx.Conn, what string, done func(st standbyStatus) bool) {
	t.Helper()

	const sql = `SELECT COALESCE(pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()) > 0, false),
	pg_get_wal_replay_pause_state(),
	COALESCE(pg_last_wal_receive_lsn()::text, ''),
	COALESCE(pg_last_wal_replay_lsn()::text, ''),
	COALESCE((SELECT status FROM pg_stat_wal_receiver), '')`

	deadline := time.Now().Add(standbyWaitTimeout)

	for {
		var st standbyStatus
		require.NoError(t, standby.QueryRow(t.Context(), sql).Scan(&st.behind, &st.pause, &st.receive, &st.replay, &st.receiver))

		if done(st) {
			return
		}

		if time.Now().After(deadline) {
			require.FailNow(t, "timed out waiting for "+what, "standby state: %+v\nstandby log:\n%s", st, standbyLog(t))
		}

		time.Sleep(pollEvery)
	}
}

// standbyLog returns the standby's server log, so a test that gives up
// waiting on the standby shows why it did not stream or replay.
func standbyLog(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	_, out, err := server.container.Exec(ctx, []string{"cat", standbyLogPath}, tcexec.Multiplexed())
	if err != nil {
		return fmt.Sprintf("read %s: %v", standbyLogPath, err)
	}

	log, err := io.ReadAll(out)
	if err != nil {
		return fmt.Sprintf("read %s: %v", standbyLogPath, err)
	}

	return string(log)
}

// TestSafety checks the limits every query runs under, and the checks on
// the role and the connection.
func TestSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		// grant is run as the superuser before the call.
		grant string
		// hold is run, and left open, on another session before the call.
		hold      string
		target    func(db testDB) target
		query     string
		wantCode  string
		wantErr   error
		wantLog   string
		wantError string
	}{
		{
			name:     "a write fails in the read-only transaction, even with the privilege",
			grant:    "GRANT INSERT ON accounts TO " + testMonitorRole,
			query:    "INSERT INTO accounts (id, owner, balance) VALUES (5000, 'x', 0) RETURNING owner",
			wantCode: "25006",
		},
		{
			name:     "a slow statement hits the statement timeout",
			cfg:      Config{StatementTimeout: 200 * time.Millisecond},
			query:    "SELECT pg_sleep(5)::text",
			wantCode: "57014",
		},
		{
			name:     "a statement behind a strong lock hits the lock timeout",
			grant:    "GRANT SELECT ON accounts TO " + testMonitorRole,
			hold:     "BEGIN; LOCK TABLE accounts IN ACCESS EXCLUSIVE MODE",
			query:    "SELECT count(*)::text FROM accounts",
			wantCode: "55P03",
		},
		{
			name: "a superuser is refused",
			target: func(db testDB) target {
				return target{name: "orders", user: testAdminUser, password: testAdminPassword, database: db.name}
			},
			wantErr: errSuperuser,
		},
		{
			name: "a role without pg_monitor works with a warning",
			target: func(db testDB) target {
				return target{name: "orders", user: testPlainRole, password: testPlainPassword, database: db.name}
			},
			wantLog: "is not a member of pg_monitor",
		},
		{
			name: "a wrong password is a failure of the source",
			target: func(db testDB) target {
				return target{name: "orders", user: testMonitorRole, password: "wrong", database: db.name}
			},
			wantError: "password authentication failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := setup(t)

			if tt.grant != "" {
				_, err := db.admin.Exec(t.Context(), tt.grant)
				require.NoError(t, err)
			}

			if tt.hold != "" {
				conn, _ := holdSession(t, db)
				_, err := conn.Exec(t.Context(), tt.hold)
				require.NoError(t, err)
			}

			tg := monitor(db)
			if tt.target != nil {
				tg = tt.target(db)
			}

			s, logs := newSource(t, tt.cfg, tg)

			if tt.query == "" {
				_, err := s.Call(t.Context(), source.Call{Tool: toolRunningQueries, Input: toolInput(t, map[string]any{"database": "orders"})})

				switch {
				case tt.wantErr != nil:
					require.ErrorIs(t, err, tt.wantErr)
				case tt.wantError != "":
					require.ErrorContains(t, err, tt.wantError)
					_, isToolErr := errors.AsType[*source.ToolError](err)
					require.False(t, isToolErr)
				default:
					require.NoError(t, err)
					require.Contains(t, logs.String(), tt.wantLog)
				}

				return
			}

			pool, err := s.pools.get(t.Context(), s.cfg.Databases[0])
			require.NoError(t, err)

			err = s.readOnly(t.Context(), pool, func(q querier) error {
				var out string

				return q.QueryRow(t.Context(), tt.query).Scan(&out)
			})

			pgErr, ok := errors.AsType[*pgconn.PgError](err)
			require.True(t, ok, "want a Postgres error, got %v", err)
			require.Equal(t, tt.wantCode, pgErr.Code)

			_, err = s.failure(t.Context(), toolRunningQueries, err)
			_, isToolErr := errors.AsType[*source.ToolError](err)
			require.True(t, isToolErr, "a query the database refused goes to the model")
		})
	}
}

// TestSessions checks how Sidekick's own sessions look on the database:
// named, and gone from the results.
func TestSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "Sidekick's sessions carry its name and are left out of running_queries"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := setup(t)
			s, _ := newSource(t, Config{}, monitor(db))

			res, err := s.Call(t.Context(), source.Call{Tool: toolRunningQueries, Input: toolInput(t, map[string]any{"database": "orders"})})
			require.NoError(t, err)
			require.True(t, res.NoData, "the call's own session is not listed")

			rows, err := db.admin.Query(t.Context(), "SELECT DISTINCT application_name FROM pg_stat_activity WHERE usename = $1 AND datname = $2", testMonitorRole, db.name)
			require.NoError(t, err)

			names, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err)
			require.Equal(t, []string{applicationName}, names)
		})
	}
}

// TestThroughTheGuardIntegration shows what the model sees for a real
// call: the result wrapped as untrusted data, with the email address in
// the query text redacted.
func TestThroughTheGuardIntegration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "running_queries through the guard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := setup(t)

			conn, pid := holdSession(t, db)
			background(t, conn, sleepSQL)
			waitFor(t, db, "the sleep to start", "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1 AND wait_event = 'PgSleep')", pid)

			s, _ := newSource(t, Config{}, monitor(db))

			r, err := redact.New(redact.Options{HashKey: []byte("test")})
			require.NoError(t, err)

			g, err := tools.New([]tools.Source{{Name: "db", Source: s}}, tools.Options{Redactor: r})
			require.NoError(t, err)

			run := g.Start(t.Context(), "inv-1", nil)

			res := run.Call(t.Context(), model.ToolCall{ID: "call-1", Name: "db__running_queries", Input: json.RawMessage(`{"database":"orders"}`)})
			require.False(t, res.ToolResult.IsError, res.ToolResult.Content)
			require.Contains(t, res.ToolResult.Content, "pg_sleep(30)")
			require.Contains(t, res.ToolResult.Content, fmt.Sprintf(`"pid":%d`, pid))
			require.NotContains(t, res.ToolResult.Content, "ada@example.com")
			require.Len(t, res.Evidence, 1)
		})
	}
}

// TestQueryPlans runs EXPLAIN ANALYZE on every fixed query as the monitor
// role, in a read-only transaction, against a seeded database with a lock
// wait, so each query is shown to run with pg_monitor alone. Run with -v to
// see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	view := pgx.Identifier{"public", "pg_stat_statements"}.Sanitize()

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{name: "the role check", sql: roleSQL},
		{name: "the timeouts", sql: timeoutsSQL, args: []any{"10000", "1000"}},
		{name: "the pg_stat_statements schema", sql: extensionSchemaSQL},
		{name: "slow_queries", sql: fmt.Sprintf(statementsSQL, view), args: []any{true, maxQueryLen, 0.0, orderMean, defaultLimit + 1}},
		{name: "running_queries", sql: activitySQL, args: []any{true, maxQueryLen, 0.0, defaultLimit + 1}},
		{name: "lock_waits: waiting sessions", sql: waitingSQL, args: []any{true, maxQueryLen, 0.0, defaultLimit + 1}},
		{name: "lock_waits: blocking sessions", sql: blockersSQL, args: []any{true, maxQueryLen, []int32{1, 2, 3}, maxLimit + 1}},
		{name: "replication_lag: the server", sql: serverSQL},
		{name: "replication_lag: standbys", sql: standbysSQL, args: []any{maxLimit + 1}},
	}

	db := setup(t)

	holder, _ := holdSession(t, db)
	_, err := holder.Exec(t.Context(), "BEGIN; UPDATE accounts SET balance = balance + 1 WHERE id = 1")
	require.NoError(t, err)

	waiter, waiterPID := holdSession(t, db)
	background(t, waiter, "UPDATE accounts SET balance = balance - 1 WHERE id = 1")
	waitFor(t, db, "the update to wait", "SELECT cardinality(pg_blocking_pids($1)) > 0", waiterPID)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _ := newSource(t, Config{}, monitor(db))

			pool, err := s.pools.get(t.Context(), s.cfg.Databases[0])
			require.NoError(t, err)

			tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
			require.NoError(t, err)

			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
				defer cancel()

				require.NoError(t, tx.Rollback(ctx))
			})

			explainer := storetest.NewExplainer(t, tx)
			_, err = explainer.Query(t.Context(), tt.sql, tt.args...)
			require.ErrorIs(t, err, storetest.ErrExplained)
			require.NotEmpty(t, explainer.Plan)
		})
	}
}

// toolInput returns a tool input as JSON.
func toolInput(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)

	return data
}
