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
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

// confirmationUnchanged is the confirmation field of feedback that did not
// change the confirmed verdict.
const confirmationUnchanged = "unchanged"

// CreateInvestigationFeedback stores a signed-in user's feedback on one
// part of an investigation, and confirms the investigation when it gives
// the real verdict, in one transaction.
func (s *server) CreateInvestigationFeedback(ctx context.Context, req apigen.CreateInvestigationFeedbackRequestObject) (apigen.CreateInvestigationFeedbackResponseObject, error) {
	p, _ := auth.PrincipalFrom(ctx)
	if p.Kind != auth.KindUser {
		return nil, problemf(http.StatusForbidden, "Feedback needs a signed-in user, not an API token.")
	}

	body := req.Body
	sub := feedback.Submission{
		InvestigationID: req.Id,
		Target:          feedback.Target{Kind: string(body.TargetKind), Ref: body.TargetRef},
		Rating:          output.Rating(body.Rating),
		Reason:          textOf(body.Reason),
		CorrectVerdict:  report.Verdict(textOf((*string)(body.CorrectVerdict))),
		CorrectCause:    textOf(body.CorrectCause),
		Author:          feedback.Author{UserID: p.ID},
		Memory:          s.memory,
	}

	var got feedback.Submitted

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error

		got, err = feedback.Submit(ctx, tx, sub)
		if err != nil {
			return fmt.Errorf("submit: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, feedbackProblem(req.Id.String(), err)
	}

	if got.RatingChanged {
		s.feedbackMetrics.Stored(got.Feedback.TargetKind, sub.Rating)
	}

	return apigen.CreateInvestigationFeedback201JSONResponse(feedbackOf(got)), nil
}

// feedbackProblem turns an error of feedback.Submit into the problem the
// caller sees; other errors stay errors, which answer 500.
func feedbackProblem(id string, err error) error {
	if invalid, ok := errors.AsType[*feedback.InvalidError](err); ok {
		return problemf(http.StatusBadRequest, "%s.", invalid.Detail)
	}

	switch {
	case errors.Is(err, feedback.ErrNotFound):
		return errNoInvestigation
	case errors.Is(err, feedback.ErrNotRatable):
		return problemf(http.StatusConflict, "The investigation has no verdict yet; only its tool calls can be rated.")
	case errors.Is(err, feedback.ErrBadTarget):
		return problemf(http.StatusUnprocessableEntity, "This part is not in the investigation's current report or steps.")
	case errors.Is(err, feedback.ErrBadVerdict):
		return problemf(http.StatusUnprocessableEntity, "The real verdict is not a verdict, or it contradicts the rating of the verdict.")
	}

	return fmt.Errorf("feedback on investigation %s: %w", id, err)
}

// feedbackOf is the API's view of submitted feedback.
func feedbackOf(got feedback.Submitted) apigen.Feedback {
	row := got.Feedback

	out := apigen.Feedback{
		Id: row.ID, InvestigationId: row.InvestigationID,
		TargetKind: apigen.FeedbackTargetKind(row.TargetKind), TargetRef: row.TargetRef,
		Rating: apigen.FeedbackRating(row.Rating), Reason: row.Reason, CorrectCause: row.CorrectCause,
		Channel:      apigen.FeedbackChannel(row.Channel),
		Confirmation: apigen.FeedbackConfirmation(confirmationUnchanged),
		CreatedAt:    row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}

	if got.Confirmation != feedback.Unchanged {
		out.Confirmation = apigen.FeedbackConfirmation(got.Confirmation)
	}

	if row.CorrectVerdict != nil {
		out.CorrectVerdict = new(apigen.Verdict(*row.CorrectVerdict))
	}

	return out
}

// textOf returns the text s points to, or "" for nil.
func textOf(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
