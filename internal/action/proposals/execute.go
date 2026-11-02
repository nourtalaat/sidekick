package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// SpanName is the name of the span of one run of an execute_action job.
const SpanName = "action.execute"

// Span attributes.
const (
	attrProposalID = "sidekick.proposal.id"
	attrAction     = "sidekick.action"
)

// ExecuteTimeout is how long one call to an action's Execute may take. The
// context it gets ends then, and the attempt counts as failed.
const ExecuteTimeout = 5 * time.Minute

// maxErrorChars is the longest error text a failed proposal keeps.
const maxErrorChars = 1000

var errNoProposalID = errors.New("the payload has no proposal_id")

// Handle runs the execute_action job of an approved proposal inside an
// action.execute span. It moves the proposal to executing, checks again
// that the config allows it and that its parameters are within the
// action's limits, and calls the action's Execute with the idempotency key
// of the proposal, which is the same on every attempt. The result, or the
// error, is stored with the audit entry and the update that shows it.
//
// A proposal that is no longer allowed, outside the limits, or that Execute
// fails for good (an error wrapping action.ErrPermanent, action.ErrLimit or
// action.ErrConflict) ends as failed and the job is done. Any other Execute
// error returns, so the job is tried again with the same key, up to its
// attempts; a crash after Execute made its change is the same case. A
// proposal that has ended already, or was never approved, is left as it is.
func (s *Service) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := s.opts.tracer().Start(queue.TraceContext(ctx, job), SpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "the action did not run")
		}

		span.End()
	}()

	var p ExecutePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode the execute_action payload: %w: %w", queue.ErrGiveUp, err)
	}

	if p.ProposalID == uuid.Nil {
		return fmt.Errorf("%w: %w", queue.ErrGiveUp, errNoProposalID)
	}

	span.SetAttributes(attribute.String(attrProposalID, p.ProposalID.String()))

	row, route, run, err := s.start(ctx, p.ProposalID)
	if err != nil {
		return fmt.Errorf("start proposal %s: %w", p.ProposalID, err)
	}

	if !run {
		return nil
	}

	span.SetAttributes(attribute.String(attrAction, row.Action))

	if err := s.run(ctx, row, route); err != nil {
		return fmt.Errorf("run proposal %s: %w", p.ProposalID, err)
	}

	return nil
}

// OnDead ends the proposal of an execute_action job that has become dead
// as failed, with the last error, unless it ended already. An admin's retry
// of the job then leaves it as it is: a new proposal is needed.
func (s *Service) OnDead(ctx context.Context, job db.Job) error {
	var p ExecutePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode the execute_action payload: %w", err)
	}

	if p.ProposalID == uuid.Nil {
		return errNoProposalID
	}

	reason := fmt.Sprintf("Sidekick tried %d times and stopped.", job.Attempts)
	if job.LastError != nil && *job.LastError != "" {
		reason += " The last error: " + cut(*job.LastError, maxErrorChars)
	}

	if err := s.finish(ctx, p.ProposalID, action.StatusFailed, nil, reason); err != nil {
		return fmt.Errorf("fail proposal %s: %w", p.ProposalID, err)
	}

	return nil
}

// start moves an approved proposal to executing, and finds an executing
// one that an earlier attempt left. It reports whether the proposal is to
// run, and returns it with the route of its investigation.
func (s *Service) start(ctx context.Context, id uuid.UUID) (row db.ActionProposal, route string, run bool, err error) {
	started := false

	err = pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		locked, err := queries.LockActionProposal(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %w", queue.ErrGiveUp, ErrNotFound)
		}

		if err != nil {
			return fmt.Errorf("lock the proposal: %w", err)
		}

		row, route = locked.ActionProposal, locked.Route

		switch action.ProposalStatus(row.Status) {
		case action.StatusExecuting:
			run = true

			return nil
		case action.StatusApproved:
		default:
			return nil
		}

		row, err = queries.StartActionExecution(ctx, id)
		if err != nil {
			return fmt.Errorf("start the execution: %w", err)
		}

		if err := s.writeAudit(ctx, tx, row, AuditExecuting, audit.ActorSystem, "", map[string]any{"status": row.Status, "action": row.Action}); err != nil {
			return fmt.Errorf("write the audit entry: %w", err)
		}

		if err := publish.Refresh(ctx, tx, row.InvestigationID); err != nil {
			return fmt.Errorf("show that the action runs: %w", err)
		}

		run, started = true, true

		return nil
	})
	if err != nil {
		return db.ActionProposal{}, "", false, fmt.Errorf("in a transaction: %w", err)
	}

	if started {
		s.opts.Metrics.reached(row.Action, action.StatusExecuting)
	}

	if !run {
		s.log.InfoContext(ctx, "action proposal not run: it is not approved", "proposal_id", id.String(), "status", row.Status)
	}

	return row, route, run, nil
}

