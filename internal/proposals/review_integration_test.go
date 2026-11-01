//go:build integration

package proposals_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

const (
	// currentBody is version 2 of the runtime strategy checkout-latency.
	currentBody = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n"
	// proposedBody is what the proposals change it to.
	proposedBody = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n  - Check the connection pool.\n"
)

// reviewer is the admin who reviews in the tests.
var reviewer = strategystore.Actor{Kind: audit.ActorUser, ID: "01920000-0000-7000-8000-000000000071"}

// spec describes a proposal the tests insert.
type spec struct {
	name       string
	baseOrigin string
	baseVer    string
	body       string
	status     string
	gateResult string
	// runs gives it a benchmark run and a baseline run.
	runs bool
}

// passedRuntime is a passed proposal for version 2 of checkout-latency.
func passedRuntime() spec {
	return spec{name: "checkout-latency", baseOrigin: "runtime", baseVer: "runtime:2", body: proposedBody, status: "passed", gateResult: "passed", runs: true}
}

// reviewEnv is a database with checkout-latency saved twice, and a service.
type reviewEnv struct {
	pool    *pgxpool.Pool
	store   *strategystore.Store
	service *proposals.Service
}

// newReviewEnv returns the environment of a review test.
func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()

	pool := storetest.New(t)
	store := strategystore.NewStore(pool)

	for i, body := range []string{"name: checkout-latency\ndescription: Checkout is slow.\n", currentBody} {
		_, err := store.Save(t.Context(), strategystore.SaveInput{Actor: reviewer, Name: "checkout-latency", Body: body, Reason: "Version " + string(rune('1'+i))})
		require.NoError(t, err)
	}

	return &reviewEnv{pool: pool, store: store, service: proposals.NewService(pool, store)}
}

// insert adds the proposal and returns its ID and its benchmark run's.
func (e *reviewEnv) insert(t *testing.T, s spec) (id, benchRun uuid.UUID) {
	t.Helper()

	id, benchRun = uuid.New(), uuid.New()
	baseline := uuid.New()

	var bench, base *uuid.UUID

	if s.runs {
		for _, run := range []uuid.UUID{benchRun, baseline} {
			_, err := e.pool.Exec(t.Context(), `INSERT INTO bench_runs (id, started_at, finished_at, sidekick_version, git_sha, config_hash,
				connectors, strategies, scenario_set, repeats, summary, status, triggered_by)
				VALUES ($1, now(), now(), 'dev', 'abc', 'hash', '{}', '{}', 'gate', 5, '{}', 'passed', 'gate')`, run)
			require.NoError(t, err)
		}

		bench, base = &benchRun, &baseline
	}

	var result *string
	if s.gateResult != "" {
		result = &s.gateResult
	}

	_, err := e.pool.Exec(t.Context(), `INSERT INTO strategy_proposals
		(id, name, base_version, base_origin, base_body, body, rationale, trigger_count, status, gate_result, bench_run_id, baseline_run_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'name: x', $5, 'The pool was not checked.', 3, $6, $7, $8, $9, now(), now())`,
		id, s.name, s.baseVer, s.baseOrigin, s.body, s.status, result, bench, base)
	require.NoError(t, err)

	return id, benchRun
}

// proposal reads a proposal.
func (e *reviewEnv) proposal(t *testing.T, id uuid.UUID) db.StrategyProposal {
	t.Helper()

	p, err := db.New(e.pool).GetStrategyProposal(t.Context(), id)
	require.NoError(t, err)

	return p
}

