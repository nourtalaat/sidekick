package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The audit actions of an undo. AuditUndoRequested has the undo proposal
// as its target, and AuditUndone the proposal that was undone.
const (
	AuditUndoRequested = "action.undo_requested"
	AuditUndone        = "action.undone"
)

// UndoTimeout is how long the request that asks for an undo waits for the
// action to restore the definition. A restore that takes longer goes on in
// the job that runs it.
const UndoTimeout = time.Minute

// undoRetryMargin is how long after UndoTimeout the job that backs up an
// undo first runs. The job runs only if the request did not finish the undo;
// waiting until the request has surely ended keeps the two from restoring at
// the same time.
const undoRetryMargin = 30 * time.Second

// uniqueViolation is the code Postgres gives a row that breaks a unique
// index.
const uniqueViolation = "23505"

var (
	// ErrTokenCannotUndo means an API token tried to undo. Like approving,
	// undoing is done by a person who is signed in or by a Slack user.
	ErrTokenCannotUndo = errors.New("an action is undone by a person, not by an API token")
	// ErrNotUndoable means the proposal cannot be undone. The error says why.
	ErrNotUndoable = errors.New("this proposal cannot be undone")
	// ErrUndoRunning means an undo of the proposal is approved or running.
	ErrUndoRunning = errors.New("an undo of this proposal is running already")
)

// NotUndoableError is the error of undoing a proposal that cannot be
// undone. It matches ErrNotUndoable with errors.Is, and Reason says why.
type NotUndoableError struct {
	Reason string
}

// Error implements error.
func (e *NotUndoableError) Error() string {
	return ErrNotUndoable.Error() + ": " + e.Reason
}

// Is makes the error match ErrNotUndoable.
func (e *NotUndoableError) Is(target error) bool {
	return target == ErrNotUndoable
}

// UndoConflictError means the definition was changed after Sidekick changed
// it, and force was not set, so nothing was restored. Changes is the
// difference between the definition now and the one a forced restore would
// write: the fields it would overwrite. Applying it again with force set
// restores the definition.
type UndoConflictError struct {
	Changes []action.Change
}

// Error implements error.
func (e *UndoConflictError) Error() string {
	return "the definition changed after the action changed it: " + action.ErrConflict.Error()
}

// UndoFailedError means the action could not restore the definition for
// good. Proposal is the undo proposal, which has ended as failed and says
// why. The proposal that was to be undone stays succeeded.
type UndoFailedError struct {
	Proposal action.Proposal
}

// Error implements error.
func (e *UndoFailedError) Error() string {
	return fmt.Sprintf("undo %s failed: %s", e.Proposal.ID, e.Proposal.Error)
}

// undoParams are the parameters of an undo proposal: what the undo runs
// with, shown to people as any parameters are.
type undoParams struct {
	Force  bool      `json:"force"`
	UndoOf uuid.UUID `json:"undoOf"`
}

