//go:build integration

package gitchange

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each query of the status job over
// a busy install, and checks that it uses the index it is meant to. Run
// with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	var proposal uuid.UUID

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT proposal_id FROM pull_request_states LIMIT 1").Scan(&proposal))

	// investigation is the investigation of the proposals, and undo a
	// succeeded undo proposal that has no CI report, for the insert.
	var investigation, undo uuid.UUID

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT investigation_id, md5('undo' || 1)::uuid FROM action_proposals WHERE id = $1", proposal).Scan(&investigation, &undo))

	now := time.Now()

	tests := []struct {
		name string
		// run calls the query through q, which explains it instead of
		// running it.
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
		// notWant are texts the plan must not contain.
		notWant []string
	}{
		{
			name: "ListPullRequestsToCheck",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListPullRequestsToCheck(ctx, db.ListPullRequestsToCheckParams{
					Actions: []string{"monitor-pr"}, Since: time.Now().Add(-TrackedFor), Before: time.Now(), Batch: MaxChecksPerRun,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_daily", "action_proposals_pkey"},
			notWant:   []string{"Seq Scan on action_proposals"},
		},
		{
			name: "RecordPullRequestState",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.RecordPullRequestState(ctx, db.RecordPullRequestStateParams{
					ProposalID: proposal, Repo: "acme/monitors", Number: 7, Url: "https://github.com/acme/monitors/pull/7", State: StateOpen, Now: time.Now(),
				}))
			},
			wantIndex: []string{"pull_request_states_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "UpsertPullRequestState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpsertPullRequestState(ctx, db.UpsertPullRequestStateParams{
					ProposalID: proposal, Repo: "acme/monitors", Number: 7, Url: "https://github.com/acme/monitors/pull/7", State: StateOpen,
					Now: time.Now(), JobID: 1, ClaimToken: 1,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"pull_request_states_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "GetPullRequestState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetPullRequestState(ctx, proposal)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"pull_request_states_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "PullRequestJobExists",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.PullRequestJobExists(ctx, uniqueKeyPrefix+time.Now().UTC().Format(time.DateOnly))

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"jobs_check_pull_requests_key"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "InsertCIReport",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertCIReport(ctx, db.InsertCIReportParams{ProposalID: undo, Now: now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"ci_reports_pkey"},
		},
		{
			name: "GetCIReportForJob",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetCIReportForJob(ctx, proposal)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"ci_reports_pkey", "action_proposals_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "LockCIReport",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockCIReport(ctx, proposal)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"ci_reports_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "RecordCICheck",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.RecordCICheck(ctx, db.RecordCICheckParams{ProposalID: proposal, Now: &now, Detail: []byte(`{}`)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"ci_reports_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "FinishCIReport",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.FinishCIReport(ctx, db.FinishCIReportParams{ProposalID: proposal, State: "passed", Detail: []byte(`{}`), Now: &now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"ci_reports_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "SetCIReportSeq",
			run: func(ctx context.Context, q *db.Queries) error {
				seq := int64(7)

				return fmt.Errorf("run query: %w", q.SetCIReportSeq(ctx, db.SetCIReportSeqParams{ProposalID: proposal, Seq: &seq}))
			},
			wantIndex: []string{"ci_reports_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			// All the proposals of this fixture are in one investigation, far
			// more than a real one has, so the plan only has to be logged.
			name: "ListUnsentCIReports",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListUnsentCIReports(ctx, db.ListUnsentCIReportsParams{InvestigationID: investigation, MaxSeq: 1 << 40, Sink: "webhook"})

				return fmt.Errorf("run query: %w", err)
			},
		},
		{
			name: "MarkCIReportSent",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.MarkCIReportSent(ctx, db.MarkCIReportSentParams{ProposalID: proposal, Sink: "webhook"}))
			},
			wantIndex: []string{"ci_reports_pkey"},
			notWant:   []string{"Seq Scan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			for _, text := range tt.notWant {
				require.NotContains(t, explain.Plan, text)
			}
		})
	}
}
