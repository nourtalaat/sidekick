//go:build integration

package store_test

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

const (
	planScenarios = 20000
	planGroups    = 2000
	planPageSize  = 50
)

// TestScenarioQueryPlans runs EXPLAIN ANALYZE on each query of the scenarios
// over 20000 scenarios and 20000 events, and checks that each one uses an
// index. Run with -v to see the plans.
func TestScenarioQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	now := time.Now().UTC().Truncate(time.Second)

	userID := uuid.New()
	groupID := uuid.New()

	seed, err := pool.Begin(t.Context())
	require.NoError(t, err)

	_, err = seed.Exec(t.Context(), `
INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
VALUES ($1, 'https://issuer.example', 'subject-1', 'Plan Reviewer', 'admin', now(), now())`, userID)
	require.NoError(t, err)

	_, err = seed.Exec(t.Context(), `
INSERT INTO scenarios (id, title, status, body, provenance, notes, reviewed_by, reviewed_at, created_at, updated_at)
SELECT 'scenario-' || i, 'title ' || i, CASE WHEN i % 10 = 0 THEN 'reviewed' ELSE 'draft' END, 'id: x', '[]', '[]',
       CASE WHEN i % 10 = 0 THEN $1::uuid END, CASE WHEN i % 10 = 0 THEN now() END,
       now(), now() - (i || ' minutes')::interval
FROM generate_series(1, $2) AS i`, userID, planScenarios)
	require.NoError(t, err)

	_, err = seed.Exec(t.Context(), `
INSERT INTO events (id, intake, key, kind, trusted, labels, fingerprint, status, raw, group_id, received_at)
SELECT gen_random_uuid(), 'alertmanager', 'key-' || i, 'alert', false, '{}', 'fp-' || i, 'grouped', '{}',
       CASE WHEN i % $2 = 0 THEN $3::uuid ELSE gen_random_uuid() END, now() - (i || ' seconds')::interval
FROM generate_series(1, $4) AS i`, planScenarios, planGroups, groupID, planScenarios)
	require.NoError(t, err)

	_, err = seed.Exec(t.Context(), `ANALYZE scenarios, events`)
	require.NoError(t, err)
	require.NoError(t, seed.Commit(t.Context()))

	cursorAt := now.Add(-time.Hour)
	cursorID := "scenario-60"

	tests := []struct {
		name string
		run  func(ctx context.Context, q *db.Queries) error
		// wantIndex lists names the plan must contain.
		wantIndex []string
		// scansTable says the plan may read the table whole, since the
		// query returns a share of it.
		scansTable bool
	}{
		{
			name: "InsertScenario",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertScenario(ctx, db.InsertScenarioParams{
					ID: "new-scenario", Title: "t", Body: "id: new-scenario", Provenance: []byte(`[]`), Notes: []byte(`[]`), Now: now,
				})

				return explained(err)
			},
			wantIndex: []string{"Insert on scenarios", "scenarios_investigation"},
		},
		{
			name: "GetScenario",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetScenario(ctx, "scenario-77")
				return explained(err)
			},
			wantIndex: []string{"scenarios_pkey"},
		},
		{
			name: "GetScenarioByInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetScenarioByInvestigation(ctx, &groupID)

				return explained(err)
			},
			wantIndex: []string{"scenarios_investigation"},
		},
		{
			name: "GetReviewedScenario",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetReviewedScenario(ctx, "scenario-70")
				return explained(err)
			},
			wantIndex: []string{"scenarios_pkey"},
		},
		{
			name: "ListReviewedScenarios",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListReviewedScenarios(ctx)
				return explained(err)
			},
			wantIndex:  []string{"scenarios"},
			scansTable: true,
		},
		{
			name: "ListScenarios",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListScenarios(ctx, db.ListScenariosParams{PageSize: planPageSize})

				return explained(err)
			},
			wantIndex: []string{"scenarios_updated"},
		},
		{
			name: "ListScenariosAfterCursor",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListScenarios(ctx, db.ListScenariosParams{CursorAt: &cursorAt, CursorID: &cursorID, PageSize: planPageSize})

				return explained(err)
			},
			wantIndex: []string{"scenarios_updated"},
		},
		{
			name: "ListScenariosByStatus",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListScenariosByStatus(ctx, db.ListScenariosByStatusParams{Status: "draft", PageSize: planPageSize})

				return explained(err)
			},
			wantIndex: []string{"scenarios_status_updated"},
		},
		{
			name: "UpdateScenarioBody",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpdateScenarioBody(ctx, db.UpdateScenarioBodyParams{Title: "t", Body: "b", Now: now, ID: "scenario-77", Revision: 1})

				return explained(err)
			},
			wantIndex: []string{"scenarios_pkey"},
		},
		{
			name: "ReviewScenario",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ReviewScenario(ctx, db.ReviewScenarioParams{ReviewedBy: &userID, Now: now, ID: "scenario-77", Revision: 1})

				return explained(err)
			},
			wantIndex: []string{"scenarios_pkey"},
		},
		{
			name: "GetFirstAlertEventOfGroup",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetFirstAlertEventOfGroup(ctx, groupID)

				return explained(err)
			},
			wantIndex: []string{"events_group"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained, fmt.Sprintf("query %s", tt.name))

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			if !tt.scansTable {
				require.NotContains(t, explain.Plan, "Seq Scan")
			}
		})
	}
}

// explained wraps the error of a query run through an explainer.
func explained(err error) error {
	return fmt.Errorf("run query: %w", err)
}
