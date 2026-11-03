package argocdrollback

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/argocd"
)

// move is one rollback request and what the history must look like before and
// after it. An Execute and an Undo are both a move: Execute goes from the
// deployment the snapshot saw to an earlier one, and Undo goes from the
// deployment that rollback made to the one that ran before it.
type move struct {
	ref argocd.AppRef
	// fromID and fromShort are the history ID and the short revision of the
	// newest entry before the move.
	fromID    int64
	fromShort string
	// toID is the history entry the rollback call names, and toShort its short
	// revision. Argo CD adds a new entry, with the next ID and that revision.
	toID    int64
	toShort string
	// undo says that the move is an Undo, which reads and fails differently.
	undo bool
	// force lets an Undo go on when the newest entry is not fromID.
	force bool
}

// moveFromSnapshot reads the move of an Execute from the snapshot it carries.
// A snapshot that is missing or is not a rollback's of this application fails
// permanently.
func moveFromSnapshot(snap *action.Snapshot, ref argocd.AppRef) (move, error) {
	if snap == nil || !snap.Revision {
		return move{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if snap.Target != targetName(ref) {
		return move{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	revs, err := snap.Revisions()
	if err != nil {
		return move{}, fmt.Errorf("%w: read the revisions: %w", action.ErrPermanent, err)
	}

	fromID, fromRevision, err := splitVersion(snap.Version)
	if err != nil {
		return move{}, fmt.Errorf("%w: %w", action.ErrPermanent, err)
	}

	toID, err := strconv.ParseInt(revs.GoesBackTo.ID, 10, 64)
	if err != nil {
		return move{}, fmt.Errorf("%w: the history ID to go back to: %w", action.ErrPermanent, err)
	}

	return move{
		ref:       ref,
		fromID:    fromID,
		fromShort: shortRevision(fromRevision),
		toID:      toID,
		toShort:   revs.GoesBackTo.Version,
	}, nil
}

// undoFromSnapshot reads the move of an Undo from the snapshot of the rollback
// it undoes. The rollback went from the Running entry, with ID r, to an
// earlier one, and Argo CD recorded it as the entry r+1. The Undo goes from
// that entry back to r.
func undoFromSnapshot(snap action.Snapshot, ref argocd.AppRef, force bool) (move, error) {
	if !snap.Revision {
		return move{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	if snap.Target != targetName(ref) {
		return move{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	revs, err := snap.Revisions()
	if err != nil {
		return move{}, fmt.Errorf("%w: read the revisions: %w", action.ErrPermanent, err)
	}

	ranBefore, err := strconv.ParseInt(revs.Running.ID, 10, 64)
	if err != nil {
		return move{}, fmt.Errorf("%w: the history ID that ran before: %w", action.ErrPermanent, err)
	}

	return move{
		ref:       ref,
		fromID:    ranBefore + 1,
		fromShort: revs.GoesBackTo.Version,
		toID:      ranBefore,
		toShort:   revs.Running.Version,
		undo:      true,
		force:     force,
	}, nil
}

// run makes the move: it finds out whether it is made already, or under way,
// and otherwise checks the application and asks Argo CD for the rollback. It
// then waits for the operation to end.
func (a *Rollback) run(ctx context.Context, m move) (action.Result, error) {
	app, err := a.client.GetApplication(ctx, m.ref)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the application: %w", classify(err))
	}

	switch {
	case m.made(app):
		return a.result(m, app), nil
	case m.underWay(app):
		// An earlier attempt asked for it, and the operation has not ended.
	default:
		if err := a.start(ctx, m, app); err != nil {
			return action.Result{}, fmt.Errorf("start the rollback: %w", err)
		}
	}

	return a.wait(ctx, m)
}

// start checks the application and asks Argo CD for the rollback.
func (a *Rollback) start(ctx context.Context, m move, app *argocd.Application) error {
	if !m.atStart(app) && !m.force {
		return m.conflict(app)
	}

	if err := checkSupported(app, m.ref); err != nil {
		return fmt.Errorf("check the application: %w", err)
	}

	if app.OperationRunning() {
		return fmt.Errorf("an operation is running on application %s: %w", m.ref, action.ErrConflict)
	}

	if _, ok := entryByID(app, m.toID); !ok {
		return fmt.Errorf("%w: history entry %d of application %s is gone: Argo CD keeps only the last %d deployments (revisionHistoryLimit)",
			action.ErrPermanent, m.toID, m.ref, app.HistoryLimit())
	}

	if _, err := a.client.Rollback(ctx, m.ref, m.toID); err != nil {
		return fmt.Errorf("ask Argo CD for the rollback: %w", classify(err))
	}

	return nil
}

// wait reads the application until the operation of the move has ended. A
// rollback that Argo CD ends well is the result. One it ends as failed or
// with an error is a permanent failure. An operation that has not ended in
// time is a failure that is not permanent, so the next attempt waits for it
// again.
func (a *Rollback) wait(ctx context.Context, m move) (action.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, a.waitFor)
	defer cancel()

	phase := "Pending"

	for {
		app, err := a.client.GetApplication(ctx, m.ref)
		if err != nil && ctx.Err() == nil {
			return action.Result{}, fmt.Errorf("read the application: %w", classify(err))
		}

		if err == nil {
			if state := m.ended(app); state != nil {
				return a.outcome(m, app, state)
			}

			phase = operationPhase(app)
		}

		select {
		case <-ctx.Done():
			return action.Result{}, fmt.Errorf("the rollback of %s had not ended in Argo CD after %s (phase %s); it may still end, "+
				"and the next attempt waits for it", m.ref, a.waitFor, phase)
		case <-time.After(a.pollEvery):
		}
	}
}

// outcome is the result of an operation that has ended.
func (a *Rollback) outcome(m move, app *argocd.Application, state *argocd.OperationState) (action.Result, error) {
	if !state.Succeeded() {
		return action.Result{}, fmt.Errorf("%w: the rollback of %s ended with phase %s in Argo CD: %s",
			action.ErrPermanent, m.ref, state.Phase, state.Message)
	}

	return a.result(m, app), nil
}

// result is the result of a move that is made, with a link to the
// application.
func (a *Rollback) result(m move, app *argocd.Application) action.Result {
	summary := fmt.Sprintf("Rolled back application %s from %s to %s.", m.ref, m.fromShort, m.toShort)
	if m.undo {
		summary = fmt.Sprintf("Undid the rollback of application %s: it runs %s again.", m.ref, m.toShort)
	}

	if newest := app.HistoryNewestFirst(); len(newest) > 0 {
		summary += fmt.Sprintf(" Argo CD recorded it as deployment %d.", newest[0].ID)
	}

	return action.Result{
		Summary: summary,
		Links:   []action.Link{{Title: "Application " + m.ref.String(), URL: a.client.ApplicationURL(m.ref)}},
	}
}

// made reports whether the move was made: no operation runs, the newest entry
// is the one after fromID and has the revision the move goes to, and the
// entry before it is the one the move started from.
func (m move) made(app *argocd.Application) bool {
	history := app.HistoryNewestFirst()
	if app.OperationRunning() || len(history) < 2 {
		return false
	}

	newest, before := history[0], history[1]

	return newest.ID == m.fromID+1 && shortRevision(newest.RevisionText()) == m.toShort &&
		before.ID == m.fromID && shortRevision(before.RevisionText()) == m.fromShort
}

// underWay reports whether an operation that goes to the revision of the move
// runs on the application, which has not recorded it yet. It is the one an
// earlier attempt asked for.
func (m move) underWay(app *argocd.Application) bool {
	pending := app.PendingRevision()

	return pending != "" && app.OperationRunning() && m.atStart(app) && shortRevision(pending) == m.toShort
}

// atStart reports whether the newest history entry is the one the move
// starts from.
func (m move) atStart(app *argocd.Application) bool {
	history := app.HistoryNewestFirst()

	return len(history) > 0 && history[0].ID == m.fromID && shortRevision(history[0].RevisionText()) == m.fromShort
}

// ended returns the state of the operation of the move when it has ended, and
// nil otherwise. The state must be of an operation that goes to the revision
// of the move, so a state that an earlier operation left is not taken for it.
func (m move) ended(app *argocd.Application) *argocd.OperationState {
	state := app.Status.OperationState
	if app.OperationRunning() || state == nil || !state.Completed() || state.Operation.Sync == nil {
		return nil
	}

	if shortRevision(state.Operation.Sync.Revision) != m.toShort {
		return nil
	}

	return state
}

// conflict is the error for a newest history entry that is not the one the
// move starts from. An Execute gets one that wraps action.ErrConflict. An
// Undo gets an *action.ConflictError that holds the revision the application
// runs now.
func (m move) conflict(app *argocd.Application) error {
	history := app.HistoryNewestFirst()

	if !m.undo || len(history) == 0 {
		return fmt.Errorf("application %s was deployed again since the proposal: %w", m.ref, action.ErrConflict)
	}

	snap, err := action.RevisionSnapshot(targetName(m.ref), "", revisionOf(history[0], imagesNote(app)), revisionOf(history[0], ""))
	if err != nil {
		return fmt.Errorf("write the revision that runs now: %w", err)
	}

	return &action.ConflictError{Live: snap.Before}
}

// entryByID returns the history entry with the ID.
func entryByID(app *argocd.Application, id int64) (argocd.HistoryEntry, bool) {
	for _, e := range app.Status.History {
		if e.ID == id {
			return e, true
		}
	}

	return argocd.HistoryEntry{}, false
}

// classify says whether the error of a call to Argo CD can pass. An answer
// that says another operation runs is a conflict. A 4xx answer, other than
// the ones that ask to try later, is permanent, because the same call gets the
// same answer. Anything else, such as a server error or a network error, is
// returned so that the job tries again.
func classify(err error) error {
	se, ok := errors.AsType[*argocd.StatusError](err)

	switch {
	case !ok:
		return fmt.Errorf("call Argo CD: %w", err)
	case strings.Contains(se.Message, anotherOperation):
		return fmt.Errorf("%w: %w", action.ErrConflict, err)
	case se.Code >= http.StatusBadRequest && se.Code < http.StatusInternalServerError &&
		se.Code != http.StatusRequestTimeout && se.Code != http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", action.ErrPermanent, err)
	default:
		return fmt.Errorf("call Argo CD: %w", err)
	}
}
