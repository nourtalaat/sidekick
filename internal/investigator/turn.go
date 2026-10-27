package investigator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// maxParallelToolCalls is how many source calls of one model turn run at
// once.
const maxParallelToolCalls = 4

// overBudgetText is what the model sees for a call past the tool call
// limit.
const overBudgetText = "This call was not run: the investigation has used all of its tool calls."

// runTools answers calls, the tool calls of the last model turn that have
// no result yet. Source calls run in parallel, at most
// maxParallelToolCalls at once and only as many as the tool call limit has
// left; calls past the limit are answered without running. Built-in calls
// are handled in turn, so they see every result saved before them. In the
// final turn only submit_verdict runs, and every other call is answered
// without running. Results are saved in call order, each as soon as it and
// every call before it are done. When runCtx ends, no more results are
// saved.
func (r *run) runTools(ctx, runCtx context.Context, calls []model.ToolCall) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	turnCtx, cancel := context.WithCancel(runCtx)
	defer cancel()

	final := r.hist.finalTurnTaken()

	var pending []chan newStep
	if !final {
		pending = r.startSourceCalls(turnCtx, calls, &wg)
	}

	for i, call := range calls {
		var (
			s   newStep
			err error
		)

		switch {
		case final && call.Name != report.SubmitVerdictTool:
			s = finalTurnOnlyStep(call)
		case isBuiltinName(call.Name):
			s, err = r.builtin(ctx, call)
			if err != nil {
				return fmt.Errorf("built-in tool %s: %w", call.Name, err)
			}
		case pending[i] == nil:
			s = overBudgetStep(call)
		default:
			s = <-pending[i]
		}

		if runCtx.Err() != nil {
			return nil
		}

		if _, err := r.save(ctx, s); err != nil {
			return fmt.Errorf("save the result of a tool call: %w", err)
		}
	}

	return nil
}

// startSourceCalls starts the source calls among calls that the tool call
// limit allows, each in a goroutine of wg, and returns the channel that
// gets each one's result. The channel is nil for a built-in call or a call
// over the limit.
func (r *run) startSourceCalls(ctx context.Context, calls []model.ToolCall, wg *sync.WaitGroup) []chan newStep {
	left := -1
	if r.budget.MaxToolCalls > 0 {
		left = max(r.budget.MaxToolCalls-int(r.inv.ToolCalls), 0)
	}

	slots := make(chan struct{}, maxParallelToolCalls)
	pending := make([]chan newStep, len(calls))

	for i, call := range calls {
		if isBuiltinName(call.Name) || left == 0 {
			continue
		}

		if left > 0 {
			left--
		}

		done := make(chan newStep, 1)
		pending[i] = done

		wg.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
			}

			started := time.Now()
			done <- sourceStep(call, r.tools.Call(ctx, call), started)
		})
	}

	return pending
}

// builtin handles a call to a built-in tool. It fails only when a
// watcher that start_watcher accepted cannot be stored.
func (r *run) builtin(ctx context.Context, call model.ToolCall) (newStep, error) {
	started := time.Now()

	var (
		content  string
		problems []string
		finding  bool
	)

	switch call.Name {
	case report.SubmitVerdictTool:
		_, problems = r.h.builtins.submitted(r.hist, call, r.h.opts.Rules)
		content = "The report was accepted. The investigation is over."

		if len(problems) > 0 {
			rejected := r.hist.count(func(s db.Step) bool {
				return isBuiltin(s, report.SubmitVerdictTool) && s.Status == statusRejected
			})
			content = rejectedText("report", problems) + fmt.Sprintf("\nTries left for submit_verdict: %d.", maxReportTries-1-rejected)
		}
	case RecordFindingTool:
		var f report.Finding

		f, problems = r.h.builtins.recorded(r.hist, call, len(r.hist.recordedFindings()))
		content = fmt.Sprintf("Recorded finding %s.", f.ID)
		finding = len(problems) == 0

		if !finding {
			content = rejectedText("finding", problems)
		}
	case watch.StartWatcherTool:
		res, err := r.startWatcher(ctx, call)
		if err != nil {
			return newStep{}, fmt.Errorf("start the watcher: %w", err)
		}

		content = fmt.Sprintf("Started watcher %q with ID %s. It runs on its own schedule; you do not need to wait for it.", res.Name, res.ID)

		if res.Refusal != "" {
			problems = []string{res.Refusal}
			content = rejectedText("watcher", problems) + "\nNothing started."
		}
	}

	s := newStep{
		kind:      kindToolResult,
		parts:     []model.Part{model.ToolResult{CallID: call.ID, Content: content, IsError: len(problems) > 0}},
		tool:      call.Name,
		callID:    call.ID,
		status:    statusOK,
		finding:   finding,
		startedAt: started,
		duration:  time.Since(started),
	}

	if len(problems) > 0 {
		s.status = statusRejected
		s.errText = store.Text(strings.Join(problems, "\n"))
	}

	return s, nil
}

