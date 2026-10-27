package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// maxReminders is how many times the model is reminded to submit a
// verdict after a turn without a tool call, before the run ends without
// one.
const maxReminders = 2

// reminderText is the note that asks the model for its verdict.
const reminderText = "You ended your turn without calling a tool. Call submit_verdict now with your verdict, " +
	"or call a tool if you need more evidence first. Do not answer with plain text."

// errWallTime is the cause of a run's context when its wall time ends.
var errWallTime = errors.New("the investigation's wall time ended")

// run is one worker's run of an investigation, from where the saved steps
// left off. Only the goroutine that runs the loop uses it.
type run struct {
	h   *Handler
	inv db.Investigation
	log *slog.Logger

	hist   *history
	budget Budget
	tools  *tools.Run

	// conn is the investigate connector, connector its name and modelID
	// the model its steps are priced as.
	conn      Connector
	connector string
	modelID   string
}

// newRun loads the saved steps of a started investigation and prepares
// its run. On the first run it builds the prompt and saves it as step 0.
func (h *Handler) newRun(ctx context.Context, inv db.Investigation, log *slog.Logger) (*run, error) {
	r := &run{h: h, inv: inv, log: log}

	if err := r.settings(); err != nil {
		return nil, fmt.Errorf("read the settings: %w", err)
	}

	steps, err := db.New(h.opts.Pool).ListSteps(ctx, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load the steps: %w", err)
	}

	if len(steps) != int(inv.NextSeq) {
		return nil, fmt.Errorf("%w: %d steps are saved, but next_seq is %d", queue.ErrGiveUp, len(steps), inv.NextSeq)
	}

	r.hist, err = newHistory(steps)
	if err != nil {
		return nil, fmt.Errorf("%w: read the steps: %w", queue.ErrGiveUp, err)
	}

	r.tools = h.opts.Guard.Start(ctx, inv.ID.String(), h.opts.Routes[inv.Route].Sources)

	if len(steps) == 0 {
		if err := r.savePrompt(ctx); err != nil {
			return nil, fmt.Errorf("start the conversation: %w", err)
		}
	}

	log.InfoContext(ctx, "investigation run started", "from_seq", len(steps), "tool_calls", inv.ToolCalls)

	return r, nil
}

// settings reads the investigation's stored settings and finds the
// configured connector and route they name.
func (r *run) settings() error {
	var conns Connectors
	if err := json.Unmarshal(r.inv.Connectors, &conns); err != nil {
		return fmt.Errorf("%w: decode the connectors: %w", queue.ErrGiveUp, err)
	}

	if err := json.Unmarshal(r.inv.Budget, &r.budget); err != nil {
		return fmt.Errorf("%w: decode the budget: %w", queue.ErrGiveUp, err)
	}

	rc := conns[model.RoleInvestigate]

	conn, ok := r.h.opts.Connectors[rc.Connector]
	if !ok {
		r.log.Error("the investigation's connector is not configured", "connector", rc.Connector)

		return fmt.Errorf("%w: connector %q is not configured", queue.ErrGiveUp, rc.Connector)
	}

	if _, ok := r.h.opts.Routes[r.inv.Route]; !ok {
		r.log.Error("the investigation's route is not configured")

		return fmt.Errorf("%w: route %q is not configured", queue.ErrGiveUp, r.inv.Route)
	}

	r.conn, r.connector, r.modelID = conn, rc.Connector, rc.Model

	return nil
}

// savePrompt builds the first request from the strategy, the tools and the
// event that opened the group, and saves it as the prompt step.
func (r *run) savePrompt(ctx context.Context) error {
	name := deref(r.inv.Strategy)

	st, ok := r.h.opts.Strategies.Get(name)
	if !ok {
		r.log.ErrorContext(ctx, "the investigation's strategy is not loaded", "strategy", name)

		return fmt.Errorf("%w: strategy %q is not loaded", queue.ErrGiveUp, name)
	}

	if st.Version != deref(r.inv.StrategyVersion) {
		r.log.InfoContext(ctx, "the strategy changed after the investigation was created; using the loaded version",
			"strategy", name, "stored_version", deref(r.inv.StrategyVersion), "loaded_version", st.Version)
	}

	first, err := db.New(r.h.opts.Pool).GetFirstEvent(ctx, r.inv.GroupID)
	if err != nil {
		return fmt.Errorf("load the first event of group %s: %w", r.inv.GroupID, err)
	}

	ev, err := store.EventOf(first.Event)
	if err != nil {
		return fmt.Errorf("read event %s: %w", first.Event.ID, err)
	}

	req := prompt.Build(prompt.Input{
		Strategy: st,
		Tools:    append(r.h.builtins.specs(), r.tools.Tools()...),
		Group:    prompt.Group{First: ev, Count: int(first.EventCount)},
		Route:    prompt.RouteContext{Name: r.inv.Route},
	})

	if _, err := r.save(ctx, newStep{kind: kindPrompt, prompt: &req, status: statusOK, startedAt: time.Now()}); err != nil {
		return fmt.Errorf("save the prompt: %w", err)
	}

	return nil
}