// Undo undoes the succeeded proposal with id for the person by, in one
// step: it records a new proposal that restores the definition, approved by
// by and executing at once, asks the action to restore it, and returns the
// new proposal. The audit log gets action.undo_requested for the new
// proposal and, when the restore worked, action.succeeded for it and
// action.undone for the proposal that was undone.
//
// The action restores only the fields it changed. When someone changed one
// of them since, and force is false, nothing is restored and the error is a
// *UndoConflictError that shows what a forced restore would overwrite. A
// restore that fails for good ends the new proposal as failed and returns a
// *UndoFailedError. A restore that fails for a reason that may pass leaves
// the new proposal executing, and the job queued with it tries again once
// UndoTimeout and a margin have passed; the call returns it.
//
// It returns ErrTokenCannotUndo for an API token, ErrNotFound,
// ErrNotUndoable, ErrUndoRunning, or an ErrNotAllowed when the config no
// longer allows the action.
func (s *Service) Undo(ctx context.Context, id uuid.UUID, by Decider, force bool) (action.Proposal, error) {
	if by.Token {
		return action.Proposal{}, ErrTokenCannotUndo
	}

	if by.UserID == uuid.Nil && by.SlackUserID == "" {
		return action.Proposal{}, errNoDecider
	}

	undo, err := s.requestUndo(ctx, id, by, force)
	if err != nil {
		return action.Proposal{}, fmt.Errorf("undo proposal %s: %w", id, err)
	}

	s.opts.Metrics.reached(undo.Action, action.StatusExecuting)
	s.log.InfoContext(ctx, "action undo requested", "proposal_id", id.String(), "undo_id", undo.ID.String(), "investigation_id", undo.InvestigationID.String(), "force", force)

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), UndoTimeout)
	defer cancel()

	res, runErr := s.performUndo(runCtx, undo)

	switch {
	case runErr == nil:
		if err := s.succeed(runCtx, undo, res); err != nil {
			return action.Proposal{}, fmt.Errorf("undo proposal %s: %w", id, err)
		}
	case errors.Is(runErr, action.ErrConflict) && !force:
		changes := s.restoreChanges(ctx, undo, runErr)

		if err := s.fail(runCtx, undo, errUndoConflict); err != nil {
			return action.Proposal{}, fmt.Errorf("undo proposal %s: %w", id, err)
		}

		return action.Proposal{}, &UndoConflictError{Changes: changes}
	case isPermanent(runErr):
		if err := s.fail(runCtx, undo, s.errorText(runErr)); err != nil {
			return action.Proposal{}, fmt.Errorf("undo proposal %s: %w", id, err)
		}

		failed, gerr := Get(ctx, s.opts.Pool, undo.ID)
		if gerr != nil {
			return action.Proposal{}, fmt.Errorf("read undo proposal %s: %w", undo.ID, gerr)
		}

		return action.Proposal{}, &UndoFailedError{Proposal: failed.Proposal}
	default:
		s.keepAttemptError(runCtx, undo, runErr)
	}

	current, err := Get(ctx, s.opts.Pool, undo.ID)
	if err != nil {
		return action.Proposal{}, fmt.Errorf("read undo proposal %s: %w", undo.ID, err)
	}

	return current.Proposal, nil
}

// errUndoConflict is why an undo that found a changed definition ended as
// failed.
const errUndoConflict = "Not restored: the definition changed after Sidekick changed it. Undo again and confirm to restore it anyway."

// requestUndo checks that the proposal with id can be undone and, in one
// transaction, stores the proposal that undoes it, as approved by by and
// executing, with its audit entry, the job that is the safety net, and the
// update that shows it.
func (s *Service) requestUndo(ctx context.Context, id uuid.UUID, by Decider, force bool) (db.ActionProposal, error) {
	var undo db.ActionProposal

	err := pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		var err error

		undo, err = s.requestUndoIn(ctx, tx, id, by, force)
		if err != nil {
			return fmt.Errorf("in a transaction: %w", err)
		}

		return nil
	})
	if err != nil {
		return db.ActionProposal{}, fmt.Errorf("request the undo: %w", err)
	}

	return undo, nil
}

// requestUndoIn is requestUndo in tx.
func (s *Service) requestUndoIn(ctx context.Context, tx pgx.Tx, id uuid.UUID, by Decider, force bool) (db.ActionProposal, error) {
	queries := db.New(tx)

	locked, err := queries.LockActionProposal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ActionProposal{}, ErrNotFound
	}

	if err != nil {
		return db.ActionProposal{}, fmt.Errorf("lock the proposal: %w", err)
	}

	orig := locked.ActionProposal

	if reason := s.notUndoable(orig); reason != "" {
		return db.ActionProposal{}, &NotUndoableError{Reason: reason}
	}

	if reason := s.notAllowed(locked.Route, orig.Action); reason != "" {
		return db.ActionProposal{}, &NotAllowedError{Reason: reason}
	}

	if _, ok := s.opts.Actions[orig.Action].(action.Undoer); !ok {
		return db.ActionProposal{}, &NotUndoableError{Reason: fmt.Sprintf("action %q cannot undo changes", orig.Action)}
	}

	params, err := json.Marshal(undoParams{Force: force, UndoOf: id})
	if err != nil {
		return db.ActionProposal{}, fmt.Errorf("encode the parameters: %w", err)
	}

	snapshot, err := restoreSnapshot(orig)
	if err != nil {
		return db.ActionProposal{}, fmt.Errorf("make the snapshot of the undo: %w", err)
	}

	var userID *uuid.UUID
	if by.UserID != uuid.Nil {
		userID = &by.UserID
	}

	var slackUser *string
	if by.UserID == uuid.Nil {
		slackUser = &by.SlackUserID
	}

	undoID := ids.New()

	undo, err := queries.InsertUndoProposal(ctx, db.InsertUndoProposalParams{
		ID:                 undoID,
		InvestigationID:    orig.InvestigationID,
		Action:             orig.Action,
		Params:             params,
		Mode:               string(action.Approved),
		IdempotencyKey:     action.IdempotencyKey(undoID.String()),
		Now:                s.now(),
		DecidedBy:          userID,
		DecidedBySlackUser: slackUser,
		Snapshot:           snapshot,
		UndoOf:             id,
	})

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation {
		return db.ActionProposal{}, ErrUndoRunning
	}

	if err != nil {
		return db.ActionProposal{}, fmt.Errorf("insert the undo proposal: %w", err)
	}

	kind, actorID := by.actor()

	if err := s.writeAudit(ctx, tx, undo, AuditUndoRequested, kind, actorID, map[string]any{
		"status": undo.Status, "action": undo.Action, "undoOf": id, "force": force,
	}); err != nil {
		return db.ActionProposal{}, fmt.Errorf("write the audit entry: %w", err)
	}

	if err := enqueueExecuteAt(ctx, tx, undo.ID, s.now().Add(UndoTimeout+undoRetryMargin)); err != nil {
		return db.ActionProposal{}, fmt.Errorf("queue the job that backs it up: %w", err)
	}

	if err := publish.Refresh(ctx, tx, undo.InvestigationID); err != nil {
		return db.ActionProposal{}, fmt.Errorf("show the undo: %w", err)
	}

	return undo, nil
}

