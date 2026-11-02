//go:build integration

package linear

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The rows of the proposals package's fixture/investigations.sql: the
// checkout investigation, whose route payments may propose in approved
// mode, and Ana, an approver.
var (
	checkoutInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	anaUser               = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000d1")
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is the proposals service with a linear.issue action that talks
// to a fake Linear, on a database of its own.
type harness struct {
	pool   *pgxpool.Pool
	svc    *proposals.Service
	linear *fakeLinear
	api    *fakehttp.Server
}

// setup builds the harness. The action allows the team OPS and maxPerDay
// issues a day.
func setup(t *testing.T, maxPerDay int) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "../proposals/fixture/investigations.sql")
	require.NoError(t, publish.Open(t.Context(), pool, checkoutInvestigation, []string{"slack"}))

	f, api := newServer(t)

	deps := testDeps(api, testAPIKey)
	deps.DB = pool

	issue, err := NewIssue(t.Context(), "ops-issue", IssueConfig{
		Connection: connection(api),
		Limits:     IssueLimits{Teams: []string{"OPS"}, Labels: []string{"incident"}, MaxPerDay: maxPerDay},
	}, deps)
	require.NoError(t, err)

	svc, err := proposals.New(proposals.Options{
		Pool:    pool,
		Actions: map[string]action.Action{"ops-issue": issue},
		Policy:  action.StaticPolicy{"payments": {Mode: action.Approved, Actions: []string{"ops-issue"}}},
	})
	require.NoError(t, err)

	return &harness{pool: pool, svc: svc, linear: f, api: api}
}
