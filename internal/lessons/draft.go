package lessons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

// maxDraftCalls is how many times the model is asked for one lesson: the
// first answer, and up to two more after answers Sidekick could not use.
const maxDraftCalls = 3

// draftMaxTokens limits one answer of the model. A lesson is short.
const draftMaxTokens = 1000

// draftKeyPrefix starts the unique key of a draft_lesson job, which is
// followed by the ID of the feedback.
const draftKeyPrefix = "draft_lesson:"

// The target kinds of feedback, as feedback stores them. This package
// cannot import feedback, which enqueues its jobs.
const (
	targetVerdict  = "verdict"
	targetFinding  = "finding"
	targetFix      = "fix"
	targetToolCall = "tool_call"
)

// The audit log names of a drafted lesson.
const (
	auditActionDrafted = "lesson.drafted"
	auditTargetLesson  = "lesson"
)

// StatusProposed and the other values of lessons.status.
const (
	StatusProposed = "proposed"
	StatusActive   = "active"
)

// draftPayload is the payload of a draft_lesson job.
type draftPayload struct {
	FeedbackID uuid.UUID `json:"feedbackId"`
}

// EnqueueDraft enqueues the draft_lesson job of a feedback row through q.
// Pass the transaction that stores the feedback, so they commit together.
// A feedback row that has a pending or running job already is left to it.
func EnqueueDraft(ctx context.Context, q db.DBTX, feedbackID uuid.UUID) error {
	payload, err := json.Marshal(draftPayload{FeedbackID: feedbackID})
	if err != nil {
		return fmt.Errorf("encode the payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{Kind: queue.KindDraftLesson, Payload: payload, UniqueKey: draftKeyPrefix + feedbackID.String()})
	if errors.Is(err, queue.ErrDuplicate) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("enqueue the %s job: %w", queue.KindDraftLesson, err)
	}

	return nil
}

// DraftOptions set up a DraftJob. Pool, Caller and Strategies are required.
type DraftOptions struct {
	Pool *pgxpool.Pool
	// Caller makes the model calls, of the summarize role.
	Caller *summarize.Caller
	// Strategies are the strategies investigations run with.
	Strategies *strategy.Set
	// Redactor redacts the text of a lesson. Nil leaves it as it is.
	Redactor *redact.Redactor
	// RequireApproval makes a drafted lesson wait as proposed for an admin.
	// Otherwise it is active at once.
	RequireApproval bool
	// Logger gets a line per draft, with IDs and the outcome only, never
	// the text of feedback or of a lesson. Nil logs nothing.
	Logger *slog.Logger
	// Metrics count the drafts. Nil records nothing.
	Metrics *Metrics
}

// DraftJob drafts lessons from feedback. Register it with the queue as the
// handler of queue.KindDraftLesson.
type DraftJob struct {
	pool       *pgxpool.Pool
	caller     *summarize.Caller
	strategies *strategy.Set
	redactor   *redact.Redactor
	approval   bool
	log        *slog.Logger
	metrics    *Metrics
}

var _ queue.Handler = (*DraftJob)(nil)

// auditor writes the audit entries of drafted lessons.
var auditor = audit.NewWriter()

// NewDraftJob returns the handler of draft_lesson jobs.
func NewDraftJob(opts DraftOptions) *DraftJob {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &DraftJob{
		pool: opts.Pool, caller: opts.Caller, strategies: opts.Strategies, redactor: opts.Redactor,
		approval: opts.RequireApproval, log: log, metrics: opts.Metrics,
	}
}

// Handle drafts a lesson from the feedback the job names. It does nothing
// for feedback that is gone, or that a lesson was drafted from already, so
// a repeated job adds no second lesson, and for a job that was queued
// before the summarize role was removed. It asks the model for the lesson
// up to three times, sending back what was wrong with an answer it could
// not use; if none is usable it stores nothing and the job is done. At a
// daily cap the job moves itself to the next day. A model call that fails
// for a reason that may pass is retried by the queue; one that cannot
// succeed ends the job.
func (j *DraftJob) Handle(ctx context.Context, job db.Job) error {
	var p draftPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode the payload of job %d: %w: %w", job.ID, err, queue.ErrGiveUp)
	}

	if !j.caller.Enabled() {
		j.metrics.drafted(resultSkipped)

		return nil
	}

	facts, ok, err := j.load(ctx, p.FeedbackID)
	if err != nil {
		return fmt.Errorf("feedback %s: %w", p.FeedbackID, err)
	}

	if !ok {
		j.metrics.drafted(resultSkipped)

		return nil
	}

	res, err := j.draft(ctx, facts)
	if err != nil {
		return fmt.Errorf("feedback %s: %w", p.FeedbackID, err)
	}

	switch {
	case res.overCap:
		return j.moveToNextDay(ctx, job)
	case res.problems != nil:
		j.metrics.drafted(resultInvalid)
		j.log.WarnContext(ctx, "the model gave no lesson that Sidekick could use", "feedback_id", p.FeedbackID.String(), "calls", maxDraftCalls,
			"problems", strings.Join(res.problems, "; "))

		return nil
	}

	return j.store(ctx, p.FeedbackID, facts, res.lesson)
}

