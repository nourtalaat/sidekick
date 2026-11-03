//go:build integration

package rollup

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	pathGroup         = "0192d3a4-0000-7000-8000-0000000000b1"
	pathInvestigation = "0192d3a4-0000-7000-8000-0000000000c1"
	pathWatcher       = "0192d3a4-0000-7000-8000-0000000000d1"
	// pathFence is the fence of the run in the cases that have one.
	pathFence = 7

	pathProposal = "0192d3a4-0000-7000-8000-0000000000a1"
	// pathJob is the ID of the claimed job of the cases that run the daily
	// pull request check.
	pathJob = 9001

	// The marks of the investigation, created on 2026-10-26 at 14:00:01 UTC.
	markStats   = "stats_hourly 2026-10-26 14:00"
	markQuality = "quality_daily 2026-10-26 00:00"
)

// TestEveryPathThatChangesACountedValueMarksItsBuckets runs the queries
// that the router, the investigator, the feedback code, the watchers and
// the API use to change an investigation, a rating or a watcher, and checks
// which buckets they mark.
func TestEveryPathThatChangesACountedValueMarksItsBuckets(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse(pathInvestigation)
	group := uuid.MustParse(pathGroup)
	watcher := uuid.MustParse(pathWatcher)
	user := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f1")
	verdict := "false_positive"
	model := "claude"
	strategy := "default"
	slackUser := "U1"
	report := json.RawMessage(`{}`)

	runningWithFence := "UPDATE investigations SET status = 'running', fence = 7, started_at = now() WHERE id = '" + pathInvestigation + "'"
	finalWithVerdict := "UPDATE investigations SET status = 'final', verdict = 'false_positive', finished_at = now() WHERE id = '" + pathInvestigation + "'"
	startedWatcher := `INSERT INTO watchers (id, group_id, investigation_id, name, origin, check_type, spec, interval_s,
		passes_to_recover, check_timeout_s, status, started_at, deadline)
		VALUES ('` + pathWatcher + `', '` + pathGroup + `', '` + pathInvestigation + `', 'health', 'model', 'alert', '{}', 30, 2, 10,
		'running', now(), now() + interval '1 hour')`

	proposalIn := func(status, expires string) string {
		return `INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at)
			VALUES ('` + pathProposal + `', '` + pathInvestigation + `', 'note', '{}', 'approved', '` + status + `', 'key-1', now(), ` + expires + `)`
	}
	proposalOpen := proposalIn("proposed", "now() + interval '1 hour'")
	proposalApproved := proposalIn("approved", "now() + interval '1 hour'")
	proposalExecuting := proposalIn("executing", "now() + interval '1 hour'")
	proposalPastExpiry := proposalIn("proposed", "now() - interval '1 hour'")
	proposalID := uuid.MustParse(pathProposal)
	proposalWithPullRequest := `INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at, fix_kind)
		VALUES ('` + pathProposal + `', '` + pathInvestigation + `', 'github.pull_request', '{}', 'approved', 'succeeded', 'key-1', now(), now() + interval '1 hour', 'pull_request')`
	pullRequestOpen := `INSERT INTO pull_request_states (proposal_id, repo, number, url, state, checked_at)
		VALUES ('` + pathProposal + `', 'acme/monitors', 7, 'https://github.com/acme/monitors/pull/7', 'open', now())`
	claimedJob := `INSERT INTO jobs (id, kind, payload, status, claim_token) VALUES (` + strconv.Itoa(pathJob) + `, 'check_pull_requests', '{}', 'running', ` + strconv.Itoa(pathFence) + `)
		ON CONFLICT (id) DO NOTHING`
	recorded := db.RecordPullRequestStateParams{
		ProposalID: proposalID, Repo: "acme/monitors", Number: 7, Url: "https://github.com/acme/monitors/pull/7",
		State: "open", Now: time.Now(),
	}
	dailyCheck := func(state string) db.UpsertPullRequestStateParams {
		at := time.Now()
		params := db.UpsertPullRequestStateParams{
			ProposalID: proposalID, Repo: "acme/monitors", Number: 7, Url: "https://github.com/acme/monitors/pull/7",
			State: state, Now: at, JobID: pathJob, ClaimToken: pathFence,
		}

		switch state {
		case "merged":
			params.MergedAt = &at
		case "closed":
			params.ClosedAt = &at
		}

		return params
	}
	attemptError := "the service answered 503"

	tests := []struct {
		name    string
		prepare []string
		run     func(ctx context.Context, t *testing.T, q *db.Queries)
		want    []string
	}{
		{
			name: "the router creates an investigation",
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				err := q.InsertInvestigation(ctx, db.InsertInvestigationParams{
					ID: uuid.New(), GroupID: group, Route: "default", Mode: "read-only", Status: "queued",
					Connectors: json.RawMessage(`{}`), Budget: json.RawMessage(`{}`),
				})
				require.NoError(t, err)
			},
			want: []string{"stats_hourly NOW"},
		},
		{
			name: "a worker starts the run",
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.StartInvestigationRun(ctx, db.StartInvestigationRunParams{ID: id, Fence: pathFence, Now: time.Now()})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "a step of the run, as every step advances the counters",
			prepare: []string{runningWithFence},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.AdvanceInvestigation(ctx, db.AdvanceInvestigationParams{
					ID: id, Fence: pathFence, InputTokens: 10, OutputTokens: 5, CostMicros: 100, ToolCalls: 1,
				})
				require.NoError(t, err)
			},
		},
		{
			name:    "the first model turn",
			prepare: []string{runningWithFence},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.AdvanceInvestigation(ctx, db.AdvanceInvestigationParams{ID: id, Fence: pathFence, Model: &model})
				require.NoError(t, err)
			},
			want: []string{markQuality, markStats},
		},
		{
			name:    "the first finding",
			prepare: []string{runningWithFence},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.AdvanceInvestigation(ctx, db.AdvanceInvestigationParams{ID: id, Fence: pathFence, Finding: true})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "triage chooses the strategy",
			prepare: []string{runningWithFence},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.SetInvestigationStrategy(ctx, db.SetInvestigationStrategyParams{
					ID: id, Fence: pathFence, Strategy: &strategy, Budget: json.RawMessage(`{}`),
				})
				require.NoError(t, err)
			},
			want: []string{markQuality},
		},
		{
			name:    "the run ends with a verdict",
			prepare: []string{runningWithFence},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.FinishInvestigation(ctx, db.FinishInvestigationParams{
					ID: id, Fence: pathFence, Status: "final", Verdict: &verdict, Report: report,
				})
				require.NoError(t, err)
			},
			want: []string{markQuality, markStats},
		},
		{
			name:    "a dead job ends the run",
			prepare: []string{runningWithFence},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.FailInvestigation(ctx, db.FailInvestigationParams{ID: id, Verdict: &verdict, Report: report})
				require.NoError(t, err)
			},
			want: []string{markQuality, markStats},
		},
		{
			name:    "a follow-up run starts",
			prepare: []string{finalWithVerdict},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.StartFollowUpRun(ctx, db.StartFollowUpRunParams{ID: id, Fence: pathFence})
				require.NoError(t, err)
			},
			want: []string{markQuality, markStats},
		},
		{
			name: "a follow-up run ends",
			prepare: []string{
				finalWithVerdict,
				"UPDATE investigations SET status = 'running', run_kind = 'follow_up', run = 2 WHERE id = '" + pathInvestigation + "'",
			},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.EndFollowUpRun(ctx, db.EndFollowUpRunParams{ID: id, Status: "final", Report: report})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "an investigation runs in full after triage",
			prepare: []string{"UPDATE investigations SET status = 'final', stop_reason = 'triage', verdict = 'inconclusive', finished_at = now() WHERE id = '" + pathInvestigation + "'"},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.RestartTriagedInvestigation(ctx, id)
				require.NoError(t, err)
			},
			want: []string{markQuality, markStats},
		},
		{
			name:    "a responder confirms the verdict",
			prepare: []string{finalWithVerdict},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				err := q.SetInvestigationConfirmation(ctx, db.SetInvestigationConfirmationParams{
					ID: id, ConfirmedVerdict: &verdict, ConfirmedBySlackUser: &slackUser,
				})
				require.NoError(t, err)
			},
			want: []string{markQuality},
		},
		{
			name:    "a responder asks the run to stop",
			prepare: []string{runningWithFence},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.RequestInvestigationCancel(ctx, db.RequestInvestigationCancelParams{ID: id, SlackUserID: &slackUser})
				require.NoError(t, err)
			},
		},
		{
			name:    "a Slack user rates the verdict",
			prepare: []string{finalWithVerdict},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.UpsertSlackFeedback(ctx, db.UpsertSlackFeedbackParams{
					ID: uuid.New(), InvestigationID: id, TargetKind: "verdict", TargetRef: "run:1/verdict", Rating: "wrong", SlackUserID: slackUser,
				})
				require.NoError(t, err)
			},
			want: []string{markQuality},
		},
		{
			name:    "a Slack user changes the rating of the verdict",
			prepare: []string{finalWithVerdict, slackRating("correct")},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.UpsertSlackFeedback(ctx, db.UpsertSlackFeedbackParams{
					ID: uuid.New(), InvestigationID: id, TargetKind: "verdict", TargetRef: "run:1/verdict", Rating: "wrong", SlackUserID: slackUser,
				})
				require.NoError(t, err)
			},
			want: []string{markQuality},
		},
		{
			name:    "a web user rates the verdict",
			prepare: []string{finalWithVerdict},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.UpsertUserFeedback(ctx, db.UpsertUserFeedbackParams{
					ID: uuid.New(), InvestigationID: id, TargetKind: "verdict", TargetRef: "run:1/verdict", Rating: "wrong", UserID: &user, Channel: "ui",
				})
				require.NoError(t, err)
			},
			want: []string{markQuality},
		},
		{
			name:    "a user rates a finding",
			prepare: []string{finalWithVerdict},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.UpsertSlackFeedback(ctx, db.UpsertSlackFeedbackParams{
					ID: uuid.New(), InvestigationID: id, TargetKind: "finding", TargetRef: "run:1/finding:f1", Rating: "wrong", SlackUserID: slackUser,
				})
				require.NoError(t, err)
			},
		},
		{
			name: "a watcher starts",
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.InsertWatcher(ctx, db.InsertWatcherParams{
					ID: watcher, GroupID: group, InvestigationID: &id, Name: "health", Origin: "model", CheckType: "alert",
					Spec: json.RawMessage(`{}`), IntervalS: 30, PassesToRecover: 2, CheckTimeoutS: 10, StartedAt: time.Now(), Deadline: time.Now().Add(time.Hour),
				})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "a watcher runs a check",
			prepare: []string{startedWatcher},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.RecordWatcherCheck(ctx, db.RecordWatcherCheckParams{ID: watcher, At: time.Now(), Passed: true})
				require.NoError(t, err)
			},
		},
		{
			name:    "a watcher recovers",
			prepare: []string{startedWatcher},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.EndWatcher(ctx, db.EndWatcherParams{ID: watcher, Status: "recovered", At: time.Now()})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name: "the investigator proposes an action",
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.InsertActionProposal(ctx, db.InsertActionProposalParams{
					ID: proposalID, InvestigationID: id, Action: "note", Params: report, Mode: "approved",
					IdempotencyKey: "key-1", ProposedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
				})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "a person approves the proposal",
			prepare: []string{proposalOpen},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.DecideActionProposal(ctx, db.DecideActionProposalParams{
					Status: "approved", DecidedBySlackUser: &slackUser, Now: time.Now(), ID: proposalID,
				})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "a person rejects the proposal",
			prepare: []string{proposalOpen},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.DecideActionProposal(ctx, db.DecideActionProposalParams{
					Status: "rejected", DecidedBySlackUser: &slackUser, Now: time.Now(), ID: proposalID,
				})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "nobody decides before the proposal expires",
			prepare: []string{proposalPastExpiry},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.ExpireActionProposals(ctx, db.ExpireActionProposalsParams{Now: time.Now(), Batch: 10})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "the action starts to run",
			prepare: []string{proposalApproved},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.StartActionExecution(ctx, proposalID)
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "an attempt of the action fails and will be retried",
			prepare: []string{proposalExecuting},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				require.NoError(t, q.RecordActionAttemptError(ctx, db.RecordActionAttemptErrorParams{Error: &attemptError, ID: proposalID}))
			},
		},
		{
			name:    "the action ends",
			prepare: []string{proposalExecuting},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.FinishActionExecution(ctx, db.FinishActionExecutionParams{Status: "succeeded", Now: time.Now(), ID: proposalID})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "the group resolves and ends its watchers",
			prepare: []string{startedWatcher},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				_, err := q.EndGroupWatchersResolved(ctx, db.EndGroupWatchersResolvedParams{GroupID: group, At: time.Now()})
				require.NoError(t, err)
			},
			want: []string{markStats},
		},
		{
			name:    "the action records a pull request it opened",
			prepare: []string{proposalWithPullRequest},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				require.NoError(t, q.RecordPullRequestState(ctx, recorded))
			},
			want: []string{markStats},
		},
		{
			name:    "the daily check finds a pull request merged",
			prepare: []string{proposalWithPullRequest, pullRequestOpen, claimedJob},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				written, err := q.UpsertPullRequestState(ctx, dailyCheck("merged"))
				require.NoError(t, err)
				require.EqualValues(t, 1, written)
			},
			want: []string{markStats},
		},
		{
			name:    "the daily check finds a pull request closed",
			prepare: []string{proposalWithPullRequest, pullRequestOpen, claimedJob},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				written, err := q.UpsertPullRequestState(ctx, dailyCheck("closed"))
				require.NoError(t, err)
				require.EqualValues(t, 1, written)
			},
			want: []string{markStats},
		},
		{
			name:    "the daily check finds a pull request still open",
			prepare: []string{proposalWithPullRequest, pullRequestOpen, claimedJob},
			run: func(ctx context.Context, t *testing.T, q *db.Queries) {
				written, err := q.UpsertPullRequestState(ctx, dailyCheck("open"))
				require.NoError(t, err)
				require.EqualValues(t, 1, written)
			},
		},
	}

	// The cases share one database, and each starts from the fixture, so
	// that the test does not hold a database and its connections for every
	// case.
	pool := storetest.New(t)

	for _, tt := range tests { //nolint:paralleltest // The cases share one database.
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), "DELETE FROM watchers; DELETE FROM feedback; DELETE FROM investigations; DELETE FROM event_groups",
				pgx.QueryExecModeSimpleProtocol)
			require.NoError(t, err)

			storetest.LoadFixture(t, pool, "fixture/paths.sql")

			for _, statement := range tt.prepare {
				_, err := pool.Exec(t.Context(), statement)
				require.NoError(t, err)
			}

			_, err = pool.Exec(t.Context(), "DELETE FROM rollup_dirty")
			require.NoError(t, err)

			before := time.Now().UTC().Truncate(time.Hour)

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			tt.run(t.Context(), t, db.New(tx))
			require.NoError(t, tx.Commit(t.Context()))

			after := time.Now().UTC().Truncate(time.Hour)

			got := dirtyOf(t, pool)
			if len(tt.want) == 1 && tt.want[0] == "stats_hourly NOW" {
				require.Len(t, got, 1)
				require.Contains(t, []string{"stats_hourly " + before.Format("2006-01-02 15:04"), "stats_hourly " + after.Format("2006-01-02 15:04")}, got[0])

				return
			}

			if len(tt.want) == 0 {
				require.Empty(t, got)

				return
			}

			require.Equal(t, tt.want, got)
		})
	}
}

// slackRating inserts a Slack user's rating of the verdict.
func slackRating(rating string) string {
	return `INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, slack_user_id, channel, created_at, updated_at)
		VALUES ('0192d3a4-0000-7000-8000-0000000000e1', '` + pathInvestigation + `', 'verdict', 'run:1/verdict', '` + rating + `', 'U1', 'slack', now(), now())`
}
