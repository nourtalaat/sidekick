package proposals

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// expireBatch is the most proposals one transaction of Expire expires.
const expireBatch = 100

var _ queue.Handler = queue.HandlerFunc((*Service)(nil).HandleExpire)

// HandleExpire runs an expire_proposals job. Register it for
// queue.KindExpireProposals; a Scheduler queues the job every minute.
func (s *Service) HandleExpire(ctx context.Context, _ db.Job) error {
	if _, err := s.Expire(ctx); err != nil {
		return fmt.Errorf("expire proposals: %w", err)
	}

	return nil
}

// Expire moves every proposal that nobody decided before it expired to
// expired, and returns how many it moved. Each one is audited as the
// system's doing and shown in its investigation's outputs in the same
// transaction that moves it. A proposal that expired cannot be approved
// even before this runs: deciding checks the time itself.
func (s *Service) Expire(ctx context.Context) (int, error) {
	total := 0

	for {
		n, err := s.expireSome(ctx)
		if err != nil {
			return total, fmt.Errorf("expire: %w", err)
		}

		total += n

		if n < expireBatch {
			return total, nil
		}
	}
}

// expireSome expires up to expireBatch proposals in one transaction.
func (s *Service) expireSome(ctx context.Context) (int, error) {
	var expired []db.ExpireActionProposalsRow

	err := pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		var err error

		expired, err = db.New(tx).ExpireActionProposals(ctx, db.ExpireActionProposalsParams{Now: s.now(), Batch: expireBatch})
		if err != nil {
			return fmt.Errorf("move them to expired: %w", err)
		}

		var shown []string

		for _, e := range expired {
			err := s.audit.Write(ctx, tx, audit.Entry{
				ActorKind:       audit.ActorSystem,
				Action:          AuditExpired,
				TargetKind:      auditTargetKind,
				TargetID:        e.ID.String(),
				InvestigationID: &e.InvestigationID,
				New:             map[string]any{"status": action.StatusExpired, "action": e.Action},
			})
			if err != nil {
				return fmt.Errorf("audit the expiry of proposal %s: %w", e.ID, err)
			}

			if slices.Contains(shown, e.InvestigationID.String()) {
				continue
			}

			shown = append(shown, e.InvestigationID.String())

			if err := publish.Refresh(ctx, tx, e.InvestigationID); err != nil {
				return fmt.Errorf("show the expiry of proposal %s: %w", e.ID, err)
			}
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("in a transaction: %w", err)
	}

	for _, e := range expired {
		s.opts.Metrics.reached(e.Action, action.StatusExpired)
		s.log.InfoContext(ctx, "action proposal expired", "proposal_id", e.ID.String(), "investigation_id", e.InvestigationID.String())
	}

	return len(expired), nil
}
