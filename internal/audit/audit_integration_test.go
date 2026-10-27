//go:build integration

package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

var investigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000a1")

func TestWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		entry       audit.Entry
		want        db.AuditLog
		wantDetails string
	}{
		{
			name: "a user changes a route",
			entry: audit.Entry{
				ActorKind: audit.ActorUser, ActorID: "U123", Action: "route.updated",
				TargetKind: "route", TargetID: "checkout",
				Old: map[string]any{"priority": 100}, New: map[string]any{"priority": 50},
			},
			want: db.AuditLog{
				ActorKind: audit.ActorUser, ActorID: ptr("U123"), Action: "route.updated",
				TargetKind: ptr("route"), TargetID: ptr("checkout"),
			},
			wantDetails: `{"old": {"priority": 100}, "new": {"priority": 50}}`,
		},
		{
			name: "a new listener has no old value",
			entry: audit.Entry{
				ActorKind: audit.ActorUser, ActorID: "U123", Action: "listener.created",
				TargetKind: "listener", TargetID: "alerts", New: map[string]any{"channel": "C1"},
			},
			want: db.AuditLog{
				ActorKind: audit.ActorUser, ActorID: ptr("U123"), Action: "listener.created",
				TargetKind: ptr("listener"), TargetID: ptr("alerts"),
			},
			wantDetails: `{"new": {"channel": "C1"}}`,
		},
		{
			name:  "the system acts on an investigation",
			entry: audit.Entry{ActorKind: audit.ActorSystem, Action: "action.expired", InvestigationID: &investigation},
			want: db.AuditLog{
				ActorKind: audit.ActorSystem, Action: "action.expired", InvestigationID: &investigation,
			},
			wantDetails: `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			before := time.Now()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)
			require.NoError(t, audit.NewWriter().Write(t.Context(), tx, tt.entry))
			require.NoError(t, tx.Commit(t.Context()))

			rows := readAll(t, pool)
			require.Len(t, rows, 1)

			got := rows[0]
			require.WithinRange(t, got.At, before.Add(-time.Second), time.Now().Add(time.Second))
			require.JSONEq(t, tt.wantDetails, string(got.Details))

			got.ID, got.At, got.Details = 0, time.Time{}, nil
			require.Equal(t, tt.want, got)
		})
	}
}

func TestWriteRollsBackWithTheChange(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)

	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	require.NoError(t, audit.NewWriter().Write(t.Context(), tx, audit.Entry{ActorKind: audit.ActorUser, Action: "route.updated"}))
	require.NoError(t, tx.Rollback(t.Context()))

	require.Empty(t, readAll(t, pool))
}

func TestUpdateIsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
	}{
		{name: "one row", query: "UPDATE audit_log SET action = 'changed' WHERE id = 1"},
		{name: "no rows", query: "UPDATE audit_log SET action = 'changed' WHERE id = -1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			require.NoError(t, audit.NewWriter().Write(t.Context(), pool, audit.Entry{ActorKind: audit.ActorUser, Action: "route.updated"}))

			_, err := pool.Exec(t.Context(), tt.query)
			require.ErrorContains(t, err, "audit_log rows cannot be updated")

			rows := readAll(t, pool)
			require.Len(t, rows, 1)
			require.Equal(t, "route.updated", rows[0].Action)
		})
	}
}

// TestQueryPlans runs EXPLAIN ANALYZE on each audit query over 50,000
// entries. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy.sql")

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "InsertAuditEntry",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
					At: time.Now(), ActorKind: audit.ActorUser, Action: "route.updated", Details: json.RawMessage(`{}`),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on audit_log"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := &storetest.Explainer{Tx: tx}

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)
			t.Logf("EXPLAIN ANALYZE of %s:\n%s", tt.name, explain.Plan)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}

func readAll(t *testing.T, pool *pgxpool.Pool) []db.AuditLog {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT * FROM audit_log ORDER BY id")
	require.NoError(t, err)

	all, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.AuditLog])
	require.NoError(t, err)

	return all
}

func ptr(s string) *string {
	return &s
}
