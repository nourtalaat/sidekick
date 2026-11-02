package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// executeMaxAttempts is how many failures make an execute_action job dead.
// An action changes something outside Sidekick, so it is tried fewer times
// than other jobs.
const executeMaxAttempts = 3

// uniqueKeyPrefix starts the unique key of an execute_action job.
const uniqueKeyPrefix = "action:"

var (
	// ErrExpired means the proposal was not decided before it expired.
	ErrExpired = errors.New("the proposal expired before it was decided")
	// ErrTokenCannotApprove means an API token tried to approve. Only a
	// person who is signed in, or a Slack user, approves a proposal: a
	// script that holds a token could otherwise approve every proposal,
	// which is running actions without a person.
	ErrTokenCannotApprove = errors.New("an action is approved by a person, not by an API token")
	// ErrNotAllowed means the config does not allow the proposal any more:
	// the route no longer lists its action or is read-only now, or the
	// action is no longer defined. The error says which.
	ErrNotAllowed = errors.New("the config no longer allows this proposal")

	// errDecided marks a proposal that was decided already, inside the
	// transaction; Approve and Reject return a *DecidedError instead.
	errDecided = errors.New("the proposal was decided already")

	errNoDecider = errors.New("a decision needs a user, an API token or a Slack user")
)

// NotAllowedError is the error of approving a proposal that the config does
// not allow any more. It matches ErrNotAllowed with errors.Is, and Reason
// says what changed, for example that the route is read-only now.
type NotAllowedError struct {
	Reason string
}

// Error implements error.
func (e *NotAllowedError) Error() string {
	return ErrNotAllowed.Error() + ": " + e.Reason
}

// Is makes the error match ErrNotAllowed.
func (e *NotAllowedError) Is(target error) bool {
	return target == ErrNotAllowed
}

// DecidedError means the proposal was decided already, or is past that, so
// this decision changed nothing. Proposal is its current state, with who
// decided it. A person who clicked second at the same time gets this.
type DecidedError struct {
	Proposal action.Proposal
}

// Error implements error.
func (e *DecidedError) Error() string {
	return fmt.Sprintf("proposal %s is %s already", e.Proposal.ID, e.Proposal.Status)
}

// UniqueKey returns the unique key of the execute_action job of a
// proposal, so a proposal is run by one job.
func UniqueKey(id uuid.UUID) string {
	return uniqueKeyPrefix + id.String()
}

// ExecutePayload is the payload of an execute_action job.
type ExecutePayload struct {
	ProposalID uuid.UUID `json:"proposal_id"`
}

// Approve approves a proposal that is waiting for a decision, and queues
// the job that runs it. It checks that the proposal has not expired and
// that the config still allows it. It returns the proposal as it is now.
//
// Of two people who decide at the same time, one wins and the other gets a
// *DecidedError that names the winner. It returns ErrTokenCannotApprove for
// an API token, and ErrNotFound, ErrExpired or ErrNotAllowed otherwise.
func (s *Service) Approve(ctx context.Context, id uuid.UUID, by Decider) (action.Proposal, error) {
	if by.Token {
		return action.Proposal{}, ErrTokenCannotApprove
	}

	return s.decide(ctx, id, by, action.StatusApproved, "")
}

// Reject rejects a proposal that is waiting for a decision. note, a reason
// the person gives, is redacted and kept in the audit entry only. Its
// results and errors are those of Approve, except that it does not check
// the config.
func (s *Service) Reject(ctx context.Context, id uuid.UUID, by Decider, note string) (action.Proposal, error) {
	return s.decide(ctx, id, by, action.StatusRejected, note)
}

// decide moves the proposal with id from proposed to status, which is
// approved or rejected, for the person by. It holds the proposal's row
// from the check to the change, so deciders go one after the other.
func (s *Service) decide(ctx context.Context, id uuid.UUID, by Decider, status action.ProposalStatus, note string) (action.Proposal, error) {
	if by.UserID == uuid.Nil && by.SlackUserID == "" {
		return action.Proposal{}, errNoDecider
	}

	var decided db.ActionProposal

	err := pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		var err error

		decided, err = s.decideIn(ctx, tx, id, by, status, note)
		if err != nil {
			return fmt.Errorf("in a transaction: %w", err)
		}

		return nil
	})

	switch {
	case errors.Is(err, errDecided):
		current, gerr := Get(ctx, s.opts.Pool, id)
		if gerr != nil {
			return action.Proposal{}, fmt.Errorf("read proposal %s after it was decided: %w", id, gerr)
		}

		return action.Proposal{}, &DecidedError{Proposal: current.Proposal}
	case err != nil:
		return action.Proposal{}, fmt.Errorf("%s proposal %s: %w", status, id, err)
	}

	s.opts.Metrics.reached(decided.Action, status)
	s.log.InfoContext(ctx, "action proposal decided", "proposal_id", id.String(), "investigation_id", decided.InvestigationID.String(), "status", string(status))

	current, err := Get(ctx, s.opts.Pool, id)
	if err != nil {
		return action.Proposal{}, fmt.Errorf("read proposal %s after the decision: %w", id, err)
	}

	return current.Proposal, nil
}

