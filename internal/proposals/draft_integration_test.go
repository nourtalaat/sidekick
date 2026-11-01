//go:build integration

package proposals_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/summarize"
	"github.com/nourtalaat/sidekick/strategies"
)

// draftStrategy is the built-in strategy the draft tests change.
const draftStrategy = "latency-alert"

// draftEnv is a database with the investigations of fixture/investigations.sql
// rated wrong, a proposal waiting for its draft, and a draft job on a
// scripted model.
type draftEnv struct {
	pool     *pgxpool.Pool
	conn     *modeltest.Connector
	job      *proposals.DraftJob
	proposal uuid.UUID
	current  strategy.Strategy
}

// draftOptions change a draftEnv.
type draftOptions struct {
	turns []modeltest.Turn
	daily summarize.Daily
	// off turns the summarize role off.
	off bool
	// baseVersion replaces the version the proposal is for.
	baseVersion string
}

// newDraftEnv returns the environment of a draft test.
func newDraftEnv(t *testing.T, opts draftOptions) *draftEnv {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	set, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	current, ok := set.Get(draftStrategy)
	require.True(t, ok)

	// The investigations of the fixture ran the version in effect.
	_, err = pool.Exec(t.Context(), "UPDATE investigations SET strategy_version = $1 WHERE strategy = $2", current.Version, draftStrategy)
	require.NoError(t, err)

	for i, inv := range []int{1, 2, 3} {
		insertRating(t, pool, i, rating{inv: inv, kind: "verdict", rating: "wrong"})
	}

	_, err = pool.Exec(t.Context(), "UPDATE feedback SET reason = 'It was a planned deploy.', correct_verdict = 'false_positive' WHERE investigation_id = $1", invID(1))
	require.NoError(t, err)

	// A confirmed investigation and a lesson of the strategy, which the draft shows too.
	_, err = pool.Exec(t.Context(), "UPDATE investigations SET confirmed_verdict = 'true_positive', confirmed_cause = 'A bad deploy.', confirmed_at = now() WHERE id = $1", invID(5))
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), `INSERT INTO lessons (id, text, scope_strategy, status, origin) VALUES ($1, 'Ask about planned deploys.', $2, 'active', 'ui')`, uuid.New(), draftStrategy)
	require.NoError(t, err)

	base := current.Version
	if opts.baseVersion != "" {
		base = opts.baseVersion
	}

	id := uuid.New()
	_, err = pool.Exec(t.Context(), `INSERT INTO strategy_proposals (id, name, base_version, trigger_count, status, created_at, updated_at)
		VALUES ($1, $2, $3, 3, 'drafting', now(), now())`, id, draftStrategy, base)
	require.NoError(t, err)

	conn := modeltest.New(t, model.Capabilities{ToolCalling: true, ToolChoice: true}, opts.turns...)

	var connector model.Connector = conn
	if opts.off {
		connector = nil
	}

	job := proposals.NewDraftJob(proposals.DraftOptions{
		Pool:       pool,
		Caller:     summarize.New(summarize.Options{Connector: connector, Name: "summarizer", Model: "claude-opus-5-5", Prices: pricing.New(nil), Daily: opts.daily}),
		Strategies: set,
		Window:     14 * 24 * time.Hour,
	})

	return &draftEnv{pool: pool, conn: conn, job: job, proposal: id, current: current}
}

// run queues the draft job of the proposal, claims it, and runs it.
func (e *draftEnv) run() error {
	payload, err := json.Marshal(proposals.ProposeStrategyPayload{ProposalID: e.proposal})
	if err != nil {
		return fmt.Errorf("encode the payload: %w", err)
	}

	ctx := context.Background()

	if _, err := queue.Enqueue(ctx, e.pool, queue.NewJob{Kind: queue.KindProposeStrategy, Payload: payload, UniqueKey: proposals.ProposeStrategyKey(e.proposal)}); err != nil {
		return fmt.Errorf("queue the job: %w", err)
	}

	claimed, err := db.New(e.pool).ClaimJobs(ctx, db.ClaimJobsParams{Worker: "test", LeaseSeconds: 60, Kinds: []string{queue.KindProposeStrategy}, Batch: 1})
	if err != nil || len(claimed) != 1 {
		return fmt.Errorf("claim the job: %d claimed: %w", len(claimed), err)
	}

	if err := e.job.Handle(ctx, claimed[0]); err != nil {
		return fmt.Errorf("handle: %w", err)
	}

	return nil
}

