package investigator

import (
	"context"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
)

// Budgets an investigation can run out of, as steps.budget and the budget
// label of sidekick_budget_exhausted_total name them.
const (
	budgetTokens    = "tokens"
	budgetToolCalls = "tool_calls"
	budgetWallTime  = "wall_time"
	budgetCost      = "cost"
)

// softLimitPercent is the share of the token, cost and tool call budgets
// at which the model gets its final turn. The rest pays for that turn.
const softLimitPercent = 90

// percent is what softLimitPercent is a share of.
const percent = 100

// microDollarsPerUSD converts micro-dollars to US dollars.
const microDollarsPerUSD = 1_000_000

// The final turn starts wallTimeMargin before the wall time ends, or
// wallTimeMarginPercent of the wall time before when that is shorter.
const (
	wallTimeMargin        = time.Minute
	wallTimeMarginPercent = 10
)

// finalTurnText is the note before the final turn. %s names the budget.
const finalTurnText = "This investigation has nearly used its %s. This is your last turn, and only submit_verdict " +
	"is available. Call submit_verdict now with what the evidence so far supports. The verdict will be recorded " +
	"as inconclusive, with your findings and reasoning."

// followUpFinalTurnText is the note before the final turn of a follow-up
// run. %s names the budget.
const followUpFinalTurnText = "This follow-up has nearly used its %s. This is your last turn, and only " +
	"answer_follow_up is available. Call answer_follow_up now with what the evidence so far supports, and say what " +
	"you could not check."

// finalTurnOnlyText is what the model sees for a call to any tool but
// the final turn's in the final turn. %s names that tool.
const finalTurnOnlyText = "This call was not run: only %s is available in the final turn."

// finalTurnOnlyError is the error of the step of such a call.
const finalTurnOnlyError = "not available in the final turn"

// followUpBudgetDivisor divides each limit of an investigation's budget
// to give the budget of each of its follow-up runs.
const followUpBudgetDivisor = 2

// budgetNames are how the final turn note and the reports name each budget.
var budgetNames = map[string]string{
	budgetTokens:    "token budget",
	budgetToolCalls: "tool calls",
	budgetWallTime:  "time limit",
	budgetCost:      "cost budget",
	budgetDailyCap:  "daily spending cap",
}

// budgetReached returns the budget that stops the run before its next
// model call, if one does, and whether it is used up, so the run ends
// without a final turn. Tokens and cost are used up at their limit; a
// budget at its soft limit gives the model a final turn first. Tool calls,
// the wall time and a daily cap that dailyCapped says is reached always
// allow the final turn.
func (r *run) budgetReached(now time.Time, dailyCapped bool) (budget string, hard bool) {
	b := r.budget
	tokens := r.hist.tokens()

	switch {
	case b.MaxTokens > 0 && tokens >= b.MaxTokens:
		return budgetTokens, true
	case b.MaxCostMicros > 0 && r.costUsed() >= b.MaxCostMicros:
		return budgetCost, true
	case soft(tokens, b.MaxTokens):
		return budgetTokens, false
	case soft(r.costUsed(), b.MaxCostMicros):
		return budgetCost, false
	case soft(int64(r.toolCallsUsed()), int64(b.MaxToolCalls)):
		return budgetToolCalls, false
	case r.pastSoftDeadline(now):
		return budgetWallTime, false
	case dailyCapped:
		return budgetDailyCap, false
	default:
		return "", false
	}
}

// pastSoftDeadline reports whether now is past the time the final turn
// for the wall time starts. A run without a wall time has none.
func (r *run) pastSoftDeadline(now time.Time) bool {
	started, ok := r.started()
	if r.budget.MaxWallTime <= 0 || !ok {
		return false
	}

	margin := min(wallTimeMargin, r.budget.MaxWallTime*wallTimeMarginPercent/percent)

	return now.After(started.Add(r.budget.MaxWallTime - margin))
}

// started returns when the run started, which its wall time counts from:
// for the first run, when the investigation first started, and for a
// follow-up run, when its first step was saved.
func (r *run) started() (time.Time, bool) {
	if !r.runStart.IsZero() {
		return r.runStart, true
	}

	if r.inv.StartedAt != nil {
		return *r.inv.StartedAt, true
	}

	return time.Time{}, false
}

// costUsed returns the cost of the run so far, in micro-dollars.
func (r *run) costUsed() int64 {
	return r.inv.CostMicros - r.before.costMicros
}

// toolCallsUsed returns the source calls the run has made so far.
func (r *run) toolCallsUsed() int32 {
	return r.inv.ToolCalls - r.before.toolCalls
}

// dailyCapped reports whether a daily cap of the run's route is reached.
func (r *run) dailyCapped(ctx context.Context) (bool, error) {
	_, reached, err := r.h.dailyCap(ctx, r.inv.Route)
	if err != nil {
		return false, fmt.Errorf("route %s: %w", r.inv.Route, err)
	}

	return reached, nil
}