// run checks the proposal again and calls the action. It returns an error
// only when the job should be tried again.
func (s *Service) run(ctx context.Context, row db.ActionProposal, route string) error {
	if reason := s.notAllowed(route, row.Action); reason != "" {
		return s.fail(ctx, row, "The config does not allow this any more: "+reason+". Propose it again if it is still needed.")
	}

	if row.UndoOf != nil {
		undoCtx, cancel := context.WithTimeout(ctx, ExecuteTimeout)
		defer cancel()

		res, err := s.performUndo(undoCtx, row)

		return s.settle(ctx, row, res, err)
	}

	a := s.opts.Actions[row.Action]

	if err := s.validate(ctx, a, row); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return fmt.Errorf("check the parameters again: %w", err)
		}

		return s.fail(ctx, row, "The parameters are not valid any more: "+s.errorText(err)+". Propose it again if it is still needed.")
	}

	snap, err := snapshotOf(row)
	if err != nil {
		return s.fail(ctx, row, "The snapshot stored with the proposal cannot be read: "+s.errorText(err))
	}

	execCtx, cancel := context.WithTimeout(ctx, ExecuteTimeout)
	defer cancel()

	res, err := a.Execute(execCtx, action.Invocation{
		ProposalID:      row.ID.String(),
		InvestigationID: row.InvestigationID.String(),
		Params:          row.Params,
		IdempotencyKey:  row.IdempotencyKey,
		ApprovedBy:      approvedBy(row),
		Snapshot:        snap,
	})

	return s.settle(ctx, row, res, err)
}

// settle ends the proposal from what the action returned, or keeps it for
// another attempt. It returns an error only when the job should be tried
// again.
func (s *Service) settle(ctx context.Context, row db.ActionProposal, res action.Result, err error) error {
	switch {
	case err == nil:
		return s.succeed(ctx, row, res)
	case ctx.Err() != nil:
		return fmt.Errorf("execute: %w", err)
	case isPermanent(err):
		return s.fail(ctx, row, s.errorText(err))
	}

	s.keepAttemptError(ctx, row, err)

	return fmt.Errorf("execute: %w", err)
}

// validate checks the stored parameters against the action's schema and
// limits as they are now.
func (s *Service) validate(ctx context.Context, a action.Action, row db.ActionProposal) error {
	if _, err := s.schemas[row.Action].Check(row.Params); err != nil {
		return fmt.Errorf("check the schema: %w", err)
	}

	if err := a.Validate(ctx, row.Params); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return nil
}

// succeed stores the result of an action that ran.
func (s *Service) succeed(ctx context.Context, row db.ActionProposal, res action.Result) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("encode the result: %w", err)
	}

	return s.finish(ctx, row.ID, action.StatusSucceeded, raw, "")
}

// fail ends the proposal as failed for good. It returns an error only when
// that could not be stored, so the job is tried again.
func (s *Service) fail(ctx context.Context, row db.ActionProposal, reason string) error {
	return s.finish(ctx, row.ID, action.StatusFailed, nil, reason)
}

// finish ends an approved or executing proposal as succeeded, with the
// result, or as failed, with the reason, in one transaction with its audit
// entry and the update that shows it. A proposal that ended already stays
// as it is.
func (s *Service) finish(ctx context.Context, id uuid.UUID, status action.ProposalStatus, result json.RawMessage, reason string) error {
	var (
		row     db.ActionProposal
		changed bool
		undone  bool
	)

	err := pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		if err := s.lockOriginal(ctx, tx, id); err != nil {
			return fmt.Errorf("lock the proposal that is undone: %w", err)
		}

		var err error

		row, err = db.New(tx).FinishActionExecution(ctx, db.FinishActionExecutionParams{
			Status: string(status), Now: s.now(), Result: result, Error: optional(reason), ID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("finish: %w", err)
		}

		changed = true

		details := map[string]any{"status": row.Status, "action": row.Action}
		act := AuditSucceeded

		if status == action.StatusFailed {
			details["error"] = reason
			act = AuditFailed
		}

		if err := s.writeAudit(ctx, tx, row, act, audit.ActorSystem, "", details); err != nil {
			return fmt.Errorf("write the audit entry: %w", err)
		}

		if status == action.StatusSucceeded && row.UndoOf != nil {
			if undone, err = s.markUndone(ctx, tx, row); err != nil {
				return fmt.Errorf("mark the proposal undone: %w", err)
			}
		}

		if err := publish.Refresh(ctx, tx, row.InvestigationID); err != nil {
			return fmt.Errorf("show the result: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("end proposal %s as %s: %w", id, status, err)
	}

	if undone {
		s.opts.Metrics.reached(row.Action, action.StatusUndone)
	}

	if changed {
		s.opts.Metrics.reached(row.Action, status)
		s.log.InfoContext(ctx, "action proposal ended", "proposal_id", id.String(), "investigation_id", row.InvestigationID.String(), "status", string(status))
	}

	return nil
}

// errorText returns the text a failed proposal keeps for err: redacted and
// cut. Errors from an action can carry text from the service it called.
func (s *Service) errorText(err error) string {
	return cut(s.redactReason(err.Error()), maxErrorChars)
}

// isPermanent reports whether repeating the call that returned err cannot
// help.
func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent) || errors.Is(err, action.ErrLimit) || errors.Is(err, action.ErrConflict)
}

// approvedBy names who approved the proposal for Invocation.ApprovedBy: the
// Sidekick user or token ID, or the Slack user ID.
func approvedBy(row db.ActionProposal) string {
	switch {
	case row.DecidedBy != nil:
		return row.DecidedBy.String()
	case row.DecidedBySlackUser != nil:
		return *row.DecidedBySlackUser
	default:
		return audit.ActorPolicy
	}
}

// optional returns a pointer to s, or nil for an empty string.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