// draftResult is how a draft ended.
type draftResult struct {
	lesson drafted
	// problems are those of the last answer, when none was usable.
	problems []string
	// overCap is set when a daily cap refused a call.
	overCap bool
}

// draft asks the model for the lesson. The calls go through the pool, so
// the spend of each is saved at once.
func (j *DraftJob) draft(ctx context.Context, facts draftFacts) (draftResult, error) {
	base := facts.userMessage()
	parts := base

	var problems []string

	for range maxDraftCalls {
		input, err := j.caller.CallTool(ctx, j.pool, summarize.Request{
			Route:     facts.scope.Route,
			System:    draftSystem,
			Messages:  []model.Message{{Role: model.User, Parts: parts}},
			Tool:      draftTool(),
			MaxTokens: draftMaxTokens,
		})

		switch {
		case errors.Is(err, summarize.ErrOverCap):
			return draftResult{overCap: true}, nil
		case errors.Is(err, summarize.ErrNoToolCall):
			problems = []string{"you did not call " + SubmitLessonTool}
		case err != nil:
			return draftResult{}, fmt.Errorf("ask the model: %w", giveUpIfInvalid(err))
		default:
			lesson, found := j.check(input, facts)
			if len(found) == 0 {
				return draftResult{lesson: lesson}, nil
			}

			problems = found
		}

		parts = append(append([]model.Part(nil), base...), correction(problems))
	}

	return draftResult{problems: problems}, nil
}

