package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// TriageSpanName is the name of the span of triage, inside the
// investigation.run span.
const TriageSpanName = "investigation.triage"

// attrTriageInvestigate says whether triage asked for a full investigation.
const attrTriageInvestigate = "sidekick.triage.investigate"

// submitTriageSchema is the input schema of submit_triage. The verdict is
// a plain string, so a verdict triage may not give is refused with a clear
// reason by triager.check rather than by the schema.
var submitTriageSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["investigate", "strategy", "reason"],
  "properties": {
    "investigate": {"type": "boolean"},
    "strategy": {"type": "string", "maxLength": 64},
    "reason": {"type": "string", "maxLength": 600},
    "verdict": {"type": "string"},
    "confidence": {"enum": ["low", "medium", "high"]}
  }
}`)

// submitTriageDescription is the description of submit_triage.
const submitTriageDescription = "Submit your decision: whether the event needs a full investigation, the name of the " +
	"strategy it should follow, and why. To skip the investigation, set investigate to false and give the verdict, " +
	"duplicate, known_issue or flapping, and your confidence."

// triageOnlyVerdicts are the verdicts a triage-only result may have.
var triageOnlyVerdicts = []report.Verdict{report.Duplicate, report.KnownIssue, report.Flapping}

// The texts of the triage steps, which are also their progress lines. They
// hold only names Sidekick checked, never the model's own words.
const (
	triageInvestigateText = "Triage: a full investigation is needed, with strategy %s."
	triageOnlyText        = "Triage: %s, so no full investigation runs."
	triageFallbackText    = "Triage gave no decision Sidekick could use, so a full investigation runs with strategy %s."
	triageOverrideText    = "A responder asked for a full investigation after the triage-only result."
)

// The audit entry of a request for a full investigation.
const (
	auditInvestigateAnyway = "investigation.investigate_anyway"
	auditTargetKind        = "investigation"
)

// ErrBusy means another investigate job of the investigation's group is
// still pending or running, so a full investigation cannot be queued yet.
var ErrBusy = errors.New("another investigate job of the group is still pending or running")

// auditor writes the audit entries of this package.
var auditor = audit.NewWriter()

// triageOnlyDetail is the stop detail of a triage-only report.
const triageOnlyDetail = "Triage only: no full investigation ran."

// triager asks the triage model and checks its answer. It needs no
// database, so its tests need only a fake connector.
type triager struct {
	schema     *tools.InputSchema
	strategies *strategy.Set
}

// triageDecision is what Sidekick made of the triage model's answer.
type triageDecision struct {
	// investigate asks for a full investigation with strategy. Otherwise
	// the run ends with verdict and confidence.
	investigate bool
	strategy    string
	verdict     report.Verdict
	confidence  report.Confidence
	reason      string
	// callID is the submit_triage call the decision comes from, if any.
	callID string
	// problems say why the answer cannot be used, and are empty when it
	// can.
	problems []string
}

// triageInput is the input of submit_triage.
type triageInput struct {
	Investigate bool              `json:"investigate"`
	Strategy    string            `json:"strategy"`
	Reason      string            `json:"reason"`
	Verdict     report.Verdict    `json:"verdict"`
	Confidence  report.Confidence `json:"confidence"`
}

// triageSteps are the indexes in history.steps of the triage steps, -1 for
// a step not saved, with the triage request and the model's answer.
type triageSteps struct {
	prompt, turn, result, override int

	req    model.Request
	answer []model.Part
}

// triageThenUntilEnd runs triage first, when the route has a triage role
// and the investigation's prompt is not saved yet, then the agent loop,
// unless triage ended the run. ctx and runCtx are as for untilEnd.
func (r *run) triageThenUntilEnd(ctx, runCtx context.Context) (ending, error) {
	if !r.withTriage || r.hist.prompted() {
		return r.untilEnd(ctx, runCtx)
	}

	end, err := r.triage(ctx, runCtx)
	if err != nil {
		return ending{}, fmt.Errorf("triage: %w", err)
	}

	if end != nil {
		return *end, nil
	}

	// The strategy triage chose may have a shorter wall time.
	runCtx, cancel := r.withWallTime(runCtx)
	defer cancel()

	return r.untilEnd(ctx, runCtx)
}

// triage asks the triage model, or reads what an earlier worker saved of
// it, inside an investigation.triage span. It returns the ending of a run
// that triage ends: a triage-only result, or a stop. Otherwise it records
// the strategy triage chose and saves the prompt of the full
// investigation, and returns nil. A triage-only result of an earlier run
// leads to a full investigation, since a responder asked for one.
func (r *run) triage(ctx, runCtx context.Context) (end *ending, err error) {
	ctx, span := r.h.tracer.Start(ctx, TriageSpanName, trace.WithAttributes(
		attribute.String(attrInvestigationID, r.inv.ID.String()),
		attribute.String(attrRoute, r.inv.Route),
		attribute.Int(attrRun, int(r.inv.Run)),
	))

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "triage failed")
		}

		span.End()
	}()

	if r.stopRequested(runCtx) {
		e := r.cancelled()

		return &e, nil
	}

	if r.hist.triage.result < 0 {
		d, decided, err := r.runTriage(ctx, trace.ContextWithSpan(runCtx, span))
		if err != nil {
			return nil, fmt.Errorf("run triage: %w", err)
		}

		if !decided {
			e := r.overBudget(budgetWallTime)
			if r.stopRequested(runCtx) {
				e = r.cancelled()
			}

			return &e, nil
		}

		if err := r.saveTriageResult(ctx, d); err != nil {
			return nil, fmt.Errorf("save the triage decision: %w", err)
		}
	}

	d := r.triageDecision()
	span.SetAttributes(attribute.Bool(attrTriageInvestigate, d.investigate), attribute.String(attrStrategy, d.strategy))

	if !d.investigate {
		if r.hist.steps[r.hist.triage.result].Run == r.inv.Run {
			e := r.triageOnly(d)

			return &e, nil
		}

		if err := r.saveTriageOverride(ctx); err != nil {
			return nil, fmt.Errorf("record the request for a full investigation: %w", err)
		}
	}

	if err := r.useStrategy(ctx, d.strategy); err != nil {
		return nil, fmt.Errorf("use strategy %s: %w", d.strategy, err)
	}

	if err := r.savePrompt(ctx); err != nil {
		return nil, fmt.Errorf("start the full investigation: %w", err)
	}

	return nil, nil
}

// runTriage saves the triage request, asks the triage model, saves its
// answer and returns the decision, resuming after whichever of those an
// earlier worker saved. It returns false, with no error, when the run's
// wall time ended or a responder stopped the run during the call. A triage
// connector that is no longer configured, or a model that refuses the
// request, gives a decision that cannot be used, so the full
// investigation runs. Other model failures return an error, so the job is
// retried. callCtx is runCtx with the triage span.
func (r *run) runTriage(ctx, callCtx context.Context) (triageDecision, bool, error) {
	if r.hist.triage.prompt < 0 {
		if err := r.saveTriagePrompt(ctx); err != nil {
			return triageDecision{}, false, fmt.Errorf("save the triage request: %w", err)
		}
	}

	if r.hist.triage.turn >= 0 {
		return r.h.triager.check(r.hist.triage.answer), true, nil
	}

	conn, ok := r.h.opts.Connectors[r.triageRole.Connector]
	if !ok {
		r.log.ErrorContext(ctx, "the connector of the triage role is not configured", "connector", r.triageRole.Connector)

		return unusable(fmt.Sprintf("connector %q of the triage role is not configured", r.triageRole.Connector)), true, nil
	}

	started := time.Now()

	resp, d, err := r.h.triager.ask(callCtx, conn, r.hist.triage.req, r.inv.Route)
	if err != nil {
		var merr *model.Error

		switch {
		case ctx.Err() != nil:
			return triageDecision{}, false, fmt.Errorf("triage: %w", context.Cause(ctx))
		case wallTimeOver(callCtx) || stopped(callCtx):
			return triageDecision{}, false, nil
		case errors.As(err, &merr) && (merr.Kind == model.ErrInvalid || merr.Kind == model.ErrContextLength):
			r.log.ErrorContext(ctx, "the triage model refused the request", "connector", r.triageRole.Connector, "kind", merr.Kind)

			return unusable(fmt.Sprintf("the triage model could not take the request (%s)", merr.Kind)), true, nil
		default:
			return triageDecision{}, false, fmt.Errorf("triage: %w", err)
		}
	}

	if err := r.saveTriageTurn(ctx, resp, started); err != nil {
		return triageDecision{}, false, fmt.Errorf("save the triage answer: %w", err)
	}

	return d, true, nil
}

// saveTriagePrompt builds the triage request from every loaded strategy and
// the event group, and saves it.
func (r *run) saveTriagePrompt(ctx context.Context) error {
	group, err := r.group(ctx)
	if err != nil {
		return fmt.Errorf("load the event group: %w", err)
	}

	var strategies []strategy.Strategy

	for _, name := range r.h.opts.Strategies.Names() {
		st, _ := r.h.opts.Strategies.Get(name)
		strategies = append(strategies, st)
	}

	req := prompt.BuildTriage(prompt.TriageInput{
		Strategies:    strategies,
		RouteStrategy: deref(r.inv.Strategy),
		Tool:          model.ToolSpec{Name: prompt.SubmitTriageTool, Description: submitTriageDescription, InputSchema: submitTriageSchema},
		Group:         group,
		Route:         prompt.RouteContext{Name: r.inv.Route},
	})

	if _, err := r.save(ctx, newStep{kind: kindTriagePrompt, prompt: &req, status: statusOK, startedAt: time.Now()}); err != nil {
		return fmt.Errorf("save: %w", err)
	}

	return nil
}

// saveTriageTurn saves the triage model's answer, priced like a model turn.
func (r *run) saveTriageTurn(ctx context.Context, resp model.Response, started time.Time) error {
	modelID := r.triageRole.Model
	if modelID == "" {
		modelID = resp.Model
	}

	s := newStep{
		kind:      kindTriageTurn,
		parts:     resp.Message.Parts,
		status:    statusOK,
		connector: r.triageRole.Connector,
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
		return fmt.Errorf("save: %w", err)
	}

	return nil
}

// saveTriageResult saves the decision d. A decision that cannot be used is
// saved as rejected, with its problems as the error, and the full
// investigation runs with the investigation's strategy.
func (r *run) saveTriageResult(ctx context.Context, d triageDecision) error {
	s := newStep{kind: kindTriageResult, tool: prompt.SubmitTriageTool, callID: d.callID, status: statusOK, startedAt: time.Now()}

	var text string

	switch {
	case len(d.problems) > 0:
		text = fmt.Sprintf(triageFallbackText, deref(r.inv.Strategy))
		s.status = statusRejected
		s.errText = store.Text(strings.Join(d.problems, "\n"))
	case d.investigate:
		text = fmt.Sprintf(triageInvestigateText, d.strategy)
	default:
		text = fmt.Sprintf(triageOnlyText, d.verdict)
	}

	s.parts = []model.Part{model.Text{Text: text}}

	if _, err := r.save(ctx, s); err != nil {
		return fmt.Errorf("save the triage decision: %w", err)
	}

	r.log.InfoContext(ctx, "triage decided", "used", len(d.problems) == 0, "investigate", d.investigate || len(d.problems) > 0,
		"strategy", d.strategy, "verdict", d.verdict)

	return nil
}

// saveTriageOverride saves the note that a responder asked for a full
// investigation, unless an earlier worker saved it.
func (r *run) saveTriageOverride(ctx context.Context) error {
	if r.hist.triage.override >= 0 {
		return nil
	}

	note := newStep{kind: kindTriageOverride, parts: []model.Part{model.Text{Text: triageOverrideText}}, status: statusOK, startedAt: time.Now()}
	if _, err := r.save(ctx, note); err != nil {
		return fmt.Errorf("save: %w", err)
	}

	return nil
}

// triageDecision returns the saved decision of triage. A decision saved as
// rejected, or one that no longer passes its checks, for example because
// its strategy was removed since, means a full investigation with the
// investigation's strategy.
func (r *run) triageDecision() triageDecision {
	if r.hist.steps[r.hist.triage.result].Status == statusOK {
		if d := r.h.triager.check(r.hist.triage.answer); len(d.problems) == 0 {
			return d
		}
	}

	return triageDecision{investigate: true, strategy: deref(r.inv.Strategy)}
}

// useStrategy records name as the investigation's strategy, with its
// version and the budget lowered to the strategy's limits, so choosing a
// strategy can tighten a limit but never raise one. A strategy that is
// already the investigation's, or that is not loaded, changes nothing;
// savePrompt reports the second.
func (r *run) useStrategy(ctx context.Context, name string) error {
	st, ok := r.h.opts.Strategies.Get(name)
	if !ok || name == deref(r.inv.Strategy) {
		return nil
	}

	budget := r.budget.tightened(st.Budgets)

	raw, err := json.Marshal(budget)
	if err != nil {
		return fmt.Errorf("encode the budget: %w", err)
	}

	_, err = db.New(r.h.opts.Pool).SetInvestigationStrategy(ctx, db.SetInvestigationStrategyParams{
		Strategy:        &st.Name,
		StrategyVersion: &st.Version,
		Budget:          raw,
		ID:              r.inv.ID,
		Fence:           r.inv.Fence,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("record the strategy: %w", queue.ErrLeaseLost)
	}

	if err != nil {
		return fmt.Errorf("record the strategy: %w", err)
	}

	r.inv.Strategy, r.inv.StrategyVersion, r.inv.Budget, r.budget = &st.Name, &st.Version, raw, budget

	return nil
}

// triageOnly returns the ending of a run that triage ends with d.
func (r *run) triageOnly(d triageDecision) ending {
	return ending{report: report.Report{
		Verdict:    d.verdict,
		Confidence: d.confidence,
		Reason:     d.reason,
		StopReason: report.StopTriage,
		StopDetail: triageOnlyDetail,
	}}
}

// group returns the investigation's event group: the event that opened it
// and the number of events in it.
func (r *run) group(ctx context.Context) (prompt.Group, error) {
	first, err := db.New(r.h.opts.Pool).GetFirstEvent(ctx, r.inv.GroupID)
	if err != nil {
		return prompt.Group{}, fmt.Errorf("load the first event of group %s: %w", r.inv.GroupID, err)
	}

	ev, err := store.EventOf(first.Event)
	if err != nil {
		return prompt.Group{}, fmt.Errorf("read event %s: %w", first.Event.ID, err)
	}

	return prompt.Group{First: ev, Count: int(first.EventCount)}, nil
}

// ask sends req to conn, with the call to submit_triage required when the
// connector can force a tool, and checks the answer. A failed call returns
// the connector's error, wrapped.
func (t triager) ask(ctx context.Context, conn Connector, req model.Request, route string) (model.Response, triageDecision, error) {
	req.Metadata = map[string]string{model.MetadataRole: string(model.RoleTriage), model.MetadataRoute: route}

	if conn.Connector.Capabilities().ToolChoice {
		req.ToolChoice = model.ToolChoice{Mode: model.ToolChoiceRequired, Name: prompt.SubmitTriageTool}
	}

	resp, err := model.Generate(ctx, conn.Connector, req, conn.Retries)
	if err != nil {
		return model.Response{}, triageDecision{}, fmt.Errorf("ask the triage model: %w", err)
	}

	return resp, t.check(resp.Message.Parts), nil
}

// check returns the decision in the first submit_triage call among parts.
// The decision cannot be used, and its problems say why, when there is no
// such call, its input does not match the schema, it names a strategy
// that is not loaded, or it skips the investigation without a verdict
// triage may give (duplicate, known_issue or flapping) or a confidence.
func (t triager) check(parts []model.Part) triageDecision {
	var (
		call  model.ToolCall
		found bool
	)

	for _, p := range parts {
		if c, ok := p.(model.ToolCall); ok && c.Name == prompt.SubmitTriageTool {
			call, found = c, true

			break
		}
	}

	if !found {
		return unusable("the triage model did not call " + prompt.SubmitTriageTool)
	}

	d := triageDecision{callID: call.ID}

	input, err := t.schema.Check(call.Input)
	if err != nil {
		d.problems = []string{err.Error()}

		return d
	}

	var in triageInput
	if err := json.Unmarshal(input, &in); err != nil {
		d.problems = []string{"the input does not decode: " + err.Error()}

		return d
	}

	if _, ok := t.strategies.Get(in.Strategy); !ok {
		d.problems = append(d.problems, fmt.Sprintf("strategy %q is not one of the strategies", in.Strategy))
	}

	if !in.Investigate {
		if !slices.Contains(triageOnlyVerdicts, in.Verdict) {
			d.problems = append(d.problems, fmt.Sprintf("triage may skip the investigation only with %s, %s or %s, not %q",
				report.Duplicate, report.KnownIssue, report.Flapping, in.Verdict))
		}

		if in.Confidence.Rank() == 0 {
			d.problems = append(d.problems, "skipping the investigation needs a confidence: low, medium or high")
		}
	}

	if len(d.problems) > 0 {
		return d
	}

	d.investigate, d.strategy, d.reason = in.Investigate, in.Strategy, in.Reason

	if !in.Investigate {
		d.verdict, d.confidence = in.Verdict, in.Confidence
	}

	return d
}

// add records the triage step s, number i in the history.
func (t *triageSteps) add(i int, s db.Step) error {
	switch s.Kind {
	case kindTriagePrompt:
		var p storedPrompt
		if err := json.Unmarshal(s.Parts, &p); err != nil {
			return fmt.Errorf("decode the triage request: %w", err)
		}

		req, err := p.request()
		if err != nil {
			return fmt.Errorf("decode the triage request: %w", err)
		}

		t.prompt, t.req = i, req
	case kindTriageTurn:
		parts, err := decodeParts(s.Parts)
		if err != nil {
			return fmt.Errorf("decode the triage answer: %w", err)
		}

		t.turn, t.answer = i, parts
	case kindTriageResult:
		t.result = i
	case kindTriageOverride:
		t.override = i
	}

	return nil
}

// tightened returns b with each limit lowered to the strategy's, where the
// strategy sets a lower one. Zero means no limit, on either side.
func (b Budget) tightened(s strategy.Budgets) Budget {
	return Budget{
		MaxTokens:     lowerLimit(b.MaxTokens, int64(s.MaxTokens)),
		MaxToolCalls:  lowerLimit(b.MaxToolCalls, s.MaxToolCalls),
		MaxWallTime:   lowerLimit(b.MaxWallTime, s.MaxWallTime),
		MaxCostMicros: lowerLimit(b.MaxCostMicros, int64(math.Round(s.MaxCostUSD*microDollarsPerUSD))),
	}
}

// InvestigateAnyway starts a full investigation of an investigation whose
// run ended with a triage-only result, as by asked for: it sets the
// investigation back to queued for its next run, enqueues its investigate
// job and writes the audit entry, all in q, which must be a transaction.
// The confirmation of the triage verdict is cleared, so the investigation
// leaves memory until its full verdict is confirmed.
// It returns false, and changes nothing, for an investigation that did not
// end that way, including one whose full investigation has started
// already.
//
// The job that ran the triage holds the group's investigate key until the
// queue marks it done, a moment after the run ended. A request in that
// moment returns an error that wraps ErrBusy; the caller must roll back,
// so the investigation is not left queued without a job, and ask again
// later.
func InvestigateAnyway(ctx context.Context, q db.DBTX, id uuid.UUID, by Requester) (bool, error) {
	inv, err := db.New(q).RestartTriagedInvestigation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("restart investigation %s: %w", id, err)
	}

	if err := memory.Sync(ctx, q, inv.ID); err != nil {
		return false, fmt.Errorf("investigation %s: %w", id, err)
	}

	payload, err := json.Marshal(Payload{InvestigationID: inv.ID, GroupID: inv.GroupID})
	if err != nil {
		return false, fmt.Errorf("encode the investigate payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:      queue.KindInvestigate,
		Route:     inv.Route,
		Payload:   payload,
		UniqueKey: UniqueKey(inv.GroupID),
	})
	if errors.Is(err, queue.ErrDuplicate) {
		return false, fmt.Errorf("investigation %s: %w", id, ErrBusy)
	}

	if err != nil {
		return false, fmt.Errorf("enqueue the investigate job of investigation %s: %w", id, err)
	}

	err = auditor.Write(ctx, q, audit.Entry{
		ActorKind:       audit.ActorUser,
		ActorID:         by.actorID(),
		Action:          auditInvestigateAnyway,
		TargetKind:      auditTargetKind,
		TargetID:        inv.ID.String(),
		InvestigationID: &inv.ID,
		Old:             map[string]any{"run": inv.Run - 1, "stopReason": report.StopTriage},
		New:             map[string]any{"run": inv.Run, "status": inv.Status},
	})
	if err != nil {
		return false, fmt.Errorf("audit the full investigation of investigation %s: %w", id, err)
	}

	return true, nil
}

// unusable returns a decision that cannot be used, for problem.
func unusable(problem string) triageDecision {
	return triageDecision{problems: []string{problem}}
}

// stepText returns the text of a step whose parts are text, such as a
// triage decision.
func stepText(s db.Step) string {
	parts, err := decodeParts(s.Parts)
	if err != nil {
		return ""
	}

	var texts []string

	for _, p := range parts {
		if t, ok := p.(model.Text); ok {
			texts = append(texts, t.Text)
		}
	}

	return strings.Join(texts, "\n")
}

// lowerLimit returns the lower of two limits, where zero means no limit.
func lowerLimit[T int | int64 | time.Duration](a, b T) T {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	default:
		return min(a, b)
	}
}