// notUndoable returns why the proposal cannot be undone, or "" when it can.
func (s *Service) notUndoable(orig db.ActionProposal) string {
	switch {
	case orig.UndoOf != nil:
		return "it is an undo itself"
	case orig.Status == string(action.StatusUndone):
		return "it was undone already"
	case orig.Status != string(action.StatusSucceeded):
		return "it is " + orig.Status + ", and only a succeeded proposal can be undone"
	case !orig.Undoable:
		return "its action cannot undo it"
	}

	snap, err := snapshotOf(orig)
	if err != nil {
		return "its snapshot cannot be read"
	}

	if snap != nil && snap.Deletes() {
		return "it removed the target, which cannot be restored"
	}

	return ""
}

// restoreSnapshot is the snapshot of the undo of orig: the same target,
// with the definition before the undo and the one after it swapped. It is
// nil for a proposal that has no snapshot.
func restoreSnapshot(orig db.ActionProposal) (json.RawMessage, error) {
	snap, err := snapshotOf(orig)
	if err != nil {
		return nil, fmt.Errorf("read the snapshot of proposal %s: %w", orig.ID, err)
	}

	if snap == nil {
		return nil, nil
	}

	raw, err := json.Marshal(action.Snapshot{Target: snap.Target, Before: snap.After, After: snap.Before})
	if err != nil {
		return nil, fmt.Errorf("encode the snapshot: %w", err)
	}

	return raw, nil
}

// performUndo asks the action to undo the proposal that the undo proposal
// row points at, with the snapshot the action stored when it changed the
// definition. It checks the config and the original's parameters again, as
// running a proposal does. The error it returns is the action's, wrapped,
// so isPermanent and errors.As work on it.
func (s *Service) performUndo(ctx context.Context, undo db.ActionProposal) (action.Result, error) {
	var p undoParams
	if err := json.Unmarshal(undo.Params, &p); err != nil {
		return action.Result{}, fmt.Errorf("decode the undo parameters: %w: %w", action.ErrPermanent, err)
	}

	orig, err := db.New(s.opts.Pool).GetActionProposal(ctx, p.UndoOf)
	if err != nil {
		return action.Result{}, fmt.Errorf("read proposal %s: %w", p.UndoOf, err)
	}

	snap, err := snapshotOf(orig)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, err)
	}

	a := s.opts.Actions[undo.Action]

	undoer, ok := a.(action.Undoer)
	if !ok {
		return action.Result{}, fmt.Errorf("action %q cannot undo changes: %w", undo.Action, action.ErrPermanent)
	}

	if err := a.Validate(ctx, orig.Params); err != nil {
		return action.Result{}, fmt.Errorf("the parameters of the proposal are not valid any more: %w", err)
	}

	var origSnap action.Snapshot
	if snap != nil {
		origSnap = *snap
	}

	res, err := undoer.Undo(ctx, action.Invocation{
		ProposalID:      undo.ID.String(),
		InvestigationID: undo.InvestigationID.String(),
		Params:          orig.Params,
		IdempotencyKey:  undo.IdempotencyKey,
		ApprovedBy:      approvedBy(undo),
		Snapshot:        snap,
	}, origSnap, p.Force)
	if err != nil {
		return action.Result{}, fmt.Errorf("undo: %w", err)
	}

	return res, nil
}