func TestApprove(t *testing.T) {
	t.Parallel()

	t.Run("a runtime strategy gets the next version", func(t *testing.T) {
		t.Parallel()

		e := newReviewEnv(t)
		id, run := e.insert(t, passedRuntime())

		got, err := e.service.Approve(t.Context(), id, reviewer)
		require.NoError(t, err)

		require.NotNil(t, got.Version)
		require.Equal(t, 3, got.Version.Number)
		require.Empty(t, got.YAML)
		require.Equal(t, proposals.StatusApplied, got.Proposal.Status)

		v, err := e.store.Version(t.Context(), "checkout-latency", 3)
		require.NoError(t, err)
		require.Equal(t, proposedBody, v.Body)
		require.Equal(t, &id, v.ProposalID)
		require.Equal(t, &run, v.BenchRunID)
		require.Contains(t, v.Reason, id.String())
		require.Contains(t, v.Reason, "The pool was not checked.")

		p := e.proposal(t, id)
		require.Equal(t, proposals.StatusApplied, p.Status)
		require.NotNil(t, p.DecidedAt)
		require.Equal(t, new(uuid.MustParse(reviewer.ID)), p.Reviewer)

		require.Equal(t, 1, auditRows(t, e.pool, proposals.ActionApproved))
		require.Equal(t, 1, auditRows(t, e.pool, strategystore.ActionUpdated))
	})

	t.Run("a built-in strategy gets a runtime strategy that takes its place", func(t *testing.T) {
		t.Parallel()

		e := newReviewEnv(t)
		s := passedRuntime()
		s.name, s.baseOrigin, s.baseVer = "generic-alert", "built-in", "0123456789abcdef"
		s.body = "name: generic-alert\ndescription: A better generic strategy.\n"
		id, _ := e.insert(t, s)

		got, err := e.service.Approve(t.Context(), id, reviewer)
		require.NoError(t, err)
		require.Equal(t, 1, got.Version.Number)
		require.Equal(t, proposals.StatusApplied, e.proposal(t, id).Status)
		require.Equal(t, 1, auditRows(t, e.pool, strategystore.ActionCreated))
	})

	t.Run("a file strategy is not changed, and its YAML is returned", func(t *testing.T) {
		t.Parallel()

		e := newReviewEnv(t)
		s := passedRuntime()
		s.name, s.baseOrigin, s.baseVer = "payments-latency", "file", "0123456789abcdef"
		s.body = "name: payments-latency\ndescription: Payments are slow.\n"
		id, _ := e.insert(t, s)

		got, err := e.service.Approve(t.Context(), id, reviewer)
		require.NoError(t, err)
		require.Nil(t, got.Version)
		require.Equal(t, s.body, got.YAML)
		require.Equal(t, proposals.StatusApproved, got.Proposal.Status)
		require.Equal(t, proposals.StatusApproved, e.proposal(t, id).Status)

		var strategies int
		require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT count(*) FROM strategies WHERE name = 'payments-latency'").Scan(&strategies))
		require.Zero(t, strategies, "nothing is saved for a strategy in a file")
	})

	t.Run("a proposal that is not passed is refused, whatever it is", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			status     string
			gateResult string
			runs       bool
		}{
			{name: "drafting", status: "drafting"},
			{name: "draft_failed", status: "draft_failed"},
			{name: "pending", status: "pending"},
			{name: "benchmarking", status: "benchmarking"},
			{name: "failed", status: "failed", gateResult: "failed", runs: true},
			{name: "not enough evidence", status: "failed", gateResult: "not_enough_evidence", runs: true},
			{name: "no benchmark coverage", status: "failed", gateResult: "no_benchmark_coverage"},
			{name: "over budget", status: "failed", gateResult: "over_budget"},
			{name: "gate error", status: "failed", gateResult: "error"},
			{name: "rejected after a pass", status: "rejected", gateResult: "passed", runs: true},
			{name: "applied already", status: "applied", gateResult: "passed", runs: true},
			{name: "approved already", status: "approved", gateResult: "passed", runs: true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				e := newReviewEnv(t)
				s := passedRuntime()
				s.status, s.gateResult, s.runs = tt.status, tt.gateResult, tt.runs
				id, _ := e.insert(t, s)

				_, err := e.service.Approve(t.Context(), id, reviewer)
				require.ErrorIs(t, err, proposals.ErrNotPassed)
				require.Equal(t, 2, versions(t, e.pool, "checkout-latency"), "no version is saved")
				require.Equal(t, tt.status, e.proposal(t, id).Status, "the proposal is as it was")
				require.Zero(t, auditRows(t, e.pool, proposals.ActionApproved))
			})
		}
	})

	t.Run("a second approval is refused", func(t *testing.T) {
		t.Parallel()

		e := newReviewEnv(t)
		id, _ := e.insert(t, passedRuntime())

		_, err := e.service.Approve(t.Context(), id, reviewer)
		require.NoError(t, err)

		_, err = e.service.Approve(t.Context(), id, reviewer)
		require.ErrorIs(t, err, proposals.ErrNotPassed)
		require.Equal(t, 3, versions(t, e.pool, "checkout-latency"))
	})

	t.Run("a strategy saved since the draft makes the approval stale", func(t *testing.T) {
		t.Parallel()

		e := newReviewEnv(t)
		id, _ := e.insert(t, passedRuntime())

		_, err := e.store.Save(t.Context(), strategystore.SaveInput{
			Actor: reviewer, Name: "checkout-latency", Body: currentBody + "  - Check the deploys.\n", Reason: "By hand",
		})
		require.NoError(t, err)

		_, err = e.service.Approve(t.Context(), id, reviewer)
		require.ErrorIs(t, err, strategystore.ErrStale)
		require.Equal(t, proposals.StatusPassed, e.proposal(t, id).Status, "the proposal stays passed, to reject or edit")
		require.Equal(t, 3, versions(t, e.pool, "checkout-latency"))
	})

	t.Run("a strategy that already holds the body is unchanged", func(t *testing.T) {
		t.Parallel()

		e := newReviewEnv(t)
		s := passedRuntime()
		s.body = currentBody
		id, _ := e.insert(t, s)

		_, err := e.service.Approve(t.Context(), id, reviewer)
		require.ErrorIs(t, err, strategystore.ErrUnchanged)
		require.Equal(t, proposals.StatusPassed, e.proposal(t, id).Status)
	})

	t.Run("a proposal that does not exist", func(t *testing.T) {
		t.Parallel()

		e := newReviewEnv(t)

		_, err := e.service.Approve(t.Context(), uuid.New(), reviewer)
		require.ErrorIs(t, err, proposals.ErrNotFound)
	})
}

