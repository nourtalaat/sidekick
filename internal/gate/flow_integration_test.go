//go:build integration

package gate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/strategystore"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

// flowTimeout is how long the flow tests wait for the jobs to finish.
const flowTimeout = 3 * time.Minute

// flowAdmin is the admin who approves in the flow tests.
var flowAdmin = strategystore.Actor{Kind: "user", ID: "01920000-0000-7000-8000-000000000071"}

// invID returns the ID of the fixture investigation n.
func invID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("01920000-0000-7000-8000-00000000010%d", n))
}

// TestTheWholeFlow follows a strategy from wrong feedback to an applied change
// with the real queue: three wrong ratings add a proposal, the summarize model
// drafts a change, the gate runs both versions on the scenarios, and an admin
// approves. Only the model is scripted.
func TestTheWholeFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus string
		wantResult string
	}{
		{name: "a change that scores like the current version passes and is applied", body: betterBody, wantStatus: proposals.StatusPassed, wantResult: "passed"},
		{name: "a change that scores much lower fails and cannot be applied", body: worseBody, wantStatus: proposals.StatusFailed, wantResult: "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, envOptions{})
			storetest.LoadFixture(t, e.pool, "fixture/flow.sql")

			// The strategy is saved in the database too, as an admin would have saved it.
			_, err := strategystore.NewStore(e.pool).Save(t.Context(), strategystore.SaveInput{Actor: flowAdmin, Name: strategyName, Body: baselineBody, Reason: "Version 1"})
			require.NoError(t, err)

			summarizer := modeltest.New(t, model.Capabilities{ToolCalling: true, ToolChoice: true}, flowAnswer(t, tt.body))
			summarizer.ExpectAllUsed()

			draft := proposals.NewDraftJob(proposals.DraftOptions{
				Pool:       e.pool,
				Caller:     summarize.New(summarize.Options{Connector: summarizer, Name: "summarizer", Model: "claude-opus-5-5", Prices: pricing.New(nil)}),
				Strategies: e.set,
				Window:     14 * 24 * time.Hour,
			})

			runner, err := queue.NewRunner(e.pool, queue.Options{WorkerID: "flow", PollInterval: 100 * time.Millisecond})
			require.NoError(t, err)
			require.NoError(t, runner.Register(queue.KindProposeStrategy, draft, queue.KindOptions{Long: true, OnDead: draft.OnDead}))
			require.NoError(t, runner.Register(queue.KindBenchGate, e.gate, queue.KindOptions{Long: true, Concurrency: 1, OnDead: e.gate.OnDead}))

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)

			go func() { done <- runner.Run(ctx) }()

			t.Cleanup(func() {
				cancel()
				require.NoError(t, <-done)
			})

			// Three people rate the verdict of three investigations wrong.
			trigger := proposals.Trigger{Enabled: true, Threshold: 3, Window: 14 * 24 * time.Hour}

			for n := 1; n <= 3; n++ {
				err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
					_, err := feedback.RateVerdict(t.Context(), tx, feedback.VerdictRating{
						InvestigationID: invID(n), Rating: output.RatingWrong, SlackUserID: fmt.Sprintf("U0%d", n), Proposals: trigger,
					})
					if err != nil {
						return fmt.Errorf("rate: %w", err)
					}

					return nil
				})
				require.NoError(t, err)
			}

			var id uuid.UUID

			require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT id FROM strategy_proposals").Scan(&id), "the third rating added the proposal")

			require.Eventually(t, func() bool {
				return loadProposal(t, e.pool, id).Status == tt.wantStatus
			}, flowTimeout, 500*time.Millisecond, "the proposal reaches its gate result")

			p := loadProposal(t, e.pool, id)
			require.Equal(t, strategyName, p.Name)
			require.Equal(t, "runtime:1", p.BaseVersion)
			require.Equal(t, new(tt.body), p.Body)
			require.Equal(t, new(tt.wantResult), p.GateResult)
			require.Len(t, p.FeedbackIds, 3)
			require.NotNil(t, p.BenchRunID)
			require.NotNil(t, p.BaselineRunID)
			require.Equal(t, 2, count(t, e.pool, "SELECT count(*) FROM bench_runs WHERE triggered_by = 'gate'"))

			service := proposals.NewService(e.pool, strategystore.NewStore(e.pool))

			if tt.wantStatus != proposals.StatusPassed {
				_, err := service.Approve(t.Context(), id, flowAdmin)
				require.ErrorIs(t, err, proposals.ErrNotPassed, "a proposal that did not pass cannot be applied")
				require.Equal(t, 1, count(t, e.pool, "SELECT count(*) FROM strategy_versions WHERE name = $1", strategyName), "no version was saved besides the first")

				return
			}

			got, err := service.Approve(t.Context(), id, flowAdmin)
			require.NoError(t, err)
			require.Equal(t, 2, got.Version.Number, "the next version")

			v, err := strategystore.NewStore(e.pool).Version(t.Context(), strategyName, got.Version.Number)
			require.NoError(t, err)
			require.Equal(t, tt.body, v.Body)
			require.Equal(t, &id, v.ProposalID)
			require.Equal(t, p.BenchRunID, v.BenchRunID)
			require.Equal(t, proposals.StatusApplied, loadProposal(t, e.pool, id).Status)
		})
	}
}

// flowAnswer is the scripted turn in which the summarize model submits body.
func flowAnswer(t *testing.T, body string) modeltest.Turn {
	t.Helper()

	input, err := json.Marshal(map[string]string{"body": body, "rationale": "Checkout alerts were mistaken for incidents."})
	require.NoError(t, err)

	return modeltest.ToolUse(model.Usage{InputTokens: 1000, OutputTokens: 200}, modeltest.Call("c1", proposals.SubmitStrategyTool, string(input)))
}