// loop runs the investigation until it ends, then stores and publishes
// how it ended. A Stop that the Canceller hears ends the run's context, so
// the model or tool call in flight returns at once.
func (r *run) loop(ctx context.Context) error {
	stopCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	unwatch := r.h.opts.Canceller.watch(r.inv.ID, r.inv.Fence, stop)
	defer unwatch()

	if err := r.checkStop(ctx); err != nil {
		return fmt.Errorf("check for a stop: %w", err)
	}

	runCtx, cancel := r.withWallTime(stopCtx)
	defer cancel()

	end, err := r.untilEnd(ctx, runCtx)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	if err := r.finish(ctx, end); err != nil {
		return fmt.Errorf("end the run: %w", err)
	}

	return nil
}

// untilEnd takes steps until the run ends, and returns how. Each pass looks
// at the saved steps to decide what comes next, so a resumed run picks up
// exactly where the last one stopped: it answers the calls of the last
// model turn that have no result, ends the run if the steps say it is
// over, reminds the model when it called no tool, and otherwise delivers
// the inbox and asks the model for a turn. A Stop ends the run as
// cancelled, unless the steps show it was over already. ctx ends when the
// worker must stop; runCtx also ends when the wall time does or a
// responder stops the run.
func (r *run) untilEnd(ctx, runCtx context.Context) (ending, error) {
	for {
		if ctx.Err() != nil {
			return ending{}, fmt.Errorf("stop: %w", context.Cause(ctx))
		}

		if r.stopRequested(runCtx) {
			if end, ok := r.ended(); ok {
				return end, nil
			}

			return r.cancelled(), nil
		}

		if calls := r.hist.unanswered(); len(calls) > 0 && runCtx.Err() == nil {
			if err := r.runTools(ctx, runCtx, calls); err != nil {
				return ending{}, fmt.Errorf("tool calls: %w", err)
			}

			continue
		}

		if end, ok := r.ended(); ok {
			return end, nil
		}

		if wallTimeOver(runCtx) {
			return r.overBudget(budgetWallTime), nil
		}

		if r.hist.needsReminder() {
			if r.hist.count(isNote) >= maxReminders {
				return r.noVerdict(fmt.Sprintf("The model ended its turn without a verdict %d times.", maxReminders+1), ""), nil
			}

			if err := r.remind(ctx); err != nil {
				return ending{}, fmt.Errorf("remind the model: %w", err)
			}

			continue
		}

		if err := r.deliverInbox(ctx); err != nil {
			return ending{}, fmt.Errorf("inbox: %w", err)
		}

		end, err := r.modelTurn(ctx, runCtx)
		if err != nil {
			return ending{}, fmt.Errorf("model turn: %w", err)
		}

		if end != nil {
			return *end, nil
		}
	}
}

// checkStop looks up whether a stop was asked for, so a stop asked for
// before the Canceller watched this run is not missed until the next
// step.
func (r *run) checkStop(ctx context.Context) error {
	stopped, err := db.New(r.h.opts.Pool).ListCancelRequested(ctx, []uuid.UUID{r.inv.ID})
	if err != nil {
		return fmt.Errorf("look up the stop flag: %w", err)
	}

	if len(stopped) > 0 && r.inv.CancelRequestedAt == nil {
		now := time.Now()
		r.inv.CancelRequestedAt = &now
	}

	return nil
}

// stopRequested reports whether a responder asked to stop the run: the
// last step saw the flag, or the Canceller ended runCtx.
func (r *run) stopRequested(runCtx context.Context) bool {
	return r.inv.CancelRequestedAt != nil || stopped(runCtx)
}

