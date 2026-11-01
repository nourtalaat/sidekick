//go:build integration

package proposals_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// checkViolation is the Postgres error code of a CHECK constraint.
const checkViolation = "23514"

// uniqueViolation is the Postgres error code of a duplicate key.
const uniqueViolation = "23505"

// TestTableRefusesAnAppliedProposalThatDidNotPass tries to put a proposal
// into every state that needs a passed gate without one, straight in SQL.
func TestTableRefusesAnAppliedProposalThatDidNotPass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  string
	}{
		{name: "approved without a gate result", set: "status = 'approved'"},
		{name: "applied without a gate result", set: "status = 'applied'"},
		{name: "applied after a failed gate", set: "status = 'applied', gate_result = 'failed'"},
		{name: "applied after too little evidence", set: "status = 'applied', gate_result = 'not_enough_evidence'"},
		{name: "applied without a benchmark", set: "status = 'applied', gate_result = 'no_benchmark_coverage'"},
		{name: "applied over budget", set: "status = 'applied', gate_result = 'over_budget'"},
		{name: "applied after an error", set: "status = 'applied', gate_result = 'error'"},
		{name: "passed without a gate result", set: "status = 'passed'"},
		{name: "failed with a passed gate result", set: "status = 'failed', gate_result = 'passed'"},
		{name: "applied with a passed gate but no runs", set: "status = 'applied', gate_result = 'passed'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			id := insertPending(t, pool, "latency-alert", "v1")

			_, err := pool.Exec(t.Context(), "UPDATE strategy_proposals SET "+tt.set+" WHERE id = $1", id)

			require.Equal(t, checkViolation, codeOf(t, err))
		})
	}
}

// TestTableHoldsOneProposalPerVersion checks that a second proposal for the
// same strategy version is refused, and one for another version is not.
func TestTableHoldsOneProposalPerVersion(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	insertPending(t, pool, "latency-alert", "v1")
	insertPending(t, pool, "latency-alert", "v2")
	insertPending(t, pool, "service-down", "v1")

	_, err := pool.Exec(t.Context(), `INSERT INTO strategy_proposals (id, name, base_version, trigger_count, status, created_at, updated_at)
		VALUES ($1, 'latency-alert', 'v1', 3, 'drafting', now(), now())`, uuid.New())

	require.Equal(t, uniqueViolation, codeOf(t, err))
}

// codeOf returns the Postgres error code of err.
func codeOf(t *testing.T, err error) string {
	t.Helper()

	var pgErr *pgconn.PgError

	require.ErrorAs(t, err, &pgErr)

	return pgErr.Code
}

// insertPending adds a drafted proposal in the pending state and returns
// its ID.
func insertPending(t *testing.T, pool *pgxpool.Pool, name, version string) uuid.UUID {
	t.Helper()

	id := uuid.New()

	_, err := pool.Exec(t.Context(), `INSERT INTO strategy_proposals
		(id, name, base_version, base_origin, base_body, body, rationale, trigger_count, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'runtime', 'name: x', 'name: y', 'Because', 3, 'pending', now(), now())`, id, name, version)
	require.NoError(t, err)

	return id
}