// decideIn is decide in tx.
func (s *Service) decideIn(ctx context.Context, tx pgx.Tx, id uuid.UUID, by Decider, status action.ProposalStatus, note string) (db.ActionProposal, error) {
	queries := db.New(tx)

	locked, err := queries.LockActionProposal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ActionProposal{}, ErrNotFound
	}

	if err != nil {
		return db.ActionProposal{}, fmt.Errorf("lock the proposal: %w", err)
	}

	row, now := locked.ActionProposal, s.now()

	switch {
	case row.Status == string(action.StatusExpired), row.Status == string(action.StatusProposed) && !now.Before(row.ExpiresAt):
		return db.ActionProposal{}, ErrExpired
	case row.Status != string(action.StatusProposed):
		return db.ActionProposal{}, errDecided
	}

	if status == action.StatusApproved {
		if reason := s.notAllowed(locked.Route, row.Action); reason != "" {
			return db.ActionProposal{}, &NotAllowedError{Reason: reason}
		}
	}

	kind, actorID := by.actor()

	var slackUser *string
	if by.UserID == uuid.Nil {
		slackUser = &by.SlackUserID
	}

	var userID *uuid.UUID
	if by.UserID != uuid.Nil {
		userID = &by.UserID
	}

	row, err = queries.DecideActionProposal(ctx, db.DecideActionProposalParams{
		Status: string(status), DecidedBy: userID, DecidedBySlackUser: slackUser, Now: now, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ActionProposal{}, ErrExpired
	}

	if err != nil {
		return db.ActionProposal{}, fmt.Errorf("decide: %w", err)
	}

	details := map[string]any{"status": row.Status, "action": row.Action}

	switch status {
	case action.StatusApproved:
		details["params"] = row.Params
	default:
		if note != "" {
			details["note"] = cut(s.redactReason(note), maxNoteChars)
		}
	}

	auditAction := AuditApproved
	if status == action.StatusRejected {
		auditAction = AuditRejected
	}

	if err := s.writeAudit(ctx, tx, row, auditAction, kind, actorID, details); err != nil {
		return db.ActionProposal{}, fmt.Errorf("write the audit entry: %w", err)
	}

	if status == action.StatusApproved {
		if err := enqueueExecute(ctx, tx, row.ID); err != nil {
			return db.ActionProposal{}, fmt.Errorf("queue the job that runs it: %w", err)
		}
	}

	if err := publish.Refresh(ctx, tx, row.InvestigationID); err != nil {
		return db.ActionProposal{}, fmt.Errorf("show the decision: %w", err)
	}

	return row, nil
}

// enqueueExecute queues the job that runs the proposal with id. A job for
// it that waits or runs already is enough.
func enqueueExecute(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	return enqueueExecuteAt(ctx, q, id, time.Time{})
}

// enqueueExecuteAt is enqueueExecute for a job that waits until runAt; the
// zero time means now.
func enqueueExecuteAt(ctx context.Context, q db.DBTX, id uuid.UUID, runAt time.Time) error {
	payload, err := json.Marshal(ExecutePayload{ProposalID: id})
	if err != nil {
		return fmt.Errorf("encode the execute_action payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:        queue.KindExecuteAction,
		Payload:     payload,
		UniqueKey:   UniqueKey(id),
		RunAt:       runAt,
		MaxAttempts: executeMaxAttempts,
	})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue the execute_action job of proposal %s: %w", id, err)
	}

	return nil
}

// writeAudit writes the audit entry act for proposal row in q, the
// transaction that changes it. details is the entry's new value.
func (s *Service) writeAudit(ctx context.Context, q db.DBTX, row db.ActionProposal, act, actorKind, actorID string, details any) error {
	err := s.audit.Write(ctx, q, audit.Entry{
		ActorKind:       actorKind,
		ActorID:         actorID,
		Action:          act,
		TargetKind:      auditTargetKind,
		TargetID:        row.ID.String(),
		InvestigationID: &row.InvestigationID,
		New:             details,
	})
	if err != nil {
		return fmt.Errorf("audit %s of proposal %s: %w", act, row.ID, err)
	}

	return nil
}
