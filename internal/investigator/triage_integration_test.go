//go:build integration

package investigator

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Inputs of submit_triage and submit_verdict for the triage tests.
const (
	triageLatency   = `{"investigate": true, "strategy": "latency-alert", "reason": "A p99 latency alert."}`
	triageDuplicate = `{"investigate": false, "strategy": "latency-alert", "reason": "The same alert as an open incident.",
		"verdict": "duplicate", "confidence": "high"}`
	triageRealIssue = `{"investigate": false, "strategy": "latency-alert", "reason": "Clearly broken.",
		"verdict": "true_positive", "confidence": "high"}`
	triageUnknown = `{"investigate": true, "strategy": "disk-full", "reason": "A disk alert."}`
	notSure       = `{"verdict": "inconclusive", "confidence": "low", "reason": "Not enough data.", "summary": "Unclear.", "findings": []}`
)

// usedTriageSteps is summary() of the steps of a triage whose decision
// the run used.
var usedTriageSteps = []string{"triage_prompt ok ", "triage_turn ok ", "triage_result ok submit_triage"}

func TestTriage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		opts  options
		steps []string
		check func(t *testing.T, h *harness)
	}{
		{
			name: "triage asks for a full investigation with another strategy",
			opts: options{
				triageTurns: []modeltest.Turn{triageTurn(triageLatency)},
				turns:       []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, notSure))},
			},
			steps: append(append([]string(nil), usedTriageSteps...), "prompt ok ", "model_turn ok ", "tool_result ok submit_verdict"),
			check: func(t *testing.T, h *harness) {
				t.Helper()

				inv := h.investigation(t)
				require.Equal(t, StatusFinal, inv.Status)
				require.Equal(t, "latency-alert", *inv.Strategy)

				st, _ := h.handler.opts.Strategies.Get("latency-alert")
				require.Equal(t, st.Version, *inv.StrategyVersion)
				require.Equal(t, testUsage.InputTokens+triageUsage.InputTokens, inv.InputTokens)

				triageReq := h.triage.Requests()[0]
				require.Equal(t, prompt.SubmitTriageTool, triageReq.Tools[0].Name)
				require.Equal(t, string(model.RoleTriage), triageReq.Metadata[model.MetadataRole])

				full := h.conn.Requests()[0]
				require.True(t, strings.HasPrefix(full.System[1].Text, "# Strategy: latency-alert"), "the full investigation follows the chosen strategy")

				_, phase, body := h.publishState(t)
				require.Equal(t, "final", phase)
				require.Equal(t, []string{"Triage: a full investigation is needed, with strategy latency-alert."}, progressTexts(body))

				require.Contains(t, spanNames(h), TriageSpanName)
			},
		},
		{
			name:  "triage ends the run as a duplicate",
			opts:  options{triageTurns: []modeltest.Turn{triageTurn(triageDuplicate)}},
			steps: usedTriageSteps,
			check: func(t *testing.T, h *harness) {
				t.Helper()

				inv := h.investigation(t)
				require.Equal(t, StatusFinal, inv.Status)
				require.Equal(t, string(report.Duplicate), *inv.Verdict)
				require.Equal(t, string(report.StopTriage), *inv.StopReason)
				require.Equal(t, "generic-alert", *inv.Strategy, "a triage-only result keeps the route's strategy")

				r := h.report(t)
				require.Equal(t, report.High, r.Confidence)
				require.Equal(t, "The same alert as an open incident.", r.Reason)
				require.Equal(t, triageOnlyDetail, r.StopDetail)

				_, phase, body := h.publishState(t)
				require.Equal(t, "final", phase)
				require.Equal(t, report.StopTriage, body.Report.StopReason)
				require.Equal(t, []string{"Triage: duplicate, so no full investigation runs."}, progressTexts(body))
			},
		},
		{
			name: "a verdict triage may not give leads to a full investigation",
			opts: options{
				triageTurns: []modeltest.Turn{triageTurn(triageRealIssue)},
				turns:       []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, notSure))},
			},
			steps: []string{
				"triage_prompt ok ", "triage_turn ok ", "triage_result rejected submit_triage",
				"prompt ok ", "model_turn ok ", "tool_result ok submit_verdict",
			},
			check: func(t *testing.T, h *harness) {
				t.Helper()

				requireTriageRejected(t, h, `not "true_positive"`)
			},
		},
		{
			name: "an unknown strategy leads to a full investigation with the route's strategy",
			opts: options{
				triageTurns: []modeltest.Turn{triageTurn(triageUnknown)},
				turns:       []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, notSure))},
			},
			steps: []string{
				"triage_prompt ok ", "triage_turn ok ", "triage_result rejected submit_triage",
				"prompt ok ", "model_turn ok ", "tool_result ok submit_verdict",
			},
			check: func(t *testing.T, h *harness) {
				t.Helper()

				requireTriageRejected(t, h, `strategy "disk-full" is not one of the strategies`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.opts)
			require.NoError(t, h.run(t))
			require.Equal(t, tt.steps, summary(h.steps(t)))
			tt.check(t, h)
		})
	}
}