func TestReject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     string
		gateResult string
		runs       bool
		wantErr    error
	}{
		{name: "pending", status: "pending"},
		{name: "benchmarking", status: "benchmarking"},
		{name: "passed", status: "passed", gateResult: "passed", runs: true},
		{name: "failed", status: "failed", gateResult: "failed", runs: true},
		{name: "drafting has no draft to reject", status: "drafting", wantErr: proposals.ErrNotRejectable},
		{name: "draft_failed has no draft to reject", status: "draft_failed", wantErr: proposals.ErrNotRejectable},
		{name: "rejected already", status: "rejected", wantErr: proposals.ErrNotRejectable},
		{name: "applied already", status: "applied", gateResult: "passed", runs: true, wantErr: proposals.ErrNotRejectable},
		{name: "approved already", status: "approved", gateResult: "passed", runs: true, wantErr: proposals.ErrNotRejectable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newReviewEnv(t)
			s := passedRuntime()
			s.status, s.gateResult, s.runs = tt.status, tt.gateResult, tt.runs
			id, _ := e.insert(t, s)

			_, err := e.pool.Exec(t.Context(), `INSERT INTO strategy_gate_runs (proposal_id, arm, scenario_id, repeat, result, cost_micros, created_at)
				VALUES ($1, 'baseline', 's', 1, '{}', 5, now())`, id)
			require.NoError(t, err)

			got, err := e.service.Reject(t.Context(), id, reviewer, " Not now ")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, tt.status, e.proposal(t, id).Status)

				return
			}

			require.NoError(t, err)
			require.Equal(t, proposals.StatusRejected, got.Status)
			require.NotNil(t, got.DecidedAt)
			require.Equal(t, new(uuid.MustParse(reviewer.ID)), got.Reviewer)
			require.Zero(t, gateRuns(t, e.pool, id), "the kept runs are deleted")
			require.Equal(t, 1, auditRows(t, e.pool, proposals.ActionRejected))
			require.Equal(t, 2, versions(t, e.pool, "checkout-latency"))
		})
	}
}

func TestRejectRefusesALongReason(t *testing.T) {
	t.Parallel()

	e := newReviewEnv(t)
	id, _ := e.insert(t, passedRuntime())

	_, err := e.service.Reject(t.Context(), id, reviewer, strings.Repeat("x", proposals.MaxReasonChars+1))

	var invalid *strategystore.InvalidError

	require.ErrorAs(t, err, &invalid)
	require.Equal(t, proposals.StatusPassed, e.proposal(t, id).Status)
}