// row reads the proposal.
func (e *draftEnv) row(t *testing.T) db.StrategyProposal {
	t.Helper()

	p, err := db.New(e.pool).GetStrategyProposal(t.Context(), e.proposal)
	require.NoError(t, err)

	return p
}

// changed returns the current strategy with a sentence in its description.
func (e *draftEnv) changed() string {
	return strings.Replace(e.current.Body, "description:", "description: Ask about planned deploys.", 1)
}

// answer is a scripted turn in which the model submits body and a rationale.
func answer(t *testing.T, body, rationale string) modeltest.Turn {
	t.Helper()

	input, err := json.Marshal(map[string]string{"body": body, "rationale": rationale})
	require.NoError(t, err)

	return modeltest.ToolUse(model.Usage{InputTokens: 1000, OutputTokens: 200}, modeltest.Call("c1", proposals.SubmitStrategyTool, string(input)))
}

func TestDraftStoresAValidChangeAndQueuesTheGate(t *testing.T) {
	t.Parallel()

	// The scripted answer needs the strategy text, which a first environment
	// tells.
	probe := newDraftEnv(t, draftOptions{})
	e := newDraftEnv(t, draftOptions{turns: []modeltest.Turn{answer(t, probe.changed(), "Planned deploys were reported as incidents.")}})

	require.NoError(t, e.run())

	p := e.row(t)
	require.Equal(t, proposals.StatusPending, p.Status)
	require.Equal(t, new(e.changed()), p.Body)
	require.Equal(t, new(e.current.Body), p.BaseBody)
	require.Equal(t, new("built-in"), p.BaseOrigin)
	require.Equal(t, new("Planned deploys were reported as incidents."), p.Rationale)
	require.Len(t, p.FeedbackIds, 3)

	var kind string

	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT kind FROM jobs WHERE unique_key = $1", proposals.BenchGateKey(e.proposal)).Scan(&kind))
	require.Equal(t, queue.KindBenchGate, kind)

	require.Equal(t, 1, auditCount(t, e.pool, proposals.ActionDrafted))

	var spent int
	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT cost_micros FROM spend_daily WHERE route = 'strategy-proposal'").Scan(&spent))
	require.Positive(t, spent, "the call counts in the day's spend")

	requests := e.conn.Requests()
	require.Len(t, requests, 1)

	prompt := textOf(requests[0])
	require.Contains(t, prompt, e.current.Body, "the current strategy")
	require.Contains(t, prompt, "It was a planned deploy.", "the reason of a wrong rating")
	require.Contains(t, prompt, "A bad deploy.", "a confirmed investigation")
	require.Contains(t, prompt, "Ask about planned deploys.", "a lesson of the strategy")
	require.Equal(t, proposals.SubmitStrategyTool, requests[0].Tools[0].Name)
	require.Equal(t, 3, strings.Count(prompt, "Rated wrong:"))
}

func TestDraftAsksAgainAfterAnAnswerThatFailsTheChecks(t *testing.T) {
	t.Parallel()

	probe := newDraftEnv(t, draftOptions{})
	good := probe.changed()

	tests := []struct {
		name       string
		first      string
		wantProblm string
	}{
		{name: "a body that is not YAML", first: "name: [", wantProblm: ""},
		{name: "a body for another strategy", first: "name: other\ndescription: x\n", wantProblm: "name"},
		{name: "a body that is the current strategy", first: probe.current.Body, wantProblm: "the same as the current"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newDraftEnv(t, draftOptions{turns: []modeltest.Turn{answer(t, tt.first, "First try."), answer(t, good, "Second try.")}})

			require.NoError(t, e.run())
			require.Equal(t, proposals.StatusPending, e.row(t).Status)
			require.Equal(t, new("Second try."), e.row(t).Rationale)

			requests := e.conn.Requests()
			require.Len(t, requests, 2)
			require.Contains(t, textOf(requests[1]), "Your last answer was rejected")
			require.Contains(t, textOf(requests[1]), tt.wantProblm)
		})
	}
}

