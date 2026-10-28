//go:build integration

package api_test

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The sizes the plan tests of the audit query ask for.
const (
	auditPlanPage = 51
	// auditPlanMiddleRow is the row whose time the page with a cursor
	// from the middle of the log starts after.
	auditPlanMiddleRow = 50000
	// auditPlanMaxFiltered is the most rows a page may read and drop
	// because they fail a filter. A page of 51 rows that the plan reads
	// through an index of the filter drops none. When the plan reads the
	// log in time order and drops what fails the filter, it drops about
	// 51 times the share of rows that fail it, which is a few thousand for
	// the commonest filter here and the whole log for a rare one.
	auditPlanMaxFiltered = 5000
)

var rowsRemoved = regexp.MustCompile(`Rows Removed by Filter: (\d+)`)

// TestAuditQueryPlans runs EXPLAIN ANALYZE on the audit list query with
// each filter, over a database with 100,000 audit entries, and checks that
// it never scans the log whole and never reads and drops more than
// auditPlanMaxFiltered rows to find a page. Run with -v to see the plans.
func TestAuditQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/audit_plans.sql")

	var investigation uuid.UUID

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('investigation' || 5)::uuid").Scan(&investigation))

	now := time.Now()

	tests := []struct {
		name string
		// params sets the filters; the page size is set for every case.
		params func(p *db.ListAuditEntriesParams)
	}{
		{name: "NoFilter", params: func(*db.ListAuditEntriesParams) {}},
		{
			name: "TimeRange",
			params: func(p *db.ListAuditEntriesParams) {
				from, to := now.Add(-30*24*time.Hour), now.Add(-20*24*time.Hour)
				p.AtFrom, p.AtTo = &from, &to
			},
		},
		{name: "Actor", params: func(p *db.ListAuditEntriesParams) { p.ActorID = new("user-7") }},
		{name: "ActorWhoIsRare", params: func(p *db.ListAuditEntriesParams) { p.ActorID = new("user-rare") }},
		{name: "ActorKind", params: func(p *db.ListAuditEntriesParams) { p.ActorKind = new("policy") }},
		{name: "Action", params: func(p *db.ListAuditEntriesParams) { p.Action = new("action.3") }},
		{name: "ActionThatIsRare", params: func(p *db.ListAuditEntriesParams) { p.Action = new("action.rare") }},
		{name: "Investigation", params: func(p *db.ListAuditEntriesParams) { p.InvestigationID = &investigation }},
		{
			name: "ActorAndAction",
			params: func(p *db.ListAuditEntriesParams) {
				p.ActorID, p.Action = new("user-rare"), new("action.rare")
			},
		},
		{
			name: "ActorAndTimeRange",
			params: func(p *db.ListAuditEntriesParams) {
				from := now.Add(-30 * 24 * time.Hour)
				p.ActorID, p.AtFrom = new("user-rare"), &from
			},
		},
		{
			name: "CursorFromTheMiddle",
			params: func(p *db.ListAuditEntriesParams) {
				p.CursorAt, p.CursorID = new(now.Add(-auditPlanMiddleRow*time.Minute)), new(int64(auditPlanMiddleRow))
			},
		},
		{
			name: "ActorWithACursor",
			params: func(p *db.ListAuditEntriesParams) {
				p.ActorID = new("user-7")
				p.CursorAt, p.CursorID = new(now.Add(-auditPlanMiddleRow*time.Minute)), new(int64(auditPlanMiddleRow))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			params := db.ListAuditEntriesParams{PageSize: auditPlanPage}
			tt.params(&params)

			explain := storetest.NewExplainer(t, tx)

			_, err = db.New(explain).ListAuditEntries(t.Context(), params)
			require.ErrorIs(t, err, storetest.ErrExplained)

			require.NotContains(t, explain.Plan, "Seq Scan on audit_log")
			require.LessOrEqual(t, removedByFilters(t, explain.Plan), auditPlanMaxFiltered, "the plan reads and drops too many rows")
		})
	}
}

// removedByFilters adds up the rows the plan dropped because they failed a
// filter.
func removedByFilters(t *testing.T, plan string) int {
	t.Helper()

	total := 0

	for _, m := range rowsRemoved.FindAllStringSubmatch(plan, -1) {
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)

		total += n
	}

	return total
}
