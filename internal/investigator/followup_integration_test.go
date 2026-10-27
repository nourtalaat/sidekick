//go:build integration

package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Inputs of the follow-up tools.
const (
	// answerC2 answers the follow-up and cites c2.
	answerC2 = `{"answer": "The errors started with deploy 4f2c1a at 14:00.", "evidenceToolCallIds": ["c2"]}`
	// answerC1 answers the follow-up and cites c1, from the first run.
	answerC1 = `{"answer": "Only checkout-api pods log the errors.", "evidenceToolCallIds": ["c1"]}`
	// knownIssueReport changes the verdict to known_issue.
	knownIssueReport = `{"verdict": "known_issue", "confidence": "medium", "reason": "Tracked as OPS-12.",
		"summary": "A known issue, tracked as OPS-12.",
		"findings": [{"claim": "The same errors as OPS-12.", "evidenceToolCallIds": ["c2"]}]}`
)

// The questions the tests ask.
const (
	askDeploy = "Did the deploy at 14:00 cause it?"
	askPods   = "Which pods log the errors?"
)

// errOverloaded is a model error that the next attempt may not have.
var errOverloaded = &model.Error{Kind: model.ErrOverloaded, Err: errors.New("overloaded")}

// longResult is what longSignoz returns: about 36 KB, 12,000 tokens as
// the history counts them.
var longResult = strings.Repeat("checkout-api error rate 5% at 14:02\n", 1000)

// firstRunSteps is how many steps firstRun saves: the prompt, and a model
// turn and its result for each of its two turns.
const firstRunSteps = 5

// firstRun is the turns of a first run that checks c1 and submits
// goodReport.
func firstRun() []modeltest.Turn {
	return []modeltest.Turn{turn(search("c1")), turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport))}
}

func TestFollowUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts options
		// steps is summary() of the steps of the follow-up runs, which
		// start at from, or firstRunSteps when it is zero.
		steps []string
		from  int
		run   func(t *testing.T, h *harness)
	}{
		{
			name: "two quick replies give one run and one answer",
			opts: options{turns: append(firstRun(),
				turn(search("c2")),
				turn(modeltest.Call("a1", report.AnswerFollowUpTool, answerC2)),
			)},
			steps: []string{
				"inbox ok ", "model_turn ok ", "tool_result ok search_logs", "model_turn ok ", "tool_result ok answer_follow_up",
			},
			run: func(t *testing.T, h *harness) {
				t.Helper()

				require.NoError(t, h.run(t))
				require.True(t, h.ask(t, "Ev1", askDeploy))
				require.True(t, h.ask(t, "Ev2", askPods))
				require.False(t, h.ask(t, "Ev1", askDeploy), "a reply that Slack sends again adds nothing")
				require.Equal(t, 1, h.pendingFollowUps(t), "the questions share one job")

				require.NoError(t, h.runFollowUp(t))

				inv := h.investigation(t)
				require.Equal(t, StatusFinal, inv.Status)
				require.EqualValues(t, 2, inv.Run)
				require.Equal(t, "true_positive", deref(inv.Verdict))

				r := h.report(t)
				require.Equal(t, report.TruePositive, r.Verdict, "an answer keeps the verdict")
				require.Len(t, r.FollowUps, 1)

				answer := r.FollowUps[0]
				require.Equal(t, 2, answer.Run)
				require.Equal(t, []report.FollowUpQuestion{{Author: "Ana", AuthorID: "U01ANA", Text: askDeploy}, {Author: "Ana", AuthorID: "U01ANA", Text: askPods}}, answer.Questions)
				require.True(t, answer.Answered)
				require.Equal(t, "The errors started with deploy 4f2c1a at 14:00.", answer.Answer)
				require.Equal(t, []source.Evidence{{Kind: source.EvidenceMetric, Title: "SigNoz", URL: signozDashboard.URL, ToolCallID: "c2"}}, answer.Evidence)
				require.False(t, answer.VerdictChanged())

				_, phase, body := h.publishState(t)
				require.Equal(t, "follow_up", phase)
				require.Len(t, body.Report.FollowUps, 1)

				reqs := h.conn.Requests()
				require.Len(t, reqs, 4)
				require.NotContains(t, toolNames(reqs[0]), report.AnswerFollowUpTool, "only follow-up runs offer answer_follow_up")
				require.Contains(t, toolNames(reqs[2]), report.AnswerFollowUpTool)
				require.Equal(t, toolNames(reqs[0]), toolNames(reqs[2])[:len(reqs[0].Tools)], "a follow-up run has the same tools")

				last := reqs[2].Messages[len(reqs[2].Messages)-1]
				question, ok := last.Parts[len(last.Parts)-1].(model.Text)
				require.True(t, ok)
				require.Contains(t, question.Text, askDeploy)
				require.Contains(t, question.Text, askPods)
				require.Equal(t, reqs[1].Messages, reqs[2].Messages[:len(reqs[1].Messages)], "the rebuilt history starts as the first run's")

				require.Zero(t, h.pendingFollowUps(t))
			},
		},
		{
			name: "a question asked while the first run runs waits for its end",
			opts: options{turns: append(firstRun(), turn(modeltest.Call("a1", report.AnswerFollowUpTool, answerC1)))},
			steps: []string{
				"inbox ok ", "model_turn ok ", "tool_result ok answer_follow_up",
			},
			run: func(t *testing.T, h *harness) {
				t.Helper()

				require.True(t, h.ask(t, "Ev1", askPods))
				require.Zero(t, h.pendingFollowUps(t), "the first run has not ended")

				require.NoError(t, h.run(t))

				for _, s := range h.steps(t) {
					require.NotEqual(t, kindInbox, s.Kind, "the first run leaves the question for the follow-up")
				}

				require.Equal(t, 1, h.pendingFollowUps(t), "the end of the first run enqueues the follow-up")
				require.NoError(t, h.runFollowUp(t))

				r := h.report(t)
				require.Len(t, r.FollowUps, 1)
				require.Equal(t, "Only checkout-api pods log the errors.", r.FollowUps[0].Answer)
			},
		},
		{
			name: "a changed verdict replaces the report and keeps the earlier answers",
			opts: options{turns: append(firstRun(),
				turn(modeltest.Call("a1", report.AnswerFollowUpTool, answerC1)),
				turn(search("c2")),
				turn(modeltest.Call("v2", report.SubmitVerdictTool, knownIssueReport)),
			)},
			steps: []string{
				"inbox ok ", "model_turn ok ", "tool_result ok answer_follow_up",
				"inbox ok ", "model_turn ok ", "tool_result ok search_logs", "model_turn ok ", "tool_result ok submit_verdict",
			},
			run: func(t *testing.T, h *harness) {
				t.Helper()

				require.NoError(t, h.run(t))
				require.True(t, h.ask(t, "Ev1", askPods))
				require.NoError(t, h.runFollowUp(t))
				require.True(t, h.ask(t, "Ev2", "Is this OPS-12 again?"))
				require.NoError(t, h.runFollowUp(t))

				inv := h.investigation(t)
				require.EqualValues(t, 3, inv.Run)
				require.Equal(t, "known_issue", deref(inv.Verdict))

				r := h.report(t)
				require.Equal(t, report.KnownIssue, r.Verdict)
				require.Equal(t, "Tracked as OPS-12.", r.Reason)
				require.Len(t, r.FollowUps, 2, "the new report keeps the earlier answers")
				require.Equal(t, "Only checkout-api pods log the errors.", r.FollowUps[0].Answer)
				require.False(t, r.FollowUps[0].VerdictChanged())
				require.Equal(t, report.TruePositive, r.FollowUps[1].PreviousVerdict)
				require.Equal(t, "A known issue, tracked as OPS-12.", r.FollowUps[1].Answer)
				require.Equal(t, 3, r.FollowUps[1].Run)

				_, phase, body := h.publishState(t)
				require.Equal(t, "final", phase, "a changed verdict is a new final update")
				require.Equal(t, report.KnownIssue, body.Report.Verdict)
			},
		},
		{
			name: "a follow-up run has half the budget, and its own final turn",
			opts: options{
				budget: Budget{MaxToolCalls: 2, MaxWallTime: 10 * time.Minute},
				turns: append(firstRun(),
					turn(search("c2"), search("c3")),
					turn(modeltest.Call("a1", report.AnswerFollowUpTool, answerC2)),
				),
			},
			steps: []string{
				"inbox ok ", "model_turn ok ", "tool_result ok search_logs", "tool_result over_budget " + searchTool,
				"note ok ", "model_turn ok ", "tool_result ok answer_follow_up",
			},
			run: func(t *testing.T, h *harness) {
				t.Helper()

				require.NoError(t, h.run(t))
				require.True(t, h.ask(t, "Ev1", askDeploy))
				require.NoError(t, h.runFollowUp(t))

				require.EqualValues(t, 2, h.investigation(t).ToolCalls, "one call in each run")

				reqs := h.conn.Requests()
				require.Equal(t, []string{report.AnswerFollowUpTool}, toolNames(reqs[len(reqs)-1]), "only answer_follow_up in the final turn")
				require.True(t, h.report(t).FollowUps[0].Answered)
			},
		},
		{
			name: "summaries replace the oldest results when the history is long",
			opts: options{
				caps:   model.Capabilities{ContextTokens: 30_000},
				source: longSignoz(t),
				turns: []modeltest.Turn{
					turn(search("c1"), search("c2")),
					turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
					modeltest.Text(testUsage, "checkout-api logged errors at a rate of 5% at 14:02."),
					turn(modeltest.Call("a1", report.AnswerFollowUpTool, answerC1)),
				},
			},
			steps: []string{
				"inbox ok ", "summary ok search_logs", "model_turn ok ", "tool_result ok answer_follow_up",
			},
			from: firstRunSteps + 1,
			run: func(t *testing.T, h *harness) {
				t.Helper()

				require.NoError(t, h.run(t))
				require.True(t, h.ask(t, "Ev1", askPods))
				require.NoError(t, h.runFollowUp(t))

				steps := h.steps(t)
				require.Equal(t, "c1", deref(steps[7].ToolCallID), "the oldest result is summarized")
				require.Contains(t, string(steps[2].Parts), "checkout-api error rate 5% at 14:02", "the original stays")

				reqs := h.conn.Requests()
				require.Len(t, reqs, 4)
				require.Equal(t, summarizeSystem, reqs[2].System[0].Text)
				require.Equal(t, model.RoleSummarize, model.Role(reqs[2].Metadata[model.MetadataRole]))

				results := toolResults(reqs[3])
				require.Contains(t, results["c1"].Content, summaryNote)
				require.Contains(t, results["c1"].Content, "a rate of 5%")
				require.Contains(t, results["c2"].Content, longResult[:100], "one summary was enough")
				require.NotContains(t, results["c2"].Content, summaryNote)
			},
		},
		{
			name: "a dead follow-up job leaves the report with an answer that says so",
			opts: options{turns: append(firstRun(), modeltest.Fail(errOverloaded))},
			steps: []string{
				"inbox ok ",
			},
			run: func(t *testing.T, h *harness) {
				t.Helper()

				require.NoError(t, h.run(t))
				require.True(t, h.ask(t, "Ev1", askDeploy))

				job := h.claimFollowUp(t)
				require.Error(t, h.handler.FollowUps().Handle(t.Context(), job))
				require.Equal(t, StatusRunning, h.investigation(t).Status)

				require.NoError(t, h.handler.FollowUps().OnDead(t.Context(), job))
				require.NoError(t, h.handler.FollowUps().OnDead(t.Context(), job), "a second call changes nothing")

				inv := h.investigation(t)
				require.Equal(t, StatusFinal, inv.Status)
				require.Equal(t, "true_positive", deref(inv.Verdict))

				r := h.report(t)
				require.Len(t, r.FollowUps, 1)
				require.False(t, r.FollowUps[0].Answered)
				require.Equal(t, unansweredDead, r.FollowUps[0].Answer)
				require.Equal(t, []report.FollowUpQuestion{{Author: "Ana", AuthorID: "U01ANA", Text: askDeploy}}, r.FollowUps[0].Questions)

				_, phase, _ := h.publishState(t)
				require.Equal(t, "follow_up", phase)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.opts)
			tt.run(t, h)

			from := tt.from
			if from == 0 {
				from = firstRunSteps
			}

			require.Equal(t, tt.steps, summary(h.steps(t)[from:]))
		})
	}
}

func TestAddFollowUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    string
		wantAdded bool
		wantJob   bool
	}{
		{name: "an investigation that has ended gets a job", status: StatusFinal, wantAdded: true, wantJob: true},
		{name: "a running investigation keeps the question for later", status: StatusRunning, wantAdded: true},
		{name: "a failed investigation takes no question", status: StatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{})

			_, err := h.pool.Exec(t.Context(), "UPDATE investigations SET status = $1 WHERE id = $2", tt.status, testInvestigation)
			require.NoError(t, err)

			require.Equal(t, tt.wantAdded, h.ask(t, "Ev1", askDeploy))
			require.Equal(t, tt.wantJob, h.pendingFollowUps(t) == 1)
		})
	}
}

func TestFindInvestigation(t *testing.T) {
	t.Parallel()

	h := setup(t, options{})

	_, err := h.pool.Exec(t.Context(), `INSERT INTO output_refs (investigation_id, sink, external, ref, last_seq, published_at)
		VALUES ($1, 'slack-thread', 'C0123ALERTS:1793023321.000100', '{}', 1, now())`, testInvestigation)
	require.NoError(t, err)

	tests := []struct {
		name     string
		sinks    []string
		external string
		want     bool
	}{
		{name: "the thread of an investigation", sinks: []string{"slack-thread"}, external: "C0123ALERTS:1793023321.000100", want: true},
		{name: "another thread", sinks: []string{"slack-thread"}, external: "C0123ALERTS:1793023999.000100"},
		{name: "a sink that is not a Slack output", sinks: []string{"callback"}, external: "C0123ALERTS:1793023321.000100"},
		{name: "no sinks", external: "C0123ALERTS:1793023321.000100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, found, err := FindInvestigation(t.Context(), h.pool, tt.sinks, tt.external)
			require.NoError(t, err)
			require.Equal(t, tt.want, found)

			if tt.want {
				require.Equal(t, testInvestigation, id)
			}
		})
	}
}

