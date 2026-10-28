// Package feedback stores what responders say about investigations, and
// keeps each investigation's confirmed verdict in step with it.
//
// One click on Correct or Wrong under a verdict stores a rating with
// target kind verdict. Each Slack user has one rating per target: clicking
// the same button again changes nothing, and clicking the other one
// changes the rating. A Correct confirms the verdict, so the investigation
// counts as confirmed; later feedback that disagrees with the confirmed
// verdict changes it. Each change of the confirmation also changes the
// investigation's memory entry in the same transaction, so only confirmed
// investigations are in memory.
package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// TargetVerdict is the target kind of a rating of the verdict.
const TargetVerdict = "verdict"

// The audit log names of a change to a confirmed verdict: the action is
// auditActionPrefix and the Change, such as verdict.withdrawn.
const (
	auditTarget       = "investigation"
	auditActionPrefix = "verdict."
)

// auditor writes the audit rows of changes to confirmed verdicts.
var auditor = audit.NewWriter()

// statusFinal is the status of an investigation that ended with a report
// people can rate. It is investigator.StatusFinal; this package does not
// import the investigator.
const statusFinal = "final"

var (
	// ErrNotFound is returned for an investigation that does not exist.
	ErrNotFound = errors.New("the investigation does not exist")
	// ErrNotRatable is returned for an investigation that has no verdict
	// to rate: it is still running, could not finish, or was stopped.
	ErrNotRatable = errors.New("the investigation has no verdict to rate")
	// errBadRating is returned for a rating other than correct or wrong.
	errBadRating = errors.New("the rating must be correct or wrong")
	// errNoUser is returned for a rating without a Slack user.
	errNoUser = errors.New("the Slack user is required")
)

// VerdictRating is one Slack user's rating of an investigation's verdict.
type VerdictRating struct {
	InvestigationID uuid.UUID
	Rating          output.Rating
	SlackUserID     string
	// Memory says how the investigation's memory entry is synced when the
	// rating changes the confirmation; set Embed with vector memory on.
	Memory memory.SyncOptions
}

// Result is what a rating changed.
type Result struct {
	// Stored is false for a rating the user had given already, which
	// changes nothing.
	Stored bool
	// Confirmation says how the confirmed verdict changed.
	Confirmation Change
}