// TestInvestigateAnyway ends a run with a triage-only result, confirms its
// verdict, asks for a full investigation, runs it with the whole budget
// although earlier runs used it all, and answers a follow-up question
// asked while triage ran. A question to the triage-only result is refused.
func TestInvestigateAnyway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a full investigation after a triage-only result"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{
				budget:      Budget{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostMicros: 1_000_000},
				triageTurns: []modeltest.Turn{triageTurn(triageDuplicate)},
				turns: []modeltest.Turn{
					turn(search("c1")),
					turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
					turn(modeltest.Call("a1", report.AnswerFollowUpTool, answerC1)),
				},
			})

			require.True(t, h.ask(t, "Ev0", askPods), "a question while triage runs waits")

			first := h.claim(t)
			require.NoError(t, h.handler.Handle(t.Context(), first))
			h.complete(t, first)
			require.Zero(t, h.pendingFollowUps(t), "a triage-only result starts no follow-up run")

			_, err := AddFollowUp(t.Context(), h.pool, testInvestigation, Question{Key: "Ev1", AuthorID: "U01ANA", Text: askDeploy})
			require.ErrorIs(t, err, ErrTriageOnly)
			require.Zero(t, h.pendingFollowUps(t))

			// Earlier runs used the whole budget; the full run has its own.
			_, err = h.pool.Exec(t.Context(), `UPDATE investigations SET confirmed_verdict = 'duplicate', confirmed_by_slack_user = 'U01ANA',
				confirmed_at = now(), cost_micros = 1000000, tool_calls = 40 WHERE id = $1`, testInvestigation)
			require.NoError(t, err)
			require.NoError(t, pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
				if err := memory.Sync(t.Context(), tx, testInvestigation, memory.SyncOptions{}); err != nil {
					return fmt.Errorf("sync memory: %w", err)
				}

				return nil
			}))
			require.Equal(t, 1, h.memoryEntries(t), "the confirmed triage verdict is in memory")

			started := h.anyway(t)
			require.True(t, started)
			require.False(t, h.anyway(t), "a second request changes nothing")

			inv := h.investigation(t)
			require.Equal(t, StatusQueued, inv.Status)
			require.EqualValues(t, 2, inv.Run)
			require.Equal(t, RunKindInvestigate, inv.RunKind)
			require.Nil(t, inv.Verdict)
			require.Nil(t, inv.Report)
			require.Nil(t, inv.StopReason)
			require.Nil(t, inv.StartedAt, "the full run's wall time counts from its own start")
			require.Nil(t, inv.ConfirmedVerdict, "the confirmation of the triage verdict does not carry over")
			require.Nil(t, inv.ConfirmedBySlackUser)
			require.Nil(t, inv.ConfirmedAt)
			require.Zero(t, h.memoryEntries(t), "an investigation without a confirmation leaves memory")
			require.Equal(t, []string{`user U01BEN investigation.investigate_anyway {"new": {"run": 2, "status": "queued"}, "old": {"run": 1, "stopReason": "triage"}}`},
				h.auditRows(t))

			require.NoError(t, h.run(t))

			steps := h.steps(t)
			require.Equal(t, append(append([]string(nil), usedTriageSteps...),
				"triage_override ok ", "prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				"model_turn ok ", "tool_result ok submit_verdict"), summary(steps))

			for _, s := range steps[len(usedTriageSteps):] {
				require.EqualValues(t, 2, s.Run, "step %d belongs to the full run", s.Seq)
			}

			require.Len(t, h.triage.Requests(), 1, "the full run does not ask triage again")

			inv = h.investigation(t)
			require.Equal(t, StatusFinal, inv.Status)
			require.Equal(t, string(report.TruePositive), *inv.Verdict, "the earlier runs' use does not count against the full run")
			require.Equal(t, string(report.StopSubmitted), *inv.StopReason)

			_, phase, body := h.publishState(t)
			require.Equal(t, "final", phase)
			require.Equal(t, report.TruePositive, body.Report.Verdict)
			require.Equal(t, []string{
				"Triage: duplicate, so no full investigation runs.",
				triageOverrideText,
				"Checked signoz (search_logs)",
			}, progressTexts(body))

			require.Equal(t, 1, h.pendingFollowUps(t), "the question asked while triage ran is answered now")
			require.NoError(t, h.runFollowUp(t))

			inv = h.investigation(t)
			require.Equal(t, StatusFinal, inv.Status)
			require.EqualValues(t, 3, inv.Run)
			require.Len(t, h.report(t).FollowUps, 1)
		})
	}
}

