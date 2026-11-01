//go:build integration

package gate_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/gate"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/scenario"
)

// reviewerID is the user who reviewed the scenarios the tests keep in the
// database.
const reviewerID = "0198f2a4-0000-7000-8000-0000000000e1"

// insertScenario keeps sc in the scenarios table with the status, as the
// export does: a reviewed one has a reviewer.
func insertScenario(t *testing.T, pool *pgxpool.Pool, sc bench.Scenario, status string) {
	t.Helper()

	body, err := yaml.Marshal(sc)
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), `INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
		VALUES ($1, 'https://idp.example.com', 'reviewer', 'Rev', 'admin', now(), now()) ON CONFLICT DO NOTHING`, reviewerID)
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), `INSERT INTO scenarios (id, title, status, body, provenance, notes, reviewed_by, reviewed_at, created_at, updated_at)
		VALUES ($1, $1, $2, $3, '[]', '[]', CASE WHEN $2 = 'reviewed' THEN $4::uuid END, CASE WHEN $2 = 'reviewed' THEN now() END, now(), now())`,
		sc.ID, status, string(body), reviewerID)
	require.NoError(t, err)
}

// TestGateChoosesReviewedScenarios checks that the gate takes the reviewed
// scenarios of the database that cover the strategy, and never a draft.
func TestGateChoosesReviewedScenarios(t *testing.T) {
	t.Parallel()

	e := newEnv(t, envOptions{
		scenarios: []bench.Scenario{},
		source: func(pool *pgxpool.Pool) gate.ScenarioSource {
			return scenario.NewReviewedSource(pool, repoRoot)
		},
	})

	// The covering scenarios of the other tests, kept as the export keeps
	// them: relative snapshot paths, found again through the base directory.
	reviewed := coveringScenarios(t, testScenarios)
	draft := coveringScenarios(t, 1)[0]
	draft.ID = "draft-scenario"

	loaded, err := bench.LoadScenarios(repoRoot+"/bench/scenarios/sample/checkout-pool-size.yaml", bench.LoadOptions{})
	require.NoError(t, err)

	for i := range reviewed {
		reviewed[i].Sources = loaded[0].Sources
		insertScenario(t, e.pool, reviewed[i], "reviewed")
	}

	draft.Sources = loaded[0].Sources
	insertScenario(t, e.pool, draft, "draft")

	id := insertProposal(t, e.pool, betterBody)
	require.NoError(t, e.gate.Handle(t.Context(), jobFor(t, id)))

	p := loadProposal(t, e.pool, id)
	d := detailOf(t, p)

	require.Equal(t, bench.GatePassed, d.Result, "reason: %s, error: %s", d.Reason, d.Error)
	require.Equal(t, proposals.StatusPassed, p.Status)
	require.Len(t, d.Scenarios, testScenarios, "the reviewed scenarios, not the draft")
}
