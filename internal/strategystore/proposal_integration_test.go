//go:build integration

package strategystore_test

import (
	"cmp"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/strategystore"
)

// proposalBody is the YAML a proposal holds and the tests save.
const proposalBody = "name: checkout-latency\ndescription: Checkout is slow, and a proposal says what to check.\n"

// proposalRow is a proposal as the tests insert it.
type proposalRow struct {
	name       string
	body       string
	status     string
	gateResult *string
	// withRuns gives the proposal a benchmark run and a baseline run.
	withRuns bool
}

// insertProposal inserts a proposal and returns its ID and the ID of the
// benchmark run of its gate.
func insertProposal(t *testing.T, pool *pgxpool.Pool, p proposalRow) (proposal, benchRun uuid.UUID) {
	t.Helper()

	proposal, benchRun = uuid.New(), uuid.New()
	baseline := uuid.New()

	for _, id := range []uuid.UUID{benchRun, baseline} {
		_, err := pool.Exec(t.Context(), `INSERT INTO bench_runs (id, started_at, finished_at, sidekick_version, git_sha, config_hash,
			connectors, strategies, scenario_set, repeats, summary, status, triggered_by)
			VALUES ($1, now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate', 5, '{}', 'passed', 'gate')`, id)
		require.NoError(t, err)
	}

	var bench, base *uuid.UUID
	if p.withRuns {
		bench, base = &benchRun, &baseline
	}

	_, err := pool.Exec(t.Context(), `INSERT INTO strategy_proposals
		(id, name, base_version, base_origin, base_body, body, rationale, trigger_count, status, gate_result, bench_run_id, baseline_run_id, created_at, updated_at)
		VALUES ($1, $2, 'runtime:2', 'runtime', 'name: x', $3, 'Because', 3, $4, $5, $6, $7, now(), now())`,
		proposal, p.name, p.body, p.status, p.gateResult, bench, base)
	require.NoError(t, err)

	return proposal, benchRun
}

func TestSaveFromAProposal(t *testing.T) {
	t.Parallel()

	passed := new("passed")
	failed := new("failed")

	tests := []struct {
		name string
		row  proposalRow
		// input changes what the test saves; the zero value saves the
		// proposal's body as strategy checkout-latency with its run.
		body       string
		strategy   string
		otherRun   bool
		noProposal bool
		wantErr    error
	}{
		{name: "a passed proposal", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "passed", gateResult: passed, withRuns: true}},
		{name: "a pending proposal", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "pending"}, wantErr: strategystore.ErrProposalNotPassed},
		{name: "a proposal still being benchmarked", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "benchmarking"}, wantErr: strategystore.ErrProposalNotPassed},
		{name: "a failed proposal", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "failed", gateResult: failed, withRuns: true}, wantErr: strategystore.ErrProposalNotPassed},
		{name: "a rejected proposal", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "rejected", gateResult: passed, withRuns: true}, wantErr: strategystore.ErrProposalNotPassed},
		{name: "a proposal that was already applied", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "applied", gateResult: passed, withRuns: true}, wantErr: strategystore.ErrProposalNotPassed},
		{name: "a proposal that is no proposal", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "passed", gateResult: passed, withRuns: true}, noProposal: true, wantErr: strategystore.ErrProposalNotPassed},
		{
			name: "a body the proposal does not hold", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "passed", gateResult: passed, withRuns: true},
			body: "name: checkout-latency\ndescription: Edited after the gate.\n", wantErr: strategystore.ErrProposalNotPassed,
		},
		{
			name: "another strategy than the proposal's", row: proposalRow{name: "disk-full", body: proposalBody, status: "passed", gateResult: passed, withRuns: true},
			wantErr: strategystore.ErrProposalNotPassed,
		},
		{
			name: "a benchmark run other than the gate's", row: proposalRow{name: "checkout-latency", body: proposalBody, status: "passed", gateResult: passed, withRuns: true},
			otherRun: true, wantErr: strategystore.ErrProposalNotPassed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			proposal, run := insertProposal(t, pool, tt.row)

			if tt.noProposal {
				proposal = uuid.New()
			}

			if tt.otherRun {
				run = uuid.New()
			}

			body := cmp.Or(tt.body, proposalBody)

			saved, err := store.Save(t.Context(), strategystore.SaveInput{
				Actor: sam, Name: "checkout-latency", Body: body, Reason: "From a proposal", ProposalID: &proposal, BenchRunID: &run,
			})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, 2, versionCount(t, pool, "checkout-latency"), "no version is saved")

				return
			}

			require.NoError(t, err)
			require.Equal(t, 3, saved.Version.Number)

			got, err := store.Version(t.Context(), "checkout-latency", 3)
			require.NoError(t, err)
			require.Equal(t, &proposal, got.ProposalID)
			require.Equal(t, &run, got.BenchRunID)
		})
	}
}

func TestSaveWithoutAProposalIsUnchanged(t *testing.T) {
	t.Parallel()

	pool, store := setup(t)

	saved, err := store.Save(t.Context(), strategystore.SaveInput{Actor: sam, Name: "checkout-latency", Body: proposalBody, Reason: "By hand"})

	require.NoError(t, err)
	require.Equal(t, 3, saved.Version.Number)
	require.Nil(t, saved.Version.ProposalID)
	require.Equal(t, 3, versionCount(t, pool, "checkout-latency"))
}

func TestSaveInCommitsAndRollsBackWithTheCaller(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		commit   bool
		wantRows int
	}{
		{name: "committed with the caller", commit: true, wantRows: 3},
		{name: "rolled back with the caller", commit: false, wantRows: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			saved, err := store.SaveIn(t.Context(), tx, strategystore.SaveInput{Actor: sam, Name: "checkout-latency", Body: proposalBody, Reason: "In a transaction"})
			require.NoError(t, err)
			require.Equal(t, 3, saved.Version.Number)

			if tt.commit {
				require.NoError(t, tx.Commit(t.Context()))
			} else {
				require.NoError(t, tx.Rollback(t.Context()))
			}

			require.Equal(t, tt.wantRows, versionCount(t, pool, "checkout-latency"))
		})
	}
}

func TestSaveInAfterAProposalDecisionInTheSameTransaction(t *testing.T) {
	t.Parallel()

	pool, store := setup(t)
	proposal, run := insertProposal(t, pool, proposalRow{name: "checkout-latency", body: proposalBody, status: "passed", gateResult: new("passed"), withRuns: true})

	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := store.SaveIn(t.Context(), tx, strategystore.SaveInput{
			Actor: sam, Name: "checkout-latency", Body: proposalBody, Reason: "From a proposal", ProposalID: &proposal, BenchRunID: &run,
		})
		if err != nil {
			return fmt.Errorf("save the version: %w", err)
		}

		if _, err := tx.Exec(t.Context(), "UPDATE strategy_proposals SET status = 'applied' WHERE id = $1", proposal); err != nil {
			return fmt.Errorf("decide on the proposal: %w", err)
		}

		return nil
	})
	require.NoError(t, err)

	// The decision has been made, so the same proposal cannot make a
	// second version.
	_, err = store.Save(t.Context(), strategystore.SaveInput{
		Actor: sam, Name: "checkout-latency", Body: proposalBody + "first:\n  - Look again.\n", Reason: "Again", ProposalID: &proposal, BenchRunID: &run,
	})
	require.ErrorIs(t, err, strategystore.ErrProposalNotPassed)
}

// versionCount returns how many versions the strategy has.
func versionCount(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM strategy_versions WHERE name = $1", name).Scan(&n))

	return n
}
