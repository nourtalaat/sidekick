package investigator

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// FollowUpSpanName is the name of the span of one run of a follow_up job.
const FollowUpSpanName = "investigation.follow_up"

// What a follow-up answer says when the run gave none.
const (
	unansweredBudget  = "Sidekick could not answer: the follow-up reached its %s before it found an answer."
	unansweredNoReply = "Sidekick could not answer: the model gave no answer that passed its checks."
	unansweredFailed  = "Sidekick could not answer: the model could not take the request."
	unansweredDead    = "Sidekick could not answer: the follow-up stopped after repeated failures."
	unansweredStopped = "Sidekick did not answer: a responder stopped the follow-up."
)

// FollowUpHandler runs follow_up jobs: each one answers the follow-up
// questions waiting in an investigation's inbox in a new run of the
// investigation. Get it with Handler.FollowUps, register it for
// queue.KindFollowUp, and set OnDead as the kind's OnDead hook.
type FollowUpHandler struct {
	h *Handler
}

var _ queue.Handler = (*FollowUpHandler)(nil)

// FollowUps returns the handler of follow_up jobs, which runs with the
// settings of h.
func (h *Handler) FollowUps() *FollowUpHandler {
	return &FollowUpHandler{h: h}
}

// Handle runs one follow_up job inside an investigation.follow_up span. A
// follow-up run resumes the conversation from the saved steps, with the
// next run number, the follow-up budget, the same tools and
// answer_follow_up. It delivers the waiting questions, and ends when the
// model answers, submits a new report, or a limit is reached; the
// investigation then shows the report with the answer added. A job for an
// investigation with no waiting question, or whose first run has not
// ended, does nothing; that run's end enqueues the job again.
//
// Errors are those of Handler.Handle.
func (f *FollowUpHandler) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := f.h.tracer.Start(queue.TraceContext(ctx, job), FollowUpSpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "the follow-up run failed")
		}

		span.End()
	}()

	id, err := decodePayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the follow_up payload: %w: %w", queue.ErrGiveUp, err)
	}

	span.SetAttributes(attribute.String(attrInvestigationID, id.String()))

	inv, started, err := f.h.startFollowUp(ctx, job, id)
	if err != nil {
		return fmt.Errorf("take over the follow-up run: %w", err)
	}

	if !started {
		return nil
	}

	span.SetAttributes(attribute.String(attrRoute, inv.Route), attribute.Int(attrRun, int(inv.Run)))

	log := f.h.log.With("investigation_id", id.String(), "job_id", job.ID, "route", inv.Route, "run", inv.Run)

	r, err := f.h.newRun(ctx, inv, log)
	if err != nil {
		return fmt.Errorf("investigation %s: %w", id, err)
	}

	r.job = job

	if err := r.loop(ctx); err != nil {
		return fmt.Errorf("investigation %s: %w", id, err)
	}

	return nil
}

// OnDead is the OnDead hook of the follow_up kind. It ends the follow-up
// run of a dead job: the investigation is final again with its report, an
// answer that says the run stopped is added, and the outputs show it.
// Questions that wait get a new job. The job is dead, so the writes are
// not fenced. An investigation with no follow-up run running is left as
// it is, so calling it again changes nothing.
func (f *FollowUpHandler) OnDead(ctx context.Context, job db.Job) error {
	id, err := decodePayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the payload of dead job %d: %w", job.ID, err)
	}

	queries := db.New(f.h.opts.Pool)

	inv, err := queries.GetInvestigation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("load investigation %s: %w", id, err)
	}

	if inv.Status != StatusRunning || inv.RunKind != RunKindFollowUp {
		return nil
	}

	steps, err := queries.ListSteps(ctx, id)
	if err != nil {
		return fmt.Errorf("load the steps of investigation %s: %w", id, err)
	}

	hist, err := newHistory(steps)
	if err != nil {
		return fmt.Errorf("read the steps of investigation %s: %w", id, err)
	}

	hist.startRun(inv.Run)

	err = pgx.BeginFunc(ctx, f.h.opts.Pool, func(tx pgx.Tx) error {
		rep, err := f.h.followUpReport(ctx, tx, inv, hist, ending{report: report.Report{StopReason: report.StopError}})
		if err != nil {
			return fmt.Errorf("build the report: %w", err)
		}

		rep.FollowUps[len(rep.FollowUps)-1].Answer = unansweredDead

		raw, err := json.Marshal(rep)
		if err != nil {
			return fmt.Errorf("encode the report: %w", err)
		}

		ended, err := db.New(tx).EndFollowUpRun(ctx, db.EndFollowUpRunParams{Status: statusAfter(rep), Report: store.JSON(raw), ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("store the report: %w", err)
		}

		return publishFollowUp(ctx, tx, ended, hist, rep, false)
	})
	if err != nil {
		return fmt.Errorf("end the follow-up run of investigation %s: %w", id, err)
	}

	f.h.log.InfoContext(ctx, "follow-up run stopped: its job is dead", "investigation_id", id.String(), "job_id", job.ID, "run", inv.Run)

	return nil
}