// ask adds a follow-up question from Ana with key, and reports whether it
// was added.
func (h *harness) ask(t *testing.T, key, text string) bool {
	t.Helper()

	added, err := AddFollowUp(t.Context(), h.pool, testInvestigation, Question{Key: key, Author: "Ana", AuthorID: "U01ANA", Text: text})
	require.NoError(t, err)

	return added
}

// pendingFollowUps returns the number of pending follow_up jobs.
func (h *harness) pendingFollowUps(t *testing.T) int {
	t.Helper()

	var n int
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'follow_up' AND status = 'pending'").Scan(&n))

	return n
}

// claimFollowUp claims the follow_up job, as a worker does.
func (h *harness) claimFollowUp(t *testing.T) db.Job {
	t.Helper()

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: testWorker, LeaseSeconds: testLease.Seconds(), Kinds: []string{queue.KindFollowUp}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1, "the follow_up job is ready")

	return jobs[0]
}

// runFollowUp claims the follow_up job and runs it, and returns the
// handler's error.
func (h *harness) runFollowUp(t *testing.T) error {
	t.Helper()

	return h.handler.FollowUps().Handle(t.Context(), h.claimFollowUp(t))
}

// longSignoz returns a source like fakeSignoz whose calls all return
// longResult.
func longSignoz(t *testing.T) source.Source {
	t.Helper()

	src := source.NewMockSource(gomock.NewController(t))
	src.EXPECT().Tools(gomock.Any()).Return([]source.Tool{{
		Name:        "search_logs",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string"}}}`),
		ReadOnly:    true,
	}}, nil).AnyTimes()
	src.EXPECT().Call(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, source.Call) (source.Result, error) {
		return source.Result{Text: longResult}, nil
	}).AnyTimes()

	return src
}

// toolResults returns the tool results of req by call ID.
func toolResults(req model.Request) map[string]model.ToolResult {
	out := map[string]model.ToolResult{}

	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if r, ok := p.(model.ToolResult); ok {
				out[r.CallID] = r
			}
		}
	}

	return out
}
