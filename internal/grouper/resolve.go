package grouper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

const (
	// ActionResolved is the action of the audit row for a group a person
	// resolved.
	ActionResolved = "group.resolved"
	// resolveTarget is the target kind of that row: the group's
	// investigation, or the group when it has none.
	resolveTarget = "investigation"
)

var (
	// ErrGroupNotFound means the group to resolve does not exist.
	ErrGroupNotFound = errors.New("the group does not exist")
	// ErrGroupClosed means the group closed for a reason other than being
	// resolved, such as the end of its window, so it cannot be resolved.
	ErrGroupClosed = errors.New("the group is closed already")
)

// resolveAuditor writes the audit rows of resolved groups.
var resolveAuditor = audit.NewWriter()

// Actor is who resolved a group, as the audit log names them.
type Actor struct {
	// Kind is audit.ActorUser or audit.ActorToken.
	Kind string
	// ID is the user's or the API token's ID.
	ID string
}

// Resolve marks the open group with groupID as resolved because a person
// said so, with the code a recovery uses: its running watchers end (alert
// watchers recover, the others stop with group_resolved, each change
// recorded with rec), the group closes with ClosedResolved, and the audit
// row is written, all in tx. It reports true when it resolved the group,
// and false, changing nothing, for a group that was resolved already.
// A group closed for another reason returns ErrGroupClosed, and a group
// that does not exist ErrGroupNotFound.
func Resolve(ctx context.Context, tx pgx.Tx, groupID uuid.UUID, by Actor, now time.Time, rec *watch.Recorder) (bool, error) {
	grp, err := db.New(tx).LockEventGroup(ctx, groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrGroupNotFound
	}

	if err != nil {
		return false, fmt.Errorf("lock group %s: %w", groupID, err)
	}

	if grp.Status != statusOpen {
		if grp.ClosedReason != nil && *grp.ClosedReason == ClosedResolved {
			return false, nil
		}

		return false, ErrGroupClosed
	}

	if err := endResolved(ctx, slog.New(slog.DiscardHandler), tx, groupID, now, rec); err != nil {
		return false, fmt.Errorf("group %s: %w", groupID, err)
	}

	targetID := groupID
	if grp.InvestigationID != nil {
		targetID = *grp.InvestigationID
	}

	err = resolveAuditor.Write(ctx, tx, audit.Entry{
		ActorKind:       by.Kind,
		ActorID:         by.ID,
		Action:          ActionResolved,
		TargetKind:      resolveTarget,
		TargetID:        targetID.String(),
		InvestigationID: grp.InvestigationID,
		Old:             map[string]any{"status": grp.Status},
		New:             map[string]any{"status": "closed", "closedReason": ClosedResolved},
	})
	if err != nil {
		return false, fmt.Errorf("audit the resolution of group %s: %w", groupID, err)
	}

	return true, nil
}

// endResolved ends the running watchers of the group with id and closes
// it as resolved, in tx.
func endResolved(ctx context.Context, log *slog.Logger, tx pgx.Tx, id uuid.UUID, now time.Time, rec *watch.Recorder) error {
	if _, err := watch.EndGroupResolved(ctx, tx, id, now, rec); err != nil {
		return fmt.Errorf("end the watchers: %w", err)
	}

	if err := closeGroup(ctx, log, db.New(tx), id, ClosedResolved); err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	return nil
}