func TestEdit(t *testing.T) {
	t.Parallel()

	const edited = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check the deploys and the connection pool.\n"

	tests := []struct {
		name       string
		status     string
		gateResult string
		runs       bool
	}{
		{name: "a passed proposal", status: "passed", gateResult: "passed", runs: true},
		{name: "a failed proposal", status: "failed", gateResult: "failed", runs: true},
		{name: "a proposal whose gate had too little evidence", status: "failed", gateResult: "not_enough_evidence", runs: true},
		{name: "a proposal whose gate failed to run", status: "failed", gateResult: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newReviewEnv(t)
			s := passedRuntime()
			s.status, s.gateResult, s.runs = tt.status, tt.gateResult, tt.runs
			id, _ := e.insert(t, s)

			_, err := e.pool.Exec(t.Context(), `INSERT INTO strategy_gate_runs (proposal_id, arm, scenario_id, repeat, result, cost_micros, created_at)
				VALUES ($1, 'baseline', 's', 1, '{}', 5, now())`, id)
			require.NoError(t, err)

			got, err := e.service.Edit(t.Context(), id, reviewer, edited)
			require.NoError(t, err)

			require.Equal(t, proposals.StatusPending, got.Status)
			require.Equal(t, new(edited), got.Body)
			require.Nil(t, got.GateResult)
			require.Nil(t, got.GateDetail)
			require.Nil(t, got.GateSetHash)
			require.Nil(t, got.BenchRunID, "the old result is not kept with a body it does not match")
			require.Nil(t, got.BaselineRunID)
			require.Equal(t, new(uuid.MustParse(reviewer.ID)), got.Reviewer)
			require.Zero(t, gateRuns(t, e.pool, id), "the kept runs are deleted")
			require.Equal(t, 1, auditRows(t, e.pool, proposals.ActionEdited))

			var kind string

			var key *string

			require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT kind, unique_key FROM jobs").Scan(&kind, &key))
			require.Equal(t, queue.KindBenchGate, kind, "the proposal goes through the gate again")
			require.Equal(t, new(proposals.BenchGateKey(id)), key)

			// An edited proposal that nobody has re-checked cannot be applied.
			_, err = e.service.Approve(t.Context(), id, reviewer)
			require.ErrorIs(t, err, proposals.ErrNotPassed)
		})
	}
}

func TestEditRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      string
		gateResult  string
		runs        bool
		body        string
		wantErr     error
		wantInvalid bool
	}{
		{name: "a proposal that is still drafting", status: "drafting", body: proposedBody, wantErr: proposals.ErrNotEditable},
		{name: "a proposal whose gate is running", status: "benchmarking", body: proposedBody, wantErr: proposals.ErrNotEditable},
		{name: "a rejected proposal", status: "rejected", gateResult: "passed", runs: true, body: proposedBody, wantErr: proposals.ErrNotEditable},
		{name: "an applied proposal", status: "applied", gateResult: "passed", runs: true, body: proposedBody, wantErr: proposals.ErrNotEditable},
		{name: "a body that is not YAML", status: "passed", gateResult: "passed", runs: true, body: "name: [", wantInvalid: true},
		{name: "a body with an unknown key", status: "passed", gateResult: "passed", runs: true, body: proposedBody + "nope: 1\n", wantInvalid: true},
		{name: "a body for another strategy", status: "passed", gateResult: "passed", runs: true, body: "name: disk-full\ndescription: Disk.\n", wantInvalid: true},
		{name: "a body that is too large", status: "passed", gateResult: "passed", runs: true, body: proposedBody + "# " + strings.Repeat("x", proposals.MaxBodyBytes) + "\n", wantInvalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newReviewEnv(t)
			s := passedRuntime()
			s.status, s.gateResult, s.runs = tt.status, tt.gateResult, tt.runs
			id, _ := e.insert(t, s)

			_, err := e.service.Edit(t.Context(), id, reviewer, tt.body)

			if tt.wantInvalid {
				var invalid *strategystore.InvalidError

				require.ErrorAs(t, err, &invalid)
				require.NotEmpty(t, invalid.Body)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}

			p := e.proposal(t, id)
			require.Equal(t, tt.status, p.Status, "the proposal is as it was")
			require.Equal(t, new(proposedBody), p.Body)
			require.Zero(t, auditRows(t, e.pool, proposals.ActionEdited))
		})
	}
}

func TestEditAcceptsTheSameBodyToRunTheGateAgain(t *testing.T) {
	t.Parallel()

	e := newReviewEnv(t)
	s := passedRuntime()
	s.status, s.gateResult, s.runs = "failed", "error", false
	id, _ := e.insert(t, s)

	got, err := e.service.Edit(t.Context(), id, reviewer, proposedBody)
	require.NoError(t, err)
	require.Equal(t, proposals.StatusPending, got.Status)
}

func TestCheckBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "a valid strategy", body: proposedBody},
		{name: "another name", body: "name: other\ndescription: x\n", wantErr: true},
		{name: "empty", body: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := proposals.CheckBody(tt.body, "checkout-latency")
			if tt.wantErr {
				var invalid *strategystore.InvalidError

				require.ErrorAs(t, err, &invalid)

				return
			}

			require.NoError(t, err)
		})
	}
}

// versions returns how many versions the strategy has.
func versions(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM strategy_versions WHERE name = $1", name).Scan(&n))

	return n
}

// gateRuns returns how many finished runs a proposal's gate has kept.
func gateRuns(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM strategy_gate_runs WHERE proposal_id = $1", id).Scan(&n))

	return n
}

// auditRows returns how many audit entries have the action.
func auditRows(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()

	return auditCount(t, pool, action)
}