// store saves the lesson and its audit entry in one transaction. A lesson
// drafted from the feedback already, for example by a job that was repeated
// after it stored the lesson, is left as it is.
func (j *DraftJob) store(ctx context.Context, feedbackID uuid.UUID, facts draftFacts, l drafted) error {
	status := StatusActive
	if j.approval {
		status = StatusProposed
	}

	id := ids.New()

	var stored bool

	err := pgx.BeginFunc(ctx, j.pool, func(tx pgx.Tx) error {
		_, err := db.New(tx).InsertDraftedLesson(ctx, db.InsertDraftedLessonParams{
			ID:            id,
			Text:          l.text,
			ScopeService:  optional(l.scope.Service),
			ScopeAlert:    optional(l.scope.Alert),
			ScopeRoute:    optional(l.scope.Route),
			ScopeStrategy: optional(l.scope.Strategy),
			Status:        status,
			FeedbackID:    feedbackID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("insert the lesson: %w", err)
		}

		stored = true

		err = auditor.Write(ctx, tx, audit.Entry{
			ActorKind:       audit.ActorSidekick,
			Action:          auditActionDrafted,
			TargetKind:      auditTargetLesson,
			TargetID:        id.String(),
			InvestigationID: &facts.investigationID,
			New:             map[string]any{"text": l.text, "scope": l.scope, "status": status, "origin": "feedback", "feedbackId": feedbackID},
		})
		if err != nil {
			return fmt.Errorf("audit the lesson: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("store the lesson: %w", err)
	}

	if !stored {
		j.metrics.drafted(resultSkipped)

		return nil
	}

	j.metrics.drafted(status)
	j.log.InfoContext(ctx, "lesson drafted", "lesson_id", id.String(), "feedback_id", feedbackID.String(), "status", status,
		"scope", l.scope.Describe())

	return nil
}

// moveToNextDay moves the job to the start of the next day, when the cap
// that refused it counts a new day. The job gives up its unique key first,
// so the job for the next day can take it.
func (j *DraftJob) moveToNextDay(ctx context.Context, job db.Job) error {
	next := j.caller.NextDay()

	err := pgx.BeginFunc(ctx, j.pool, func(tx pgx.Tx) error {
		if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
			return fmt.Errorf("give up the unique key: %w", err)
		}

		_, err := queue.Enqueue(ctx, tx, queue.NewJob{Kind: queue.KindDraftLesson, Payload: job.Payload, UniqueKey: deref(job.UniqueKey), RunAt: next})
		if err != nil {
			return fmt.Errorf("enqueue the job for %s: %w", next.Format("2006-01-02 15:04 MST"), err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("wait for the next day: %w", err)
	}

	j.metrics.drafted(resultOverCap)
	j.log.InfoContext(ctx, "lesson draft held by the daily cap", "job_id", job.ID, "until", next.Format("2006-01-02 15:04 MST"))

	return nil
}

// load reads what a draft is made from. It reports false, with no error,
// when there is nothing to draft: the feedback is gone, a lesson was drafted
// from it already, its investigation or the investigation's events are
// gone, or it has no reason.
func (j *DraftJob) load(ctx context.Context, feedbackID uuid.UUID) (draftFacts, bool, error) {
	queries := db.New(j.pool)

	fb, err := queries.GetFeedbackToDraft(ctx, feedbackID)
	if errors.Is(err, pgx.ErrNoRows) {
		return draftFacts{}, false, nil
	}

	if err != nil {
		return draftFacts{}, false, fmt.Errorf("read the feedback: %w", err)
	}

	if strings.TrimSpace(deref(fb.Reason)) == "" {
		return draftFacts{}, false, nil
	}

	exists, err := queries.LessonExistsForFeedback(ctx, feedbackID)
	if err != nil {
		return draftFacts{}, false, fmt.Errorf("look for a lesson drafted from it: %w", err)
	}

	if exists {
		return draftFacts{}, false, nil
	}

	row, err := queries.GetInvestigationWithEvent(ctx, fb.InvestigationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return draftFacts{}, false, nil
	}

	if err != nil {
		return draftFacts{}, false, fmt.Errorf("read investigation %s: %w", fb.InvestigationID, err)
	}

	ev, err := store.EventOf(row.Event)
	if err != nil {
		return draftFacts{}, false, fmt.Errorf("read event %s: %w", row.Event.ID, err)
	}

	var rep report.Report
	if len(row.Investigation.Report) > 0 {
		// A report that does not decode is shown as an empty one.
		_ = json.Unmarshal(row.Investigation.Report, &rep)
	}

	facts := draftFacts{
		scope: Scope{
			Service: ev.Service, Alert: ev.AlertName, Route: row.Investigation.Route, Strategy: deref(row.Investigation.Strategy),
		},
		feedback: feedbackText(fb),
		event:    eventText(ev.AlertName, ev.Title, ev.Service, ev.Text),
	}

	if st, ok := j.strategies.Get(facts.scope.Strategy); ok {
		facts.strategyPurpose = st.Description
	}

	facts.investigationID = fb.InvestigationID

	facts.part, err = j.partText(ctx, fb, rep)
	if err != nil {
		return draftFacts{}, false, fmt.Errorf("describe the rated part: %w", err)
	}

	return facts, true, nil
}

// partText describes the part of the investigation the feedback rates.
func (j *DraftJob) partText(ctx context.Context, fb db.GetFeedbackToDraftRow, rep report.Report) (string, error) {
	switch fb.TargetKind {
	case targetFinding:
		id, _ := refID(fb.TargetRef, targetFinding)

		for _, f := range rep.Findings {
			if f.ID == id {
				return fact("Finding", f.Claim), nil
			}
		}
	case targetFix:
		id, _ := refID(fb.TargetRef, targetFix)

		for _, f := range rep.Fixes {
			if f.ID == id {
				return fact("Fix", f.Title) + fact("Detail", f.Detail), nil
			}
		}
	case targetToolCall:
		step, err := db.New(j.pool).GetToolCallStep(ctx, db.GetToolCallStepParams{InvestigationID: fb.InvestigationID, ToolCallID: &fb.TargetRef})
		if errors.Is(err, pgx.ErrNoRows) {
			return "A tool call.\n", nil
		}

		if err != nil {
			return "", fmt.Errorf("read the tool call: %w", err)
		}

		return fact("Tool", toolName(deref(step.Source), deref(step.Tool))) + fact("Result", step.Status), nil
	case targetVerdict:
		return fact("Verdict", string(rep.Verdict)) + fact("Confidence", string(rep.Confidence)) + fact("Reason", rep.Reason) +
			fact("Summary", rep.Summary) + fact("Cause", rep.Cause), nil
	}

	return "The rated part is not in the report any more.\n", nil
}

// feedbackText writes what the person said.
func feedbackText(fb db.GetFeedbackToDraftRow) string {
	return fact("Rated", fb.Rating) + fact("Target", fb.TargetKind) + fact("Reason", deref(fb.Reason)) +
		fact("Real verdict", deref(fb.CorrectVerdict)) + fact("Real cause", deref(fb.CorrectCause))
}

// eventText writes the parts of the event that came from its sender.
func eventText(alertName, title, service, text string) string {
	return fact("Alert name", alertName) + fact("Title", title) + fact("Service", service) + fact("Text", text)
}

// refID returns the ID in a target ref of a finding or fix, such as "f1" in
// "run:1/finding:f1".
func refID(ref, kind string) (string, bool) {
	_, rest, found := strings.Cut(ref, "/")
	if !found {
		return "", false
	}

	id, ok := strings.CutPrefix(rest, kind+":")
	if !ok {
		return "", false
	}

	return id, true
}

// giveUpIfInvalid wraps queue.ErrGiveUp into err when it is a model error
// that repeating the job cannot fix: an invalid request, or a prompt the
// model cannot take. Other errors are left to the job's retries.
func giveUpIfInvalid(err error) error {
	if merr, ok := errors.AsType[*model.Error](err); ok && (merr.Kind == model.ErrInvalid || merr.Kind == model.ErrContextLength) {
		return fmt.Errorf("%w: %w", err, queue.ErrGiveUp)
	}

	return err
}

// toolName returns the qualified name of a source's tool, or the tool's own
// name for a built-in tool.
func toolName(sourceName, tool string) string {
	if sourceName == "" {
		return tool
	}

	return sourceName + source.Separator + tool
}

// optional returns &s, or nil for an empty s.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