func TestInvestigateAnywayOnlyAfterTriage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// status and stopReason are what the investigation is set to first.
		status     string
		stopReason *string
	}{
		{name: "a queued investigation", status: StatusQueued},
		{name: "a submitted verdict", status: StatusFinal, stopReason: new(string(report.StopSubmitted))},
		{name: "a stopped run", status: StatusCancelled, stopReason: new(string(report.StopCancelled))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{})

			_, err := h.pool.Exec(t.Context(), "UPDATE investigations SET status = $1, stop_reason = $2 WHERE id = $3",
				tt.status, tt.stopReason, testInvestigation)
			require.NoError(t, err)

			require.False(t, h.anyway(t))
			require.EqualValues(t, 1, h.investigation(t).Run)
		})
	}
}

// TestInvestigateAnywayWhileTheJobFinishes asks for a full investigation
// while the job that ran the triage still holds the group's investigate
// key, and checks that nothing changes.
func TestInvestigateAnywayWhileTheJobFinishes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "the triage job is not marked done yet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{triageTurns: []modeltest.Turn{triageTurn(triageDuplicate)}})
			require.NoError(t, h.run(t))

			_, err := h.tryAnyway(t)
			require.ErrorIs(t, err, ErrBusy)

			inv := h.investigation(t)
			require.Equal(t, StatusFinal, inv.Status, "the reset is rolled back")
			require.EqualValues(t, 1, inv.Run)
			require.Equal(t, string(report.Duplicate), *inv.Verdict)
			require.Empty(t, h.auditRows(t))
		})
	}
}

// requireTriageRejected checks that the triage decision was saved as
// rejected with a problem holding want, and that the full investigation
// followed the route's strategy.
func requireTriageRejected(t *testing.T, h *harness, want string) {
	t.Helper()

	decision := h.steps(t)[2]
	require.Contains(t, deref(decision.Error), want)

	inv := h.investigation(t)
	require.Equal(t, StatusFinal, inv.Status)
	require.Equal(t, "generic-alert", *inv.Strategy)
	require.True(t, strings.HasPrefix(h.conn.Requests()[0].System[1].Text, "# Strategy: generic-alert"))

	_, _, body := h.publishState(t)
	require.Equal(t, []string{"Triage gave no decision Sidekick could use, so a full investigation runs with strategy generic-alert."},
		progressTexts(body))
}

// complete marks job done, as the queue does when Handle returns nil.
func (h *harness) complete(t *testing.T, job db.Job) {
	t.Helper()

	n, err := db.New(h.pool).CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

// anyway asks for a full investigation of the test investigation, in a
// transaction, and returns whether one started.
func (h *harness) anyway(t *testing.T) bool {
	t.Helper()

	started, err := h.tryAnyway(t)
	require.NoError(t, err)

	return started
}

// tryAnyway is anyway, returning the error of a transaction that rolled
// back.
func (h *harness) tryAnyway(t *testing.T) (bool, error) {
	t.Helper()

	var started bool

	err := pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
		var err error

		started, err = InvestigateAnyway(t.Context(), tx, testInvestigation, Requester{SlackUserID: "U01BEN"})
		if err != nil {
			return fmt.Errorf("investigate anyway: %w", err)
		}

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("transaction: %w", err)
	}

	return started, nil
}

// auditRows returns each audit entry of a request for a full investigation
// of the test investigation as "actor action details".
func (h *harness) auditRows(t *testing.T) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(),
		"SELECT actor_kind || ' ' || actor_id || ' ' || action || ' ' || details::text FROM audit_log WHERE investigation_id = $1 AND action = $2 ORDER BY id",
		testInvestigation, auditInvestigateAnyway)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return out
}

// spanNames returns the names of the ended spans.
func spanNames(h *harness) []string {
	var out []string

	for _, s := range h.spans.Ended() {
		out = append(out, s.Name())
	}

	return out
}

// memoryEntries returns how many memory entries the test investigation
// has.
func (h *harness) memoryEntries(t *testing.T) int {
	t.Helper()

	var n int
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM memory_entries WHERE investigation_id = $1", testInvestigation).Scan(&n))

	return n
}
