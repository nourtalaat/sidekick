package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/feedback"
)

// detailNoFix is what a refused fix mark says.
const detailNoFix = "Only an investigation that ended as a false positive has a fix to mark as applied."

// MarkFixApplied records that a responder applied the fix of a false
// positive. The audit row is written in the same transaction.
func (s *server) MarkFixApplied(ctx context.Context, req apigen.MarkFixAppliedRequestObject) (apigen.MarkFixAppliedResponseObject, error) {
	mark, err := s.setFixApplied(ctx, req.Id, true)
	if err != nil {
		return nil, err
	}

	return apigen.MarkFixApplied200JSONResponse(fixAppliedOf(mark)), nil
}

// UnmarkFixApplied takes back the mark that the fix was applied.
func (s *server) UnmarkFixApplied(ctx context.Context, req apigen.UnmarkFixAppliedRequestObject) (apigen.UnmarkFixAppliedResponseObject, error) {
	mark, err := s.setFixApplied(ctx, req.Id, false)
	if err != nil {
		return nil, err
	}

	return apigen.UnmarkFixApplied200JSONResponse(fixAppliedOf(mark)), nil
}

// setFixApplied sets or clears the mark of the investigation for the
// caller, in one transaction.
func (s *server) setFixApplied(ctx context.Context, id apigen.InvestigationID, applied bool) (feedback.FixMark, error) {
	p, _ := auth.PrincipalFrom(ctx)

	var mark feedback.FixMark

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error

		mark, err = feedback.SetFixApplied(ctx, tx, id, applied, feedback.Marker{UserID: p.ID, Token: p.Kind == auth.KindToken})
		if err != nil {
			return fmt.Errorf("set the mark: %w", err)
		}

		return nil
	})

	switch {
	case errors.Is(err, feedback.ErrNotFound):
		return feedback.FixMark{}, errNoInvestigation
	case errors.Is(err, feedback.ErrNoFix):
		return feedback.FixMark{}, problemf(http.StatusConflict, detailNoFix)
	case err != nil:
		return feedback.FixMark{}, fmt.Errorf("mark the fix of investigation %s: %w", id, err)
	}

	return mark, nil
}

// fixAppliedOf is the API's view of the mark.
func fixAppliedOf(mark feedback.FixMark) apigen.FixApplied {
	return apigen.FixApplied{Applied: mark.Applied, Changed: mark.Changed, AppliedAt: mark.At}
}
