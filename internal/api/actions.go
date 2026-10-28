package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// maxQuestionBytes is the longest follow-up question, in bytes.
const maxQuestionBytes = 4000

// What a refused action says.
const (
	detailTriageOnly = "This was a triage-only result, so there is no investigation to ask about yet. " +
		"Start a full investigation with Investigate anyway in Slack, then ask again."
	detailFailed      = "The investigation failed, so it takes no follow-up questions."
	detailGroupClosed = "The group of this investigation closed when its window ended, so it cannot be marked resolved."
	detailNoWatcher   = "No watcher has this ID."
	detailNoStopper   = "Stopping watchers is not set up on this server."
	detailEmptyAsk    = "text must not be empty."
	detailLongAsk     = "text must be at most 4000 bytes."
)

// AskFollowUp adds a follow-up question from the caller to the
// investigation. The audit row is written in the same transaction by
// investigator.AddFollowUpTx; resolving a group and stopping a watcher are
// audited by the code that does them as well.
func (s *server) AskFollowUp(ctx context.Context, req apigen.AskFollowUpRequestObject) (apigen.AskFollowUpResponseObject, error) {
	p, _ := auth.PrincipalFrom(ctx)

	text := strings.TrimSpace(req.Body.Text)

	switch {
	case text == "":
		return nil, problemf(http.StatusBadRequest, detailEmptyAsk)
	case len(text) > maxQuestionBytes:
		return nil, problemf(http.StatusBadRequest, detailLongAsk)
	}

	question := investigator.Question{
		Key:      ids.New().String(),
		Author:   p.Name,
		AuthorID: p.ID.String(),
		// AuthorToken makes the audit row name an API token.
		AuthorToken: p.Kind == auth.KindToken,
		Text:        s.mask(text),
	}

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.requireInvestigation(ctx, tx, req.Id); err != nil {
			return err
		}

		added, err := investigator.AddFollowUpTx(ctx, tx, req.Id, question)
		if errors.Is(err, investigator.ErrTriageOnly) {
			return problemf(http.StatusConflict, detailTriageOnly)
		}

		if err != nil {
			return fmt.Errorf("add the question: %w", err)
		}

		if !added {
			return problemf(http.StatusConflict, detailFailed)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ask a follow-up question of investigation %s: %w", req.Id, err)
	}

	return apigen.AskFollowUp202JSONResponse{Added: true}, nil
}

// CancelInvestigation asks the run to stop. investigator.RequestCancel
// writes the audit row in the same transaction when this request asked
// for it.
func (s *server) CancelInvestigation(ctx context.Context, req apigen.CancelInvestigationRequestObject) (apigen.CancelInvestigationResponseObject, error) {
	p, _ := auth.PrincipalFrom(ctx)

	var asked bool

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.requireInvestigation(ctx, tx, req.Id); err != nil {
			return err
		}

		var err error

		asked, err = investigator.RequestCancel(ctx, tx, req.Id, investigator.Requester{UserID: &p.ID, Token: p.Kind == auth.KindToken})
		if err != nil {
			return fmt.Errorf("ask the run to stop: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stop investigation %s: %w", req.Id, err)
	}

	return apigen.CancelInvestigation200JSONResponse{Requested: asked}, nil
}

// ResolveInvestigation marks the investigation's group as resolved and
// ends its watchers.
func (s *server) ResolveInvestigation(ctx context.Context, req apigen.ResolveInvestigationRequestObject) (apigen.ResolveInvestigationResponseObject, error) {
	p, _ := auth.PrincipalFrom(ctx)

	var resolved bool

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		groupID, err := db.New(tx).GetInvestigationGroup(ctx, req.Id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoInvestigation
		}

		if err != nil {
			return fmt.Errorf("read the group: %w", err)
		}

		resolved, err = grouper.Resolve(ctx, tx, groupID, grouper.Actor{Kind: actorKindOf(p), ID: p.ID.String()}, s.now(), s.watchers)
		if errors.Is(err, grouper.ErrGroupClosed) {
			return problemf(http.StatusConflict, detailGroupClosed)
		}

		if err != nil {
			return fmt.Errorf("resolve group %s: %w", groupID, err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("resolve investigation %s: %w", req.Id, err)
	}

	return apigen.ResolveInvestigation200JSONResponse{Resolved: resolved}, nil
}

// StopWatcher stops a running watcher for the caller. The watcher's own
// audit row names the caller.
func (s *server) StopWatcher(ctx context.Context, req apigen.StopWatcherRequestObject) (apigen.StopWatcherResponseObject, error) {
	if s.stopper == nil {
		return nil, problemf(http.StatusServiceUnavailable, detailNoStopper)
	}

	p, _ := auth.PrincipalFrom(ctx)

	_, stopped, err := s.stopper.Stop(ctx, req.Id, output.Person{UserID: p.ID, Name: p.Name})
	if err != nil {
		return nil, fmt.Errorf("stop watcher %s: %w", req.Id, err)
	}

	if !stopped {
		_, err := db.New(s.pool).GetWatcher(ctx, req.Id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, problemf(http.StatusNotFound, detailNoWatcher)
		}

		if err != nil {
			return nil, fmt.Errorf("read watcher %s: %w", req.Id, err)
		}
	}

	return apigen.StopWatcher200JSONResponse{Stopped: stopped}, nil
}

// requireInvestigation returns errNoInvestigation when no investigation
// has id.
func (s *server) requireInvestigation(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	_, err := db.New(q).GetInvestigationGroup(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNoInvestigation
	}

	if err != nil {
		return fmt.Errorf("read investigation %s: %w", id, err)
	}

	return nil
}
