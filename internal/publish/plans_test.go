//go:build integration

package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

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
			name: "RefreshPublishState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.RefreshPublishState(ctx, busy)

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
		{
			name: "ClaimNotice",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ClaimNotice(ctx, db.ClaimNoticeParams{Key: "daily_cap:route:route-5000", SentAt: time.Now(), NotBefore: time.Now().Add(-time.Hour)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on notices", "Conflict Arbiter Indexes: notices_pkey"},
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
