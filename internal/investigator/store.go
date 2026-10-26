package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// maxDurationMS is the longest step duration steps.duration_ms holds.
const maxDurationMS = math.MaxInt32

// newStep is a step to save. Empty strings are stored as NULL.
type newStep struct {
	kind string
	// parts are the step's parts; prompt is set instead for a prompt step.
	parts  []model.Part
	prompt *model.Request
	status string

	source, tool, callID, errText string
	evidence                      []source.Evidence

	// For model turns.
	connector, model string
	usage            *model.Usage
	cost             *int64

	// toolCalls counts toward the tool call limit; finding marks an
	// accepted record_finding.
	toolCalls int32
	finding   bool

	// inbox are the IDs of the inbox items an inbox step delivers.
	inbox []int64

	startedAt time.Time
	duration  time.Duration
}

// save writes s as the next step, in one transaction with the fenced
// advance of next_seq, the marking of the inbox items it delivers and,
// when the step adds a progress line, the new publish state. It returns the saved step, or an error that wraps
// queue.ErrLeaseLost when a newer claim owns the run.
func (r *run) save(ctx context.Context, s newStep) (db.Step, error) {
	params, err := r.stepParams(s)
	if err != nil {
		return db.Step{}, fmt.Errorf("save: %w", err)
	}

	step := db.Step(params)
	line, hasLine := r.hist.progressLine(step)

	var adv db.AdvanceInvestigationRow

	err = pgx.BeginFunc(ctx, r.h.opts.Pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		var usage model.Usage
		if s.usage != nil {
			usage = *s.usage
		}

		adv, err = queries.AdvanceInvestigation(ctx, db.AdvanceInvestigationParams{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CostMicros:   deref(s.cost),
			ToolCalls:    s.toolCalls,
			Finding:      s.finding,
			ID:           r.inv.ID,
			Fence:        r.inv.Fence,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("take step %d: %w", step.Seq, queue.ErrLeaseLost)
		}

		if err != nil {
			return fmt.Errorf("take step %d: %w", step.Seq, err)
		}

		if adv.Seq != step.Seq {
			return fmt.Errorf("take step %d: the database gave seq %d", step.Seq, adv.Seq)
		}

		if err := queries.InsertStep(ctx, params); err != nil {
			return fmt.Errorf("insert step %d: %w", step.Seq, err)
		}

		if err := markDelivered(ctx, queries, r.inv.ID, step.Seq, s.inbox); err != nil {
			return fmt.Errorf("step %d: %w", step.Seq, err)
		}

		if s.kind != kindPrompt && !hasLine {
			return nil
		}

		progress := r.hist.progress()
		if hasLine {
			progress = append(progress, line)
		}

		return publishRunning(ctx, tx, r.inv.ID, step.Seq, progress)
	})
	if err != nil {
		return db.Step{}, fmt.Errorf("save step %d: %w", step.Seq, err)
	}

	if err := r.hist.add(step); err != nil {
		return db.Step{}, fmt.Errorf("add step %d: %w", step.Seq, err)
	}

	r.account(s, adv)

	return step, nil
}

// account adds a saved step to the investigation as this run holds it.
func (r *run) account(s newStep, adv db.AdvanceInvestigationRow) {
	r.inv.NextSeq++
	r.inv.ToolCalls += s.toolCalls

	if s.usage != nil {
		r.inv.InputTokens += s.usage.InputTokens
		r.inv.OutputTokens += s.usage.OutputTokens
	}

	r.inv.CostMicros += deref(s.cost)

	if r.inv.FirstFindingAt == nil && adv.FirstFindingAt != nil {
		r.inv.FirstFindingAt = adv.FirstFindingAt
		r.h.opts.Metrics.firstFinding(r.inv.Route, adv.FirstFindingAt.Sub(*r.inv.StartedAt))
	}
}

