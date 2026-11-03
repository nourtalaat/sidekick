//go:build integration

package proposals

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each proposal query over 100,000
// proposals and checks that it uses the index it is meant to. The queries
// that filter by action run twice, as a pgx pool may run them: planned for
// the values given, and with a generic plan. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy.sql")
	storetest.LoadFixture(t, pool, "fixture/busy_actions.sql")

	// busyInvestigation is an investigation in the middle of the table,
	// busy its ten proposals' first, and proposed a proposal that is still
	// proposed.
	var busyInvestigation, busy, proposed uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('investigation' || 2500)::uuid, md5('proposal' || 2500 || '-' || 1)::uuid, md5('proposal' || 10 || '-' || 10)::uuid").Scan(&busyInvestigation, &busy, &proposed))

	now := time.Now()
	farFuture := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
		// generic plans the query as a prepared statement with a generic plan.
		generic bool
	}{
		{
			name: "InsertActionProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertActionProposal(ctx, db.InsertActionProposalParams{
					ID: uuid.New(), InvestigationID: busyInvestigation, Action: "monitor-note", Params: []byte(`{}`), Mode: "approved",
					IdempotencyKey: "sidekick/new", ProposedAt: now, ExpiresAt: now.Add(time.Hour),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on action_proposals"},
		},
		{
			name: "InsertUndoProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertUndoProposal(ctx, db.InsertUndoProposalParams{
					ID: uuid.New(), InvestigationID: busyInvestigation, Action: "monitor-note", Params: []byte(`{}`), Mode: "approved",
					IdempotencyKey: "sidekick/undo", Now: now, UndoOf: busy,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on action_proposals"},
		},
		{
			name: "MarkActionUndone",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.MarkActionUndone(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_pkey"},
		},
		{
			name: "CountInvestigationProposals",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CountInvestigationProposals(ctx, busyInvestigation)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_investigation"},
		},
		{
			name: "GetActionProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetActionProposal(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_pkey"},
		},
		{
			name: "GetActionProposalView",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetActionProposalView(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_pkey", "investigations_pkey", "users_pkey", "pull_request_states_pkey"},
		},
		{
			name: "LockActionProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockActionProposal(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_pkey", "investigations_pkey"},
		},
		{
			name: "DecideActionProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DecideActionProposal(ctx, db.DecideActionProposalParams{Status: "approved", Now: now, ID: proposed})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_pkey"},
		},
		{
			name: "StartActionExecution",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.StartActionExecution(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			// The primary key finds the row, and so does the status index, since
			// few proposals are approved at a time; the planner may take either.
			wantIndex: []string{"Index Scan using action_proposals_"},
		},
		{
			name: "FinishActionExecution",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.FinishActionExecution(ctx, db.FinishActionExecutionParams{Status: "succeeded", Now: now, ID: busy})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_pkey"},
		},
		{
			name: "RecordActionAttemptError",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.RecordActionAttemptError(ctx, db.RecordActionAttemptErrorParams{Error: new("failed"), ID: busy})

				return fmt.Errorf("run query: %w", err)
			},
			// The primary key finds the row, and so does the status index, since
			// few proposals are executing at a time; the planner may take either.
			wantIndex: []string{"Index Scan using action_proposals_"},
		},
		{
			name: "ExpireActionProposals",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ExpireActionProposals(ctx, db.ExpireActionProposalsParams{Now: now.Add(48 * time.Hour), Batch: 100})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_expiry"},
		},
		{
			name: "ListActionProposals",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "proposed", BeforeAt: farFuture, BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed", "investigations_pkey", "pull_request_states_pkey"},
		},
		{
			name: "ListActionProposalsDecided",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "succeeded", BeforeAt: farFuture, BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed", "investigations_pkey", "pull_request_states_pkey"},
		},
		{
			name: "ListActionProposalsByAction",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "failed", Action: new("slack-react"), BeforeAt: farFuture, BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed", "investigations_pkey"},
		},
		{
			name: "ListActionProposalsByRareAction",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "failed", Action: new("monitor-rollback"), BeforeAt: farFuture, BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed", "investigations_pkey"},
		},
		{
			name: "ListActionProposalsByRareActionAfterCursor",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "succeeded", Action: new("monitor-rollback"), BeforeAt: now.Add(-100 * time.Hour), BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			// A rare action is found through the index on the action, which the
			// daily count made; the planner may take either index.
			wantIndex: []string{"Index Scan using action_proposals_", "investigations_pkey"},
		},
		{
			name: "ListActionProposalsGeneric",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "succeeded", BeforeAt: farFuture, BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed", "investigations_pkey"},
			generic:   true,
		},
		{
			name: "ListActionProposalsByActionGeneric",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "failed", Action: new("slack-react"), BeforeAt: farFuture, BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed", "investigations_pkey"},
			generic:   true,
		},
		{
			name: "ListActionProposalsByRareActionGeneric",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListActionProposals(ctx, db.ListActionProposalsParams{
					Status: "failed", Action: new("monitor-rollback"), BeforeAt: farFuture, BeforeID: uuid.Max, PageSize: 51,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed", "investigations_pkey"},
			generic:   true,
		},
		{
			name: "ListInvestigationActionProposals",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListInvestigationActionProposals(ctx, busyInvestigation)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_investigation", "investigations_pkey", "pull_request_states_pkey"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)
			if tt.generic {
				explain = storetest.NewGenericExplainer(t, tx)
			}

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			// users and api_tokens are read by key; reading either whole is
			// a plan that would not scale.
			require.NotContains(t, strings.ReplaceAll(explain.Plan, "Seq Scan on api_tokens", ""), "Seq Scan")
		})
	}
}