// startWatcher asks the watch package to start the watcher of a
// start_watcher call of the last model turn.
func (r *run) startWatcher(ctx context.Context, call model.ToolCall) (watch.ModelResult, error) {
	var defaults watch.Settings
	if st, ok := r.h.opts.Strategies.Get(deref(r.inv.Strategy)); ok && st.WatcherDefaults != nil {
		defaults = *st.WatcherDefaults
	}

	res, err := r.h.opts.Watchers.Start(ctx, watch.ModelRequest{
		Route:            r.inv.Route,
		Strategy:         deref(r.inv.Strategy),
		StrategyDefaults: defaults,
		GroupID:          r.inv.GroupID,
		InvestigationID:  r.inv.ID,
		StartedBySeq:     r.hist.steps[r.hist.lastTurn].Seq,
		Input:            call.Input,
		SourceTool:       r.tools.SourceTool,
	})
	if err != nil {
		return watch.ModelResult{}, fmt.Errorf("start_watcher: %w", err)
	}

	return res, nil
}

// sourceStep returns the step of a source call's result.
func sourceStep(call model.ToolCall, res tools.Result, started time.Time) newStep {
	s := newStep{
		kind:      kindToolResult,
		parts:     []model.Part{res.ToolResult},
		source:    res.Source,
		tool:      res.Tool,
		callID:    call.ID,
		status:    stepStatus(res),
		evidence:  res.Evidence,
		toolCalls: 1,
		startedAt: started,
		duration:  time.Since(started),
	}

	if s.tool == "" {
		s.tool = call.Name
	}

	if res.Err != nil {
		s.errText = store.Text(res.Err.Message)
	}

	return s
}

// overBudgetStep returns the step of a call past the tool call limit.
func overBudgetStep(call model.ToolCall) newStep {
	return newStep{
		kind:      kindToolResult,
		parts:     []model.Part{model.ToolResult{CallID: call.ID, Content: overBudgetText, IsError: true}},
		tool:      call.Name,
		callID:    call.ID,
		status:    statusOverBudget,
		startedAt: time.Now(),
	}
}

// finalTurnOnlyStep returns the step of a call to a tool other than
// submit_verdict in the final turn.
func finalTurnOnlyStep(call model.ToolCall) newStep {
	return newStep{
		kind:      kindToolResult,
		parts:     []model.Part{model.ToolResult{CallID: call.ID, Content: finalTurnOnlyText, IsError: true}},
		tool:      call.Name,
		callID:    call.ID,
		status:    statusRejected,
		errText:   "not available in the final turn",
		startedAt: time.Now(),
	}
}

// stepStatus returns the step status of a source call's result.
func stepStatus(res tools.Result) string {
	if res.Err == nil {
		if res.NoData {
			return statusNoData
		}

		return statusOK
	}

	switch res.Err.Kind {
	case tools.KindTimeout:
		return statusTimeout
	case tools.KindUnavailable:
		return statusUnavailable
	case tools.KindInvalidInput, tools.KindUnknownTool:
		return statusRejected
	case tools.KindToolError, tools.KindFailed:
		return statusError
	default:
		return statusError
	}
}
