//go:build integration

package daily

import (
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// checkoutInvestigation is the investigation of fixture/investigations.sql.
var checkoutInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a database with the checkout investigation.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	return pool
}

// proposal is a row of action_proposals the tests insert.
type proposal struct {
	action     string
	status     string
	decidedAt  *time.Time
	executedAt *time.Time
}

// insert stores the proposal and returns its ID.
func (p proposal) insert(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()

	id := ids.New()
	proposedAt := time.Now().Add(-time.Hour)

	_, err := pool.Exec(t.Context(), `
		INSERT INTO action_proposals (id, investigation_id, action, params, reason, mode, status, idempotency_key,
		                              proposed_at, expires_at, decided_at, executed_at)
		VALUES ($1, $2, $3, '{}', '', 'approved', $4, $5, $6, $7, $8, $9)`,
		id, checkoutInvestigation, p.action, p.status, "sidekick/"+id.String(), proposedAt, proposedAt.Add(48*time.Hour), p.decidedAt, p.executedAt)
	require.NoError(t, err)

	return id
}

// at returns a pointer to t, for the optional times of a proposal.
func at(t time.Time) *time.Time {
	return &t
}