// restoreChanges is what a forced restore would overwrite: the difference
// between the live definition the conflict carries and the same definition
// with the fields the action changed put back. It is empty when the action
// gave no live definition.
func (s *Service) restoreChanges(ctx context.Context, undo db.ActionProposal, conflict error) []action.Change {
	live, ok := errors.AsType[*action.ConflictError](conflict)
	if !ok || len(live.Live) == 0 {
		return nil
	}

	changes, err := s.overwrites(ctx, undo, live.Live)
	if err != nil {
		s.log.WarnContext(ctx, "could not work out what a forced undo would overwrite", "proposal_id", undo.ID.String(), "error", err.Error())

		return nil
	}

	return changes
}

// overwrites works out the difference between live and live with the
// fields of the proposal that undo undoes put back.
func (s *Service) overwrites(ctx context.Context, undo db.ActionProposal, live json.RawMessage) ([]action.Change, error) {
	var p undoParams
	if err := json.Unmarshal(undo.Params, &p); err != nil {
		return nil, fmt.Errorf("decode the undo parameters: %w", err)
	}

	orig, err := db.New(s.opts.Pool).GetActionProposal(ctx, p.UndoOf)
	if err != nil {
		return nil, fmt.Errorf("read proposal %s: %w", p.UndoOf, err)
	}

	snap, err := snapshotOf(orig)
	if err != nil {
		return nil, fmt.Errorf("read the snapshot of proposal %s: %w", orig.ID, err)
	}

	if snap == nil {
		return nil, nil
	}

	deltas, err := action.Diff(snap.Before, snap.After)
	if err != nil {
		return nil, fmt.Errorf("compare the snapshot: %w", err)
	}

	restored, err := action.Revert(live, deltas)
	if err != nil {
		return nil, fmt.Errorf("put the fields back: %w", err)
	}

	diff, err := action.Diff(live, restored)
	if err != nil {
		return nil, fmt.Errorf("compare with the live definition: %w", err)
	}

	return action.Display(diff), nil
}

// keepAttemptError keeps the error of an attempt that will be tried again.
func (s *Service) keepAttemptError(ctx context.Context, row db.ActionProposal, err error) {
	if rerr := db.New(s.opts.Pool).RecordActionAttemptError(ctx, db.RecordActionAttemptErrorParams{ID: row.ID, Error: new(s.errorText(err))}); rerr != nil {
		s.log.WarnContext(ctx, "could not keep the error of a failed attempt", "proposal_id", row.ID.String(), "error", rerr.Error())
	}

	s.log.WarnContext(ctx, "action attempt failed; it is tried again", "proposal_id", row.ID.String(), "action", row.Action)
}

// lockOriginal locks the proposal that the proposal with id restores, when
// it is an undo, and does nothing for any other. Ending an undo changes the
// undo and then the original; a person asking to undo the same proposal
// again holds the original and then reads the undo. Taking the original's
// lock first in both keeps them from waiting on each other.
func (s *Service) lockOriginal(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	queries := db.New(tx)

	row, err := queries.GetActionProposal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read proposal %s: %w", id, err)
	}

	if row.UndoOf == nil {
		return nil
	}

	if _, err := queries.LockActionProposal(ctx, *row.UndoOf); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock proposal %s: %w", row.UndoOf, err)
	}

	return nil
}

// markUndone moves the proposal that the undo row restored to undone and
// writes action.undone, in tx, the transaction that ends the undo as
// succeeded. A proposal that is not succeeded any more stays as it is. It
// reports whether it moved the proposal.
func (s *Service) markUndone(ctx context.Context, tx pgx.Tx, undo db.ActionProposal) (bool, error) {
	orig, err := db.New(tx).MarkActionUndone(ctx, *undo.UndoOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("mark proposal %s undone: %w", undo.UndoOf, err)
	}

	kind, actorID := audit.ActorUser, ""

	switch {
	case undo.DecidedBy != nil:
		actorID = undo.DecidedBy.String()
	case undo.DecidedBySlackUser != nil:
		actorID = *undo.DecidedBySlackUser
	}

	if err := s.writeAudit(ctx, tx, orig, AuditUndone, kind, actorID, map[string]any{
		"status": orig.Status, "action": orig.Action, "undoneBy": undo.ID,
	}); err != nil {
		return false, fmt.Errorf("write the audit entry: %w", err)
	}

	return true, nil
}