func TestDraftFailsWhenTheModelGivesNothingUsable(t *testing.T) {
	t.Parallel()

	noTool := modeltest.Text(model.Usage{InputTokens: 100, OutputTokens: 10}, "I will not.")

	tests := []struct {
		name   string
		turns  func(t *testing.T) []modeltest.Turn
		detail string
	}{
		{
			name: "two invalid bodies",
			turns: func(t *testing.T) []modeltest.Turn {
				return []modeltest.Turn{answer(t, "name: [", "x"), answer(t, "name: [", "x")}
			},
			detail: "The model gave no change that passes the checks",
		},
		{
			name:   "two answers without the tool",
			turns:  func(*testing.T) []modeltest.Turn { return []modeltest.Turn{noTool, noTool} },
			detail: "you did not call submit_strategy",
		},
		{
			name: "a rationale that is too long",
			turns: func(t *testing.T) []modeltest.Turn {
				long := strings.Repeat("x", proposals.MaxRationaleChars+1)
				return []modeltest.Turn{answer(t, "name: [", long), answer(t, "name: [", long)}
			},
			detail: "longer than",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newDraftEnv(t, draftOptions{turns: tt.turns(t)})

			require.NoError(t, e.run(), "an answer that cannot be used ends the job, with the reason on the proposal")

			p := e.row(t)
			require.Equal(t, proposals.StatusDraftFailed, p.Status)
			require.Contains(t, *p.StatusDetail, tt.detail)
			require.Zero(t, count(t, e.pool, "SELECT count(*) FROM jobs WHERE kind = $1", queue.KindBenchGate), "no gate runs")
		})
	}
}

func TestDraftFailsWithoutAModelCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		opts   draftOptions
		detail string
	}{
		{name: "no summarize role", opts: draftOptions{off: true}, detail: "summarize role"},
		{name: "the version is not available any more", opts: draftOptions{baseVersion: "0000000000000000"}, detail: "not available any more"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newDraftEnv(t, tt.opts)

			require.NoError(t, e.run())

			p := e.row(t)
			require.Equal(t, proposals.StatusDraftFailed, p.Status)
			require.Contains(t, *p.StatusDetail, tt.detail)
			require.Empty(t, e.conn.Requests())
		})
	}
}

func TestDraftIsHeldByTheDailyCap(t *testing.T) {
	t.Parallel()

	e := newDraftEnv(t, draftOptions{daily: summarize.Daily{MaxCostMicros: 1}})

	_, err := e.pool.Exec(t.Context(), "INSERT INTO spend_daily (day, route, connector, model, cost_micros) VALUES (current_date, 'payments', 'c', 'm', 5)")
	require.NoError(t, err)

	require.NoError(t, e.run())

	require.Equal(t, proposals.StatusDrafting, e.row(t).Status, "the proposal still waits for its draft")
	require.Empty(t, e.conn.Requests())

	var runAt time.Time
	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT run_at FROM jobs WHERE kind = $1 AND status = 'pending'", queue.KindProposeStrategy).Scan(&runAt))
	require.True(t, runAt.After(time.Now()), "the job moved to the next day")
}

func TestDraftRetriesAProviderFailure(t *testing.T) {
	t.Parallel()

	e := newDraftEnv(t, draftOptions{turns: []modeltest.Turn{modeltest.Fail(&model.Error{Kind: model.ErrOverloaded, Err: errors.New("overloaded")})}})

	require.Error(t, e.run(), "the job fails, so the queue tries again")
	require.Equal(t, proposals.StatusDrafting, e.row(t).Status)
}

func TestDraftLeavesProposalsThatAreNotWaiting(t *testing.T) {
	t.Parallel()

	e := newDraftEnv(t, draftOptions{})

	_, err := e.pool.Exec(t.Context(), `UPDATE strategy_proposals SET status = 'draft_failed', status_detail = 'x'`)
	require.NoError(t, err)

	require.NoError(t, e.run())
	require.Empty(t, e.conn.Requests())
	require.Equal(t, proposals.StatusDraftFailed, e.row(t).Status)
}

func TestDraftOnDeadEndsTheProposal(t *testing.T) {
	t.Parallel()

	e := newDraftEnv(t, draftOptions{})

	payload, err := json.Marshal(proposals.ProposeStrategyPayload{ProposalID: e.proposal})
	require.NoError(t, err)

	job := db.Job{ID: 1, Payload: payload, LastError: new("the model is down")}

	require.NoError(t, e.job.OnDead(t.Context(), job))

	p := e.row(t)
	require.Equal(t, proposals.StatusDraftFailed, p.Status)
	require.Contains(t, *p.StatusDetail, "the model is down")

	// Called again, as the queue may, it changes nothing.
	require.NoError(t, e.job.OnDead(t.Context(), job))
	require.Equal(t, 1, auditCount(t, e.pool, proposals.ActionDraftFailed))
}

// textOf returns the text of every message of a request.
func textOf(req model.Request) string {
	var b strings.Builder

	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if text, ok := p.(model.Text); ok {
				b.WriteString(text.Text)
			}
		}
	}

	return b.String()
}

// count returns the number of rows a query counts.
func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), query, args...).Scan(&n))

	return n
}