// startFinalTurn saves the note that starts the final turn for budget.
func (r *run) startFinalTurn(ctx context.Context, budget string) error {
	text := finalTurnText
	if r.followUp() {
		text = followUpFinalTurnText
	}

	note := newStep{
		kind:      kindNote,
		parts:     []model.Part{model.Text{Text: fmt.Sprintf(text, budgetNames[budget])}},
		status:    statusOK,
		budget:    budget,
		startedAt: time.Now(),
	}

	if _, err := r.save(ctx, note); err != nil {
		return fmt.Errorf("save the final turn note: %w", err)
	}

	r.log.InfoContext(ctx, "budget nearly used: final turn", "budget", budget)

	return nil
}

// finalRequest narrows req to the final turn: the final tool, submit_verdict
// or in a follow-up run answer_follow_up, is the only tool, and the model
// must call it when the connector can force a tool.
func (r *run) finalRequest(req model.Request) model.Request {
	var tools []model.ToolSpec

	for _, t := range req.Tools {
		if t.Name == r.finalTool() {
			tools = append(tools, t)
		}
	}

	req.Tools = tools

	if r.conn.Connector.Capabilities().ToolChoice {
		req.ToolChoice = model.ToolChoice{Mode: model.ToolChoiceRequired, Name: r.finalTool()}
	}

	return req
}

// finalTool returns the only tool of the final turn.
func (r *run) finalTool() string {
	if r.followUp() {
		return report.AnswerFollowUpTool
	}

	return report.SubmitVerdictTool
}

// submittedInFinalTurn returns the ending of a run whose model submitted
// rep in its final turn for budget: the model's report, kept with its
// findings and reasoning, but inconclusive with stop reason budget.
func (r *run) submittedInFinalTurn(rep report.Report, budget string) ending {
	detail := fmt.Sprintf("%s nearly reached; the verdict submitted in the final turn was %s", r.limitText(budget), rep.Verdict)

	rep.Verdict = report.Inconclusive
	rep.Confidence = report.Low
	rep.StopReason = report.StopBudget
	rep.StopDetail = detail

	return ending{report: rep, budget: budget}
}

// overBudget returns the ending of a run that ran out of budget: an
// inconclusive report built from the saved steps.
func (r *run) overBudget(budget string) ending {
	reason := "The investigation reached its " + budgetNames[budget] + " before it reached a verdict."

	switch budget {
	case budgetToolCalls:
		reason = fmt.Sprintf("The investigation used all of its %d tool calls before it reached a verdict.", r.budget.MaxToolCalls)
	case budgetWallTime:
		reason = fmt.Sprintf("The investigation reached its time limit of %s before it reached a verdict.", r.budget.MaxWallTime)
	case budgetTokens:
		reason = fmt.Sprintf("The investigation used its budget of %d tokens before it reached a verdict.", r.budget.MaxTokens)
	case budgetCost:
		reason = fmt.Sprintf("The investigation used its cost budget of %s before it reached a verdict.", usd(r.budget.MaxCostMicros))
	case budgetDailyCap:
		reason = "The daily spending cap was reached before the investigation reached a verdict."
	}

	return ending{report: r.hist.inconclusive(report.StopBudget, reason, r.limitText(budget)+" reached"), budget: budget}
}

// limitText names the limit of budget with its value, as the config
// does, for the stop detail.
func (r *run) limitText(budget string) string {
	switch budget {
	case budgetTokens:
		return fmt.Sprintf("maxTokens %d", r.budget.MaxTokens)
	case budgetToolCalls:
		return fmt.Sprintf("maxToolCalls %d", r.budget.MaxToolCalls)
	case budgetWallTime:
		return fmt.Sprintf("maxWallTime %s", r.budget.MaxWallTime)
	case budgetCost:
		return "maxCostUSD " + dollars(r.budget.MaxCostMicros)
	case budgetDailyCap:
		return "daily cap"
	default:
		return budget
	}
}

// followUpShare returns a follow-up run's share of limit: limit divided by
// followUpBudgetDivisor, and at least 1 when limit is set. Zero, no limit,
// stays zero.
func followUpShare[T int | int64 | time.Duration](limit T) T {
	if limit <= 0 {
		return limit
	}

	return max(limit/followUpBudgetDivisor, 1)
}

// soft reports whether used is at the soft limit of limit; a limit of
// zero has none.
func soft(used, limit int64) bool {
	return limit > 0 && used*percent >= limit*softLimitPercent
}

// usd formats micro-dollars as US dollars, such as $1.00.
func usd(micros int64) string {
	return "$" + dollars(micros)
}

// dollars formats micro-dollars as a number of US dollars, such as 1.00.
func dollars(micros int64) string {
	return fmt.Sprintf("%.2f", float64(micros)/microDollarsPerUSD)
}