// stepParams returns the row of s as the next step.
func (r *run) stepParams(s newStep) (db.InsertStepParams, error) {
	var (
		parts json.RawMessage
		err   error
	)

	if s.prompt != nil {
		parts, err = encodePrompt(*s.prompt)
	} else {
		parts, err = encodeParts(s.parts)
	}

	if err != nil {
		return db.InsertStepParams{}, fmt.Errorf("encode step %d: %w", r.inv.NextSeq, err)
	}

	p := db.InsertStepParams{
		InvestigationID: r.inv.ID,
		Seq:             r.inv.NextSeq,
		Run:             r.inv.Run,
		Kind:            s.kind,
		Parts:           parts,
		Source:          optional(s.source),
		Tool:            optional(s.tool),
		ToolCallID:      optional(s.callID),
		Status:          s.status,
		Error:           optional(store.Text(s.errText)),
		Connector:       optional(s.connector),
		Model:           optional(s.model),
		CostMicros:      s.cost,
		StartedAt:       s.startedAt,
		DurationMs:      int32(min(s.duration.Milliseconds(), maxDurationMS)), //nolint:gosec // Capped at maxDurationMS.
	}

	if len(s.evidence) > 0 {
		raw, err := json.Marshal(s.evidence)
		if err != nil {
			return db.InsertStepParams{}, fmt.Errorf("encode the evidence: %w", err)
		}

		p.Evidence = store.JSON(raw)
	}

	if s.usage != nil {
		p.InputTokens = &s.usage.InputTokens
		p.OutputTokens = &s.usage.OutputTokens
		p.CacheReadTokens = &s.usage.CacheReadTokens
		p.CacheWriteTokens = &s.usage.CacheWriteTokens
	}

	return p, nil
}

// finish ends the run with end: it adds what was checked, stores the
// report and the final status, fenced, and publishes the final update, in
// one transaction.
func (r *run) finish(ctx context.Context, end ending) error {
	end.report.Checked = r.hist.checked(r.tools.Checked())

	raw, err := json.Marshal(end.report)
	if err != nil {
		return fmt.Errorf("encode the report: %w", err)
	}

	status := end.status
	if status == "" {
		status = StatusFinal
	}

	var finished db.Investigation

	err = pgx.BeginFunc(ctx, r.h.opts.Pool, func(tx pgx.Tx) error {
		finished, err = db.New(tx).FinishInvestigation(ctx, db.FinishInvestigationParams{
			Status:     status,
			Verdict:    new(string(end.report.Verdict)),
			Confidence: new(string(end.report.Confidence)),
			Report:     store.JSON(raw),
			StopReason: new(string(end.report.StopReason)),
			ID:         r.inv.ID,
			Fence:      r.inv.Fence,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store the report: %w", queue.ErrLeaseLost)
		}

		if err != nil {
			return fmt.Errorf("store the report: %w", err)
		}

		return publishFinal(ctx, tx, finished, r.hist.progress(), &end.report)
	})
	if err != nil {
		return fmt.Errorf("finish: %w", err)
	}

	r.h.finished(ctx, r.log, finished, end)

	return nil
}

// finished records the end of an investigation in the metrics and the log.
func (h *Handler) finished(ctx context.Context, log *slog.Logger, inv db.Investigation, end ending) {
	h.opts.Metrics.finished(inv, end)

	log.InfoContext(ctx, "investigation finished",
		"status", inv.Status, "verdict", end.report.Verdict, "stop_reason", end.report.StopReason,
		"steps", inv.NextSeq, "tool_calls", inv.ToolCalls)
}

// publishRunning publishes the progress of a running investigation, as of
// the step with seq.
func publishRunning(ctx context.Context, tx pgx.Tx, id uuid.UUID, seq int32, progress []output.Progress) error {
	if err := publish.Enqueue(ctx, tx, publish.Update{
		InvestigationID: id,
		Phase:           output.PhaseRunning,
		Seq:             int64(seq) + 1,
		Progress:        progress,
	}); err != nil {
		return fmt.Errorf("publish step %d: %w", seq, err)
	}

	return nil
}

// publishFinal publishes the report of an ended investigation. Its seq is
// above that of every step's update.
func publishFinal(ctx context.Context, tx pgx.Tx, inv db.Investigation, progress []output.Progress, rep *report.Report) error {
	if err := publish.Enqueue(ctx, tx, publish.Update{
		InvestigationID: inv.ID,
		Phase:           output.PhaseFinal,
		Seq:             int64(inv.NextSeq) + 1,
		Progress:        progress,
		Report:          rep,
	}); err != nil {
		return fmt.Errorf("publish the report: %w", err)
	}

	return nil
}

// markDelivered marks the inbox items with ids as delivered by the step
// with seq. Each must still be waiting: the fence makes this worker the
// only one that delivers, so an item delivered already means the run's
// view is out of date, and the step is not saved.
func markDelivered(ctx context.Context, queries *db.Queries, investigationID uuid.UUID, seq int32, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	marked, err := queries.MarkInboxDelivered(ctx, db.MarkInboxDeliveredParams{DeliveredSeq: &seq, InvestigationID: investigationID, Ids: ids})
	if err != nil {
		return fmt.Errorf("mark %d inbox items delivered: %w", len(ids), err)
	}

	if marked != int64(len(ids)) {
		return fmt.Errorf("mark %d inbox items delivered: %d were still waiting", len(ids), marked)
	}

	return nil
}

// optional turns "" into NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
