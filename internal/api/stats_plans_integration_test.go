//go:build integration

package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	planYear  = 365
	planMonth = 30
)

// planCase is one query of the plan tests, run through an explainer.
type planCase struct {
	name string
	run  func(ctx context.Context, q *db.Queries) error
	// wantIndex lists names the plan must contain, such as the index of
	// the primary key.
	wantIndex []string
	// scansTable says the plan may read the table whole, since its range
	// is most of it.
	scansTable bool
}

// TestStatsQueryPlans runs EXPLAIN ANALYZE on each query of the stats
// endpoints over two years of rollups, for a range of a year and a range of
// a month. A month must use the primary key of its table and read no table
// whole; a year reads half of the rows, so its plan is only recorded. Run
// with -v to see the plans.
func TestStatsQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/stats_plans.sql")

	today := time.Now().UTC().Truncate(24 * time.Hour)

	// Every case is run for both ranges, and with and without filters.
	var tests []planCase

	for _, r := range []struct {
		name string
		days int
	}{{"Year", planYear}, {"Month", planMonth}} {
		from, to := pgDate(today.AddDate(0, 0, -(r.days-1))), pgDate(today)
		index := r.days == planMonth

		tests = append(tests, qualityPlans(r.name, from, to, index, filterSet{})...)
		filters := filterSet{route: new("route-1"), service: new("service-3"), model: new("model-2"), verdict: new("false_positive")}

		tests = append(tests, qualityPlans(r.name+"Filtered", from, to, index, filters)...)

		fromTime, toTime := from.Time, to.Time.AddDate(0, 0, 1)

		tests = append(tests, costPlans(r.name, from, to, fromTime, toTime, index, filterSet{})...)
		tests = append(tests, costPlans(r.name+"Filtered", from, to, fromTime, toTime, index, filters)...)
	}

	tests = append(tests,
		planCase{
			name: "StatsFilterRoutes",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.StatsFilterRoutes(ctx, today.AddDate(0, 0, -400))

				return fmt.Errorf("run query: %w", err)
			},
			scansTable: true,
		},
		planCase{
			name: "StatsFilterServices",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.StatsFilterServices(ctx, today.AddDate(0, 0, -400))

				return fmt.Errorf("run query: %w", err)
			},
			scansTable: true,
		},
		planCase{
			name: "StatsFilterModels",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.StatsFilterModels(ctx, today.AddDate(0, 0, -400))

				return fmt.Errorf("run query: %w", err)
			},
			scansTable: true,
		},
	)

	runPlanCases(t, pool, tests)
}

// filterSet are the dimension filters of a plan case.
type filterSet struct {
	route, service, model, verdict *string
}

// qualityPlans are the plan cases of the four quality queries for a range.
// A range that must use the index also reads no table whole.
func qualityPlans(prefix string, from, to pgtype.Date, index bool, f filterSet) []planCase {
	wantIndex := []string(nil)
	if index {
		wantIndex = []string{"quality_daily_pkey"}
	}

	return []planCase{
		{
			name: prefix + "QualityTotals",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.QualityTotals(ctx, db.QualityTotalsParams{FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: wantIndex, scansTable: !index,
		},
		{
			name: prefix + "QualityByDay",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.QualityByDay(ctx, db.QualityByDayParams{FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: wantIndex, scansTable: !index,
		},
		{
			name: prefix + "QualityMatrix",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.QualityMatrix(ctx, db.QualityMatrixParams{FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: wantIndex, scansTable: !index,
		},
		{
			name: prefix + "QualityComparison",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.QualityComparison(ctx, db.QualityComparisonParams{FromDay: from, ToDay: to, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: wantIndex, scansTable: !index,
		},
	}
}

// costPlans are the plan cases of the cost queries for a range. The spend
// queries take the route and model filters; the cost per investigation
// takes the hours of the range as well, and every filter.
func costPlans(prefix string, from, to pgtype.Date, fromHour, toHour time.Time, index bool, f filterSet) []planCase {
	spendIndex, statsIndex := []string(nil), []string(nil)
	if index {
		spendIndex, statsIndex = []string{"spend_daily_pkey"}, []string{"stats_hourly_pkey"}
	}

	return []planCase{
		{
			name: prefix + "SpendTotals",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.SpendTotals(ctx, db.SpendTotalsParams{FromDay: from, ToDay: to, Route: f.route, Model: f.model})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: spendIndex, scansTable: !index,
		},
		{
			name: prefix + "SpendByDayModel",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.SpendByDayModel(ctx, db.SpendByDayModelParams{FromDay: from, ToDay: to, Route: f.route, Model: f.model})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: spendIndex, scansTable: !index,
		},
		{
			name: prefix + "SpendByRoute",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.SpendByRoute(ctx, db.SpendByRouteParams{FromDay: from, ToDay: to, Route: f.route, Model: f.model})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: spendIndex, scansTable: !index,
		},
		{
			name: prefix + "CostPerInvestigationByDay",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CostPerInvestigationByDay(ctx, db.CostPerInvestigationByDayParams{
					FromHour: fromHour, ToHour: toHour, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: statsIndex, scansTable: !index,
		},
		{
			name: prefix + "CostPerInvestigationTotals",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CostPerInvestigationTotals(ctx, db.CostPerInvestigationTotalsParams{
					FromHour: fromHour, ToHour: toHour, Route: f.route, Service: f.service, Model: f.model, Verdict: f.verdict,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: statsIndex, scansTable: !index,
		},
	}
}

// runPlanCases runs each case as a subtest through an explainer, and checks
// the plan of each.
func runPlanCases(t *testing.T, pool *pgxpool.Pool, tests []planCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, name := range tt.wantIndex {
				require.Contains(t, explain.Plan, name)
			}

			if !tt.scansTable {
				require.NotContains(t, explain.Plan, "Seq Scan")
			}
		})
	}
}

// pgDate is t as a database date.
func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}