// startFollowUp gives the follow-up run of the investigation with id to
// the claim of job, in one transaction that holds the investigation's row:
// a new run when the investigation has ended and a question waits, or the
// running follow-up run, resumed. Otherwise it gives up the job's unique
// key, so a question that arrives later gets a job of its own, and returns
// false: there is nothing to answer, or the first run has not ended, and
// its end enqueues the job again.
func (h *Handler) startFollowUp(ctx context.Context, job db.Job, id uuid.UUID) (db.Investigation, bool, error) {
	token := queue.ClaimToken(job)

	var (
		inv     db.Investigation
		started bool
	)

	err := pgx.BeginFunc(ctx, h.opts.Pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		locked, err := queries.LockInvestigation(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("investigation %s: %w: %w", id, queue.ErrGiveUp, errNotFound)
		}

		if err != nil {
			return fmt.Errorf("lock investigation %s: %w", id, err)
		}

		if locked.Fence >= token {
			return fmt.Errorf("investigation %s: a newer claim owns the run: %w", id, queue.ErrLeaseLost)
		}

		resume := locked.Status == StatusRunning && locked.RunKind == RunKindFollowUp

		// A new run starts without the Stop of an earlier run, which
		// StartFollowUpRun clears; a resumed run keeps its own.
		if !resume && !h.waitingQuestion(ctx, queries, locked) {
			if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
				return fmt.Errorf("give up the unique key: %w", err)
			}

			return nil
		}

		inv, err = queries.StartFollowUpRun(ctx, db.StartFollowUpRunParams{Fence: token, ID: id})
		if err != nil {
			return fmt.Errorf("start the follow-up run of investigation %s: %w", id, err)
		}

		started = true

		return nil
	})
	if err != nil {
		return db.Investigation{}, false, fmt.Errorf("start the follow-up: %w", err)
	}

	if !started {
		h.log.DebugContext(ctx, "follow-up not run: no question waits, or the first run has not ended", "investigation_id", id.String())
	}

	return inv, started, nil
}

// waitingQuestion reports whether inv has ended with a report and a
// follow-up question waits in its inbox. A failed lookup counts as no
// question; the question gets a job when the next one arrives or a run
// ends.
func (h *Handler) waitingQuestion(ctx context.Context, queries *db.Queries, inv db.Investigation) bool {
	if !answers(inv.Status) {
		return false
	}

	waiting, err := queries.HasWaitingInbox(ctx, db.HasWaitingInboxParams{InvestigationID: inv.ID, Kind: InboxFollowUp})
	if err != nil {
		h.log.WarnContext(ctx, "could not look for waiting follow-up questions", "investigation_id", inv.ID.String(), "error", err)

		return false
	}

	return waiting
}