// withWallTime returns ctx with the run's wall time deadline, counted from
// when the investigation first started, so time spent before a resume
// counts too.
func (r *run) withWallTime(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.budget.MaxWallTime <= 0 || r.inv.StartedAt == nil {
		return context.WithCancel(ctx)
	}

	return context.WithDeadlineCause(ctx, r.inv.StartedAt.Add(r.budget.MaxWallTime), errWallTime)
}

// ended reports whether the saved steps end the run, and how: an accepted
// report, too many reports that failed their checks, or a call over the
// tool call limit.
func (r *run) ended() (ending, bool) {
	var rejected []db.Step

	for _, s := range r.hist.steps {
		switch {
		case isBuiltin(s, report.SubmitVerdictTool) && s.Status == statusOK:
			rep, _ := r.h.builtins.submitted(r.hist, r.hist.calls[*s.ToolCallID], r.h.opts.Rules)
			rep.StopReason = report.StopSubmitted

			return ending{report: rep}, true
		case isBuiltin(s, report.SubmitVerdictTool) && s.Status == statusRejected:
			rejected = append(rejected, s)
		case s.Kind == kindToolResult && s.Status == statusOverBudget:
			return r.overBudget(budgetToolCalls), true
		}
	}

	if len(rejected) >= maxReportTries {
		last := rejected[len(rejected)-1]

		return r.noVerdict(fmt.Sprintf("The model's report did not pass its checks %d times.", len(rejected)), deref(last.Error)), true
	}

	return ending{}, false
}

// remind saves a note that asks the model for its verdict.
func (r *run) remind(ctx context.Context) error {
	note := newStep{kind: kindNote, parts: []model.Part{model.Text{Text: reminderText}}, status: statusOK, startedAt: time.Now()}
	if _, err := r.save(ctx, note); err != nil {
		return fmt.Errorf("save a reminder: %w", err)
	}

	return nil
}

// modelTurn asks the model for its next turn and saves it. It returns an
// ending when the model refuses the request for good, and nil, with no
// error, when the run's wall time ended or a responder stopped the run
// during the call, which the loop then handles. Other failures return an error, so the job is retried.
func (r *run) modelTurn(ctx, runCtx context.Context) (*ending, error) {
	req := r.hist.request()
	req.Metadata = map[string]string{model.MetadataRole: string(model.RoleInvestigate), model.MetadataRoute: r.inv.Route}

	started := time.Now()

	resp, err := model.Generate(runCtx, r.conn.Connector, req, r.conn.Retries)
	if err != nil {
		var merr *model.Error

		switch {
		case ctx.Err() != nil:
			return nil, fmt.Errorf("model turn: %w", context.Cause(ctx))
		case wallTimeOver(runCtx) || stopped(runCtx):
			return nil, nil
		case errors.As(err, &merr) && (merr.Kind == model.ErrInvalid || merr.Kind == model.ErrContextLength):
			r.log.ErrorContext(ctx, "the model refused the request", "connector", r.connector, "kind", merr.Kind)

			end := r.failed(fmt.Sprintf("The model could not take the investigation's request (%s).", merr.Kind))

			return &end, nil
		default:
			return nil, fmt.Errorf("model turn: %w", err)
		}
	}

	modelID := r.modelID
	if modelID == "" {
		modelID = resp.Model
	}

	s := newStep{
		kind:      kindModelTurn,
		parts:     resp.Message.Parts,
		status:    statusOK,
		connector: r.connector,
		model:     modelID,
		usage:     &resp.Usage,
		startedAt: started,
		duration:  time.Since(started),
	}

	if price, ok := r.h.opts.Prices.Lookup(modelID); ok {
		cost := price.Cost(resp.Usage)
		s.cost = &cost
	}

	if _, err := r.save(ctx, s); err != nil {
		return nil, fmt.Errorf("save the model turn: %w", err)
	}

	return nil, nil
}

// wallTimeOver reports whether the run's wall time has ended.
func wallTimeOver(runCtx context.Context) bool {
	return errors.Is(context.Cause(runCtx), errWallTime)
}

// stopped reports whether a responder's Stop ended runCtx.
func stopped(runCtx context.Context) bool {
	return errors.Is(context.Cause(runCtx), errCancelled)
}

// isNote reports whether s is a note, which so far is always a reminder.
func isNote(s db.Step) bool {
	return s.Kind == kindNote
}
