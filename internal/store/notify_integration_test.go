//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	// updatesChannel is the channel of the investigation notifications.
	updatesChannel = "investigation_updates"
	// notifyMarker ends each test's wait: the notifications of a
	// transaction arrive in order, so what comes before the marker is all
	// the change sent.
	notifyMarker = "marker"
	// notifyTimeout bounds the wait for a notification.
	notifyTimeout = 10 * time.Second
	// notifyInvestigation is the ID of the investigation in
	// fixture/notify.sql.
	notifyInvestigation = "0192d3a4-0000-7000-8000-0000000000c1"
)

const insertStep = `INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms)
	VALUES ('` + notifyInvestigation + `', 0, 1, 'model_turn', '[]', 'ok', now(), 1)`

func TestInvestigationUpdateNotifications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// statements run in one transaction, which commits unless rollback
		// is set.
		statements []string
		rollback   bool
		want       []string
	}{
		{
			name:       "a saved step",
			statements: []string{insertStep},
			want:       []string{notifyInvestigation + ":step:0"},
		},
		{
			name:       "a status change",
			statements: []string{"UPDATE investigations SET status = 'final' WHERE id = '" + notifyInvestigation + "'"},
			want:       []string{notifyInvestigation + ":status:final"},
		},
		{
			name:       "a new run",
			statements: []string{"UPDATE investigations SET run = run + 1 WHERE id = '" + notifyInvestigation + "'"},
			want:       []string{notifyInvestigation + ":status:running"},
		},
		{
			name:       "a stop request",
			statements: []string{"UPDATE investigations SET cancel_requested_at = now() WHERE id = '" + notifyInvestigation + "'"},
			want:       []string{notifyInvestigation + ":status:running"},
		},
		{
			name:       "a confirmed verdict",
			statements: []string{"UPDATE investigations SET confirmed_verdict = 'true_positive', confirmed_at = now() WHERE id = '" + notifyInvestigation + "'"},
			want:       []string{notifyInvestigation + ":status:running"},
		},
		{
			name:       "counters only, as every step advances them",
			statements: []string{"UPDATE investigations SET next_seq = next_seq + 1, cost_micros = cost_micros + 5 WHERE id = '" + notifyInvestigation + "'"},
		},
		{
			name: "a status change and a step in one transaction",
			statements: []string{
				insertStep,
				"UPDATE investigations SET status = 'final' WHERE id = '" + notifyInvestigation + "'",
			},
			want: []string{notifyInvestigation + ":step:0", notifyInvestigation + ":status:final"},
		},
		{
			name: "a started watcher",
			statements: []string{`INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s,
				passes_to_recover, check_timeout_s, status, started_at, deadline)
				VALUES ('0192d3a4-0000-7000-8000-0000000000d1', '0192d3a4-0000-7000-8000-0000000000b1', '` + notifyInvestigation + `',
				'health', 'model', 'alert', '{}', 30, 2, 10, 'running', now(), now() + interval '1 hour')`},
			want: []string{notifyInvestigation + ":watchers"},
		},
		{
			name: "a watcher of no investigation",
			statements: []string{`INSERT INTO watchers (id, group_id, name, origin, check_type, spec, interval_s,
				passes_to_recover, check_timeout_s, status, started_at, deadline)
				VALUES ('0192d3a4-0000-7000-8000-0000000000d2', '0192d3a4-0000-7000-8000-0000000000b1',
				'health', 'model', 'alert', '{}', 30, 2, 10, 'running', now(), now() + interval '1 hour')`},
		},
		{
			name:       "a rolled back step",
			statements: []string{insertStep},
			rollback:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			storetest.LoadFixture(t, pool, "fixture/notify.sql")

			listener := listen(t, pool)

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			for _, statement := range tt.statements {
				_, err := tx.Exec(t.Context(), statement)
				require.NoError(t, err)
			}

			if tt.rollback {
				require.NoError(t, tx.Rollback(t.Context()))
			} else {
				_, err = tx.Exec(t.Context(), "SELECT pg_notify($1, $2)", updatesChannel, "before-commit")
				require.NoError(t, err)
				require.NoError(t, tx.Commit(t.Context()))
			}

			_, err = pool.Exec(t.Context(), "SELECT pg_notify($1, $2)", updatesChannel, notifyMarker)
			require.NoError(t, err)

			got := listener.receiveUntilMarker(t)

			if !tt.rollback {
				require.Equal(t, "before-commit", got[len(got)-1], "a transaction's notifications arrive together, in order")
				got = got[:len(got)-1]
			}

			if len(tt.want) == 0 {
				require.Empty(t, got)

				return
			}

			require.Equal(t, tt.want, got)
		})
	}
}

// listener is a connection that listens on updatesChannel.
type listener struct {
	conn *pgxpool.Conn
}

func listen(t *testing.T, pool *pgxpool.Pool) listener {
	t.Helper()

	conn, err := pool.Acquire(t.Context())
	require.NoError(t, err)

	t.Cleanup(conn.Release)

	_, err = conn.Exec(t.Context(), "LISTEN "+updatesChannel)
	require.NoError(t, err)

	return listener{conn: conn}
}

// receiveUntilMarker returns the payloads that arrive before the marker.
func (l listener) receiveUntilMarker(t *testing.T) []string {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), notifyTimeout)
	defer cancel()

	var got []string

	for {
		n, err := l.conn.Conn().WaitForNotification(ctx)
		require.NoError(t, err, "the marker never arrived")

		if n.Payload == notifyMarker {
			return got
		}

		got = append(got, n.Payload)
	}
}