// finishFollowUp ends a follow-up run with end, in one transaction, fenced
// like finish: the investigation is final again with the report that
// end gives, the outputs show it, the job gives up its unique key, and
// questions that arrived during the run get a new job.
func (r *run) finishFollowUp(ctx context.Context, end ending) error {
	var (
		finished db.Investigation
		rep      report.Report
	)

	err := pgx.BeginFunc(ctx, r.h.opts.Pool, func(tx pgx.Tx) error {
		var err error

		rep, err = r.h.followUpReport(ctx, tx, r.inv, r.hist, end)
		if err != nil {
			return fmt.Errorf("build the report: %w", err)
		}

		if last := &rep.FollowUps[len(rep.FollowUps)-1]; !last.Answered {
			last.Answer = unansweredText(end)
		}

		rep.Checked = r.hist.checked(r.tools.Checked())

		raw, err := json.Marshal(rep)
		if err != nil {
			return fmt.Errorf("encode the report: %w", err)
		}

		finished, err = db.New(tx).FinishInvestigation(ctx, db.FinishInvestigationParams{
			Status:     statusAfter(rep),
			Verdict:    new(string(rep.Verdict)),
			Confidence: new(string(rep.Confidence)),
			Report:     store.JSON(raw),
			StopReason: new(string(rep.StopReason)),
			ID:         r.inv.ID,
			Fence:      r.inv.Fence,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store the report: %w", queue.ErrLeaseLost)
		}

		if err != nil {
			return fmt.Errorf("store the report: %w", err)
		}

		last := rep.FollowUps[len(rep.FollowUps)-1]
		if err := publishFollowUp(ctx, tx, finished, r.hist, rep, last.VerdictChanged()); err != nil {
			return fmt.Errorf("publish: %w", err)
		}

		if err := queue.ReleaseUniqueKey(ctx, tx, r.job); err != nil {
			return fmt.Errorf("give up the unique key: %w", err)
		}

		return enqueueWaitingFollowUp(ctx, tx, finished)
	})
	if err != nil {
		return fmt.Errorf("finish the follow-up: %w", err)
	}

	last := rep.FollowUps[len(rep.FollowUps)-1]
	r.log.InfoContext(ctx, "follow-up finished", "answered", last.Answered, "verdict_changed", last.VerdictChanged(),
		"questions", len(last.Questions), "stop_reason", end.report.StopReason, "steps", finished.NextSeq)

	return nil
}

// followUpReport returns the report of inv after the follow-up run that
// ended with end: the report the run submitted, or the report before it,
// with the run's answer added to the follow-ups of the report before. The
// answer has the questions the run delivered. When the run gave neither,
// the answer is marked unanswered and its text is left for the caller.
func (h *Handler) followUpReport(ctx context.Context, q db.DBTX, inv db.Investigation, hist *history, end ending) (report.Report, error) {
	var prev report.Report
	if len(inv.Report) > 0 {
		if err := json.Unmarshal(inv.Report, &prev); err != nil {
			return report.Report{}, fmt.Errorf("decode the report before the run: %w", err)
		}
	}

	items, err := db.New(q).ListDeliveredKeyedInbox(ctx, db.ListDeliveredKeyedInboxParams{
		InvestigationID: inv.ID,
		FromSeq:         int32(hist.runFrom), //nolint:gosec // A step index, which seq holds.
	})
	if err != nil {
		return report.Report{}, fmt.Errorf("list the questions of the run: %w", err)
	}

	answer := report.FollowUp{Run: int(inv.Run), Questions: []report.FollowUpQuestion{}, At: h.now()}

	for _, item := range items {
		if item.Kind != InboxFollowUp {
			continue
		}

		question, err := questionOf(item)
		if err != nil {
			return report.Report{}, fmt.Errorf("item %d: %w", item.ID, err)
		}

		answer.Questions = append(answer.Questions, question)
	}

	rep := prev

	switch {
	case end.answered:
		answer.Answered, answer.Answer, answer.Evidence = true, end.answer, end.evidence
	case end.report.StopReason == report.StopSubmitted && end.report.Verdict != "":
		rep = end.report
		answer.Answered, answer.Answer = true, cmp.Or(end.report.Summary, end.report.Reason)

		if end.report.Verdict != prev.Verdict {
			answer.PreviousVerdict = prev.Verdict
		}
	}

	rep.FollowUps = append(append([]report.FollowUp(nil), prev.FollowUps...), answer)

	return rep, nil
}

// unansweredText is what the answer of a follow-up run that ended with
// end and no answer says.
func unansweredText(end ending) string {
	switch end.report.StopReason {
	case report.StopBudget:
		return fmt.Sprintf(unansweredBudget, budgetNames[end.budget])
	case report.StopError:
		return unansweredFailed
	case report.StopCancelled:
		return unansweredStopped
	default:
		return unansweredNoReply
	}
}

// statusAfter returns the status of an investigation whose report is rep
// once a follow-up run ends: cancelled while the report is the one of a
// run a responder stopped, and final otherwise.
func statusAfter(rep report.Report) string {
	if rep.StopReason == report.StopCancelled {
		return StatusCancelled
	}

	return StatusFinal
}

// publishFollowUp publishes the report of inv after a follow-up run: as a
// final update when the run changed the verdict, so every output shows
// the new verdict, and as a follow-up update otherwise. Its seq is above
// that of every step.
func publishFollowUp(ctx context.Context, tx pgx.Tx, inv db.Investigation, hist *history, rep report.Report, changed bool) error {
	phase := output.PhaseFollowUp
	if changed {
		phase = output.PhaseFinal
	}

	if err := publish.Enqueue(ctx, tx, publish.Update{
		InvestigationID: inv.ID,
		Phase:           phase,
		Seq:             int64(inv.NextSeq) + 1,
		Progress:        hist.progress(),
		Report:          &rep,
	}); err != nil {
		return fmt.Errorf("publish the follow-up: %w", err)
	}

	return nil
}
