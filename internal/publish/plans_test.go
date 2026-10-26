//go:build integration

package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// errExplained stops a query after explainer has run its EXPLAIN ANALYZE.
var errExplained = errors.New("explained")

// TestQueryPlans runs EXPLAIN ANALYZE on each publish query over 50,000
// investigations and checks that it uses the index it is meant to. Run
// with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")
	storetest.LoadFixture(t, pool, "fixture/busy.sql")

	// busy is one of the seeded investigations, in the middle of the table.
	var busy uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('inv' || 25000)::uuid").Scan(&busy))

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "InsertPublishState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertPublishState(ctx, db.InsertPublishStateParams{
					InvestigationID: webhookInvestigation, Sinks: []string{slackSink}, Phase: "queued", Body: json.RawMessage(`{}`),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on publish_states"},
		},
		{
			name: "UpdatePublishState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpdatePublishState(ctx, db.UpdatePublishStateParams{
					InvestigationID: busy, Seq: 6, Phase: "running", Body: json.RawMessage(`{}`),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"publish_states_pkey"},
		},
		{
			name: "GetPublishState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetPublishState(ctx, db.GetPublishStateParams{InvestigationID: busy, Sink: slackSink})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"publish_states_pkey", "investigations_pkey", "event_groups_pkey", "output_refs_pkey"},
		},
		{
			name: "LockPublishSeq",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockPublishSeq(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"publish_states_pkey"},
		},
		{
			name: "SaveOutputRef",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.SaveOutputRef(ctx, db.SaveOutputRefParams{
					InvestigationID: busy, Sink: slackSink, External: "C123:1.2", Ref: json.RawMessage(`{}`), LastSeq: 5,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"output_refs_pkey"},
		},
		{
			name: "GetInvestigationWithEvent",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetInvestigationWithEvent(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey", "events_group"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := &explainer{tx: tx}

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), errExplained)
			t.Logf("EXPLAIN ANALYZE of %s:\n%s", tt.name, explain.plan)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.plan, index)
			}

			require.NotContains(t, explain.plan, "Seq Scan")
		})
	}
}

// explainer is a db.DBTX that runs EXPLAIN ANALYZE on each query instead of
// the query itself, keeps the plan, and returns errExplained. EXPLAIN
// ANALYZE runs the query, so it runs inside a transaction that the test
// rolls back.
type explainer struct {
	tx   pgx.Tx
	plan string
}

func (e *explainer) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, e.explain(ctx, sql, args)
}

func (e *explainer) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	return nil, e.explain(ctx, sql, args)
}

func (e *explainer) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return errorRow{err: e.explain(ctx, sql, args)}
}

func (e *explainer) explain(ctx context.Context, sql string, args []interface{}) error {
	rows, err := e.tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		return fmt.Errorf("explain: %w", err)
	}

	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}

	e.plan = strings.Join(lines, "\n")

	return errExplained
}

// errorRow is a pgx.Row whose Scan returns err.
type errorRow struct {
	err error
}

func (r errorRow) Scan(...interface{}) error {
	return r.err
}
