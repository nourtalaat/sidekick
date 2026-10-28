package grouper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// skipReasonNoGroup is why a recovery event whose alert is in no group is
// skipped.
const skipReasonNoGroup = "no group has this alert"

// Recover links the recovery event ev, which is still received, to the
// group its alert is in, inside a grouper.group span: the group of the
// newest grouped event with the same stored fingerprint, open or closed.
// The event goes to the inbox of the group's investigation as evidence; it
// never stops the investigation. When the group is open and every alert in
// it has now recovered, a close_group job is enqueued to close it as
// resolved.
//
// It reports false when no group has the alert, or the group is gone
// because the retention job deleted it, and the event is then stored as
// skipped, or when another run linked or skipped the event
// first.
func (g *Grouper) Recover(ctx context.Context, tx pgx.Tx, ev db.Event) (res Result, linked bool, err error) {
	ctx, span := g.tracer.Start(ctx, SpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "linking the recovery failed")
		}

		span.End()
	}()

	queries := db.New(tx)

	grp, found, err := lockGroupOfAlert(ctx, queries, ev.Fingerprint)
	if err != nil {
		return Result{}, false, fmt.Errorf("recovery event %s: %w", ev.ID, err)
	}

	if !found {
		if _, err := queries.MarkEventSkipped(ctx, db.MarkEventSkippedParams{SkipReason: skipReasonNoGroup, ID: ev.ID}); err != nil {
			return Result{}, false, fmt.Errorf("skip recovery event %s: %w", ev.ID, err)
		}

		return Result{}, false, nil
	}

	span.SetAttributes(attribute.String(attrRoute, grp.Route), attribute.String(attrGroupID, grp.ID.String()))

	marked, err := queries.MarkEventGrouped(ctx, db.MarkEventGroupedParams{GroupID: &grp.ID, ID: ev.ID})
	if err != nil {
		return Result{}, false, fmt.Errorf("link recovery event %s to group %s: %w", ev.ID, grp.ID, err)
	}

	if marked == 0 {
		return Result{}, false, nil
	}

	res = Result{GroupID: grp.ID}

	if grp.InvestigationID != nil {
		res.InvestigationID = *grp.InvestigationID

		if err := investigator.AddEventToInbox(ctx, tx, res.InvestigationID, investigator.InboxResolved, ev.ID); err != nil {
			return Result{}, false, fmt.Errorf("tell the investigation: %w", err)
		}
	}

	if err := g.closeIfRecovered(ctx, tx, grp); err != nil {
		return Result{}, false, fmt.Errorf("group %s: %w", grp.ID, err)
	}

	return res, true, nil
}

// closeIfRecovered enqueues the close_group job that closes grp as
// resolved, when grp is open and every alert in it has recovered.
func (g *Grouper) closeIfRecovered(ctx context.Context, tx pgx.Tx, grp db.EventGroup) error {
	if grp.Status != statusOpen {
		return nil
	}

	recovered, err := db.New(tx).AllAlertsRecovered(ctx, grp.ID)
	if err != nil {
		return fmt.Errorf("check the alerts: %w", err)
	}

	if !recovered {
		return nil
	}

	if err := enqueueClose(ctx, tx, grp.ID, time.Time{}, resolvedKey(grp.ID)); err != nil {
		return fmt.Errorf("close it as resolved: %w", err)
	}

	g.log.DebugContext(ctx, "every alert in the group recovered", "group_id", grp.ID.String())

	return nil
}

// lockGroupOfAlert finds and locks the group of the newest grouped event
// with fingerprint. It reports false when no event of the alert is
// grouped, or when the group that event names was deleted: events have
// no foreign key to their group, so an event can outlive it.
func lockGroupOfAlert(ctx context.Context, queries *db.Queries, fingerprint string) (db.EventGroup, bool, error) {
	groupID, err := queries.FindGroupOfAlert(ctx, fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.EventGroup{}, false, nil
	}

	if err != nil {
		return db.EventGroup{}, false, fmt.Errorf("find the group of the alert: %w", err)
	}

	grp, err := queries.LockEventGroup(ctx, groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.EventGroup{}, false, nil
	}

	if err != nil {
		return db.EventGroup{}, false, fmt.Errorf("lock group %s: %w", groupID, err)
	}

	return grp, true, nil
}
