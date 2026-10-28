//go:build integration

package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The sizes the plan tests' queries ask for.
const (
	planPage      = 51
	planEvents    = 50
	planFollowUps = 50
	planStepsPage = 101
	planChecks    = 50
	// planMiddleRow is the row whose time and ID the page with a cursor
	// from the middle of the list starts after.
	planMiddleRow = 50000
	// planSteps is how many steps the investigation of the detail plans has.
	planSteps = 5000
)

// TestListQueryPlans runs EXPLAIN ANALYZE on the investigation list query
// with each filter, over a database with 100,000 investigations, and checks
// that it reads the page through the index of the filter, never scans the
// investigations whole, and finds the feedback and the watcher of each row
// through their indexes. Run with -v to see the plans.
func TestListQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	now := time.Now()

	tests := []struct {
		name string
		// params sets the filters; the page size is set for every case.
		params    func(p *db.ListInvestigationsParams)
		wantIndex string
	}{
		{name: "NoFilter", params: func(*db.ListInvestigationsParams) {}, wantIndex: "investigations_list"},
		{
			name: "TimeRange",
			params: func(p *db.ListInvestigationsParams) {
				from, to := now.Add(-30*24*time.Hour), now.Add(-20*24*time.Hour)
				p.CreatedFrom, p.CreatedTo = &from, &to
			},
			wantIndex: "investigations_list",
		},
		{name: "Route", params: func(p *db.ListInvestigationsParams) { p.Route = new("route-7") }, wantIndex: "investigations_list_route"},
		{name: "Service", params: func(p *db.ListInvestigationsParams) { p.Service = new("svc-7") }, wantIndex: "investigations_list_service"},
		{name: "Model", params: func(p *db.ListInvestigationsParams) { p.Model = new("model-7") }, wantIndex: "investigations_list_model"},
		{name: "Verdict", params: func(p *db.ListInvestigationsParams) { p.Verdict = new("flapping") }, wantIndex: "investigations_list_verdict"},
		{name: "Status", params: func(p *db.ListInvestigationsParams) { p.Status = new("failed") }, wantIndex: "investigations_list_status"},
		{name: "Text", params: func(p *db.ListInvestigationsParams) { p.Search = new("zebra") }, wantIndex: "investigations_list_title"},
		{
			name: "RouteWithACursor",
			params: func(p *db.ListInvestigationsParams) {
				p.Route = new("route-7")
				p.CursorAt = new(now.Add(-planMiddleRow * time.Minute))
				p.CursorID = new(uuid.New())
			},
			wantIndex: "investigations_list_route",
		},
		{
			name: "CursorFromTheMiddle",
			params: func(p *db.ListInvestigationsParams) {
				p.CursorAt = new(now.Add(-planMiddleRow * time.Minute))
				p.CursorID = new(uuid.New())
			},
			wantIndex: "investigations_list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			params := db.ListInvestigationsParams{PageSize: planPage}
			tt.params(&params)

			explain := storetest.NewExplainer(t, tx)

			_, err = db.New(explain).ListInvestigations(t.Context(), params)
			require.ErrorIs(t, err, storetest.ErrExplained)

			require.Contains(t, explain.Plan, tt.wantIndex)
			require.Contains(t, explain.Plan, "feedback_investigation")
			require.Contains(t, explain.Plan, "watchers_group_name")
			require.NotContains(t, explain.Plan, "Seq Scan on investigations")
		})
	}
}

// TestDetailQueryPlans runs EXPLAIN ANALYZE on the queries of the
// investigation page and its steps, and checks that each reads through an
// index.
func TestDetailQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	var (
		id, group, watcher uuid.UUID
	)

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('investigation' || 3)::uuid, md5('group' || 3)::uuid, md5('watcher' || 1)::uuid").Scan(&id, &group, &watcher))

	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms)
			SELECT '%s', n, 1, 'model_turn', '[]', 'ok', now(), 1 FROM generate_series(0, %d) AS n`, id, planSteps-1),
		fmt.Sprintf(`INSERT INTO investigation_inbox (investigation_id, kind, body, key, created_at)
			SELECT '%s', 'follow_up', '{}', 'k' || n, now() FROM generate_series(1, 20) AS n`, id),
		fmt.Sprintf(`INSERT INTO events (id, intake, key, kind, trusted, fingerprint, status, group_id, received_at)
			SELECT md5('extra' || n)::uuid, 'am', 'extra-' || n, 'alert', true, 'fp-3', 'grouped', '%s', now() FROM generate_series(1, 200) AS n`, group),
		fmt.Sprintf(`INSERT INTO watcher_checks (watcher_id, seq, at, result, duration_ms)
			SELECT '%s', n, now(), 'pass', 1 FROM generate_series(6, 2005) AS n`, watcher),
		"ANALYZE",
	} {
		_, err := pool.Exec(t.Context(), statement)
		require.NoError(t, err)
	}

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "GetInvestigationDetail",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetInvestigationDetail(ctx, id)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey", "event_groups_pkey"},
		},
		{
			name: "GetInvestigationGroup",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetInvestigationGroup(ctx, id)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "ListGroupEvents",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListGroupEvents(ctx, db.ListGroupEventsParams{GroupID: &group, PageSize: planEvents})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"events_group"},
		},
		{
			name: "ListPendingFollowUps",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListPendingFollowUps(ctx, db.ListPendingFollowUpsParams{InvestigationID: id, PageSize: planFollowUps})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigation_inbox_waiting"},
		},
		{
			name: "ListStepsPage",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListStepsPage(ctx, db.ListStepsPageParams{InvestigationID: id, After: planSteps / 2, PageSize: planStepsPage})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"steps_pkey"},
		},
		{
			name: "ListStepsPageOfARun",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListStepsPage(ctx, db.ListStepsPageParams{InvestigationID: id, After: -1, Run: new(int32(1)), PageSize: planStepsPage})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"steps_pkey"},
		},
		{
			name: "ListRecentWatcherChecks",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListRecentWatcherChecks(ctx, db.ListRecentWatcherChecksParams{WatcherIds: []uuid.UUID{watcher}, PerWatcher: planChecks})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watcher_checks_pkey"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func(tx pgx.Tx) { _ = tx.Rollback(t.Context()) }(tx)

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}