// RateVerdict stores r in q's transaction and updates the confirmed
// verdict and the investigation's memory entry, holding a lock on the
// investigation's row so two clicks at once are applied in turn. Call it
// in a transaction, and show the change in the outputs in the same one,
// for example with publish.Refresh. It returns an error that wraps ErrNotFound or ErrNotRatable when the
// investigation has no verdict to rate.
func RateVerdict(ctx context.Context, q db.DBTX, r VerdictRating) (Result, error) {
	if r.Rating != output.RatingCorrect && r.Rating != output.RatingWrong {
		return Result{}, errBadRating
	}

	if r.SlackUserID == "" {
		return Result{}, errNoUser
	}

	queries := db.New(q)

	inv, err := queries.LockInvestigation(ctx, r.InvestigationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, fmt.Errorf("investigation %s: %w", r.InvestigationID, ErrNotFound)
	}

	if err != nil {
		return Result{}, fmt.Errorf("lock investigation %s: %w", r.InvestigationID, err)
	}

	if inv.Status != statusFinal || inv.Verdict == nil {
		return Result{}, fmt.Errorf("investigation %s is %s: %w", r.InvestigationID, inv.Status, ErrNotRatable)
	}

	_, err = queries.UpsertSlackFeedback(ctx, db.UpsertSlackFeedbackParams{
		ID:              ids.New(),
		InvestigationID: inv.ID,
		TargetKind:      TargetVerdict,
		TargetRef:       VerdictRef(inv.Run),
		Rating:          string(r.Rating),
		SlackUserID:     r.SlackUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, nil
	}

	if err != nil {
		return Result{}, fmt.Errorf("store the rating of investigation %s: %w", inv.ID, err)
	}

	change, err := confirm(ctx, q, inv, r)
	if err != nil {
		return Result{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
	}

	if change != Unchanged {
		if err := memory.Sync(ctx, q, inv.ID, r.Memory); err != nil {
			return Result{}, fmt.Errorf("investigation %s: %w", inv.ID, err)
		}
	}

	return Result{Stored: true, Confirmation: change}, nil
}

// Verdicts returns the ratings of the current verdict of inv, oldest change
// first, as the outputs show them.
func Verdicts(ctx context.Context, q db.DBTX, inv db.Investigation) ([]output.Feedback, error) {
	rows, err := db.New(q).ListTargetFeedback(ctx, db.ListTargetFeedbackParams{
		InvestigationID: inv.ID,
		TargetKind:      TargetVerdict,
		TargetRef:       VerdictRef(inv.Run),
	})
	if err != nil {
		return nil, fmt.Errorf("list the ratings of investigation %s: %w", inv.ID, err)
	}

	out := make([]output.Feedback, 0, len(rows))
	for _, row := range rows {
		out = append(out, output.Feedback{Rating: output.Rating(row.Rating), By: output.Person{SlackUserID: deref(row.SlackUserID)}})
	}

	return out, nil
}

// VerdictRef is the target ref of the verdict of run, such as "run:1/verdict".
func VerdictRef(run int32) string {
	return fmt.Sprintf("run:%d/%s", run, TargetVerdict)
}

// confirm applies the confirmation that r leads to, through q, and
// returns how it changed. A change to a verdict confirmed before is
// written to the audit log in the same transaction.
func confirm(ctx context.Context, q db.DBTX, inv db.Investigation, r VerdictRating) (Change, error) {
	queries := db.New(q)
	before := current(inv)
	next, change := nextConfirmation(before, r.Rating, *inv.Verdict, causeOf(inv.Report), r.SlackUserID, time.Now())

	switch change {
	case Unchanged:
		return Unchanged, nil
	case Revised, Withdrawn:
		if err := setConfirmation(ctx, queries, inv.ID, next); err != nil {
			return Unchanged, fmt.Errorf("change the confirmed verdict: %w", err)
		}

		err := auditor.Write(ctx, q, audit.Entry{
			ActorKind:       audit.ActorUser,
			ActorID:         r.SlackUserID,
			Action:          auditActionPrefix + string(change),
			TargetKind:      auditTarget,
			TargetID:        inv.ID.String(),
			InvestigationID: &inv.ID,
			Old:             before.view(),
			New:             next.view(),
		})
		if err != nil {
			return Unchanged, fmt.Errorf("audit the change of the confirmed verdict: %w", err)
		}
	default:
		if err := setConfirmation(ctx, queries, inv.ID, next); err != nil {
			return Unchanged, fmt.Errorf("confirm the verdict: %w", err)
		}
	}

	return change, nil
}

// setConfirmation stores c as the confirmed verdict of the investigation
// with id.
func setConfirmation(ctx context.Context, queries *db.Queries, id uuid.UUID, c confirmation) error {
	if err := queries.SetInvestigationConfirmation(ctx, db.SetInvestigationConfirmationParams{
		ConfirmedVerdict:     c.verdict,
		ConfirmedCause:       c.cause,
		ConfirmedBy:          c.by,
		ConfirmedBySlackUser: c.bySlackUser,
		ConfirmedAt:          c.at,
		ID:                   id,
	}); err != nil {
		return fmt.Errorf("store the confirmation: %w", err)
	}

	return nil
}

// causeOf returns the cause in a stored report, or nil.
func causeOf(raw json.RawMessage) *string {
	var r report.Report
	if err := json.Unmarshal(raw, &r); err != nil || r.Cause == "" {
		return nil
	}

	return &r.Cause
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}
