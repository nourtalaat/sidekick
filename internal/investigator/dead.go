package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// deadReason is the reason of the report of an investigation whose job
// became dead.
const deadReason = "The investigation stopped after repeated failures. This report holds only what it had recorded."

// OnDead is the OnDead hook of the investigate kind. It ends the
// investigation of a dead job as failed, with an inconclusive report built
// from its saved steps and stop reason error, and publishes it, so the
// outputs show "Could not finish". The job is dead, so no worker owns the
// run and the writes are not fenced. An investigation that has ended
// already is left as it is, so calling it again changes nothing.
func (h *Handler) OnDead(ctx context.Context, job db.Job) error {
	id, err := decodePayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the payload of dead job %d: %w", job.ID, err)
	}

	queries := db.New(h.opts.Pool)

	steps, err := queries.ListSteps(ctx, id)
	if err != nil {
		return fmt.Errorf("load the steps of investigation %s: %w", id, err)
	}

	hist, err := newHistory(steps)
	if err != nil {
		return fmt.Errorf("read the steps of investigation %s: %w", id, err)
	}

	end := ending{report: hist.inconclusive(report.StopError, deadReason, ""), status: StatusFailed}
	end.report.Checked = hist.checked(nil)

	raw, err := json.Marshal(end.report)
	if err != nil {
		return fmt.Errorf("encode the report: %w", err)
	}

	var (
		failed db.Investigation
		ended  bool
	)

	err = pgx.BeginFunc(ctx, h.opts.Pool, func(tx pgx.Tx) error {
		failed, err = db.New(tx).FailInvestigation(ctx, db.FailInvestigationParams{
			Verdict:    new(string(end.report.Verdict)),
			Confidence: new(string(end.report.Confidence)),
			Report:     store.JSON(raw),
			StopReason: new(string(end.report.StopReason)),
			ID:         id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			ended = true

			return nil
		}

		if err != nil {
			return fmt.Errorf("store the report: %w", err)
		}

		return publishFinal(ctx, tx, failed, hist.progress(), &end.report)
	})
	if err != nil {
		return fmt.Errorf("fail investigation %s: %w", id, err)
	}

	if ended {
		return nil
	}

	h.finished(ctx, h.log.With("investigation_id", id.String(), "job_id", job.ID, "route", failed.Route), failed, end)

	return nil
}
