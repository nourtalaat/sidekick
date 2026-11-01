package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Trigger decides when a strategy version has collected enough wrong
// feedback to get a proposed change. The zero value is off.
type Trigger struct {
	// Enabled turns the trigger on. Off, Check does nothing.
	Enabled bool
	// Threshold is how many ratings of wrong make a version get a proposal.
	Threshold int
	// Window is how far back the ratings are counted.
	Window time.Duration
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// auditor writes the audit rows of changes to proposals.
var auditor = audit.NewWriter()

// Check counts the ratings of wrong on the verdict and the findings of
// investigations that ran the same strategy at the same version as inv,
// within the window, and when they reach the threshold, adds a proposal for
// that version and queues the job that drafts it. It reports whether it
// added a proposal.
//
// A version gets one proposal, ever: when it has one, whatever its status,
// Check adds nothing. Call it in the transaction that stores the rating, so
// the rating, the count, the proposal and the job commit together. An
// investigation without a strategy or its version is never counted.
func (t Trigger) Check(ctx context.Context, q db.DBTX, inv db.Investigation) (bool, error) {
	if !t.Enabled || t.Threshold < 1 || inv.Strategy == nil || inv.StrategyVersion == nil {
		return false, nil
	}

	now := time.Now
	if t.Now != nil {
		now = t.Now
	}

	queries := db.New(q)

	wrong, err := queries.CountWrongFeedbackForVersion(ctx, db.CountWrongFeedbackForVersionParams{
		Since: now().Add(-t.Window), Strategy: inv.Strategy, StrategyVersion: inv.StrategyVersion,
	})
	if err != nil {
		return false, fmt.Errorf("count the wrong ratings of strategy %s: %w", *inv.Strategy, err)
	}

	if int(wrong) < t.Threshold {
		return false, nil
	}

	id, err := queries.InsertStrategyProposal(ctx, db.InsertStrategyProposalParams{
		ID: ids.New(), Name: *inv.Strategy, BaseVersion: *inv.StrategyVersion, TriggerCount: wrong,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("add a proposal for strategy %s: %w", *inv.Strategy, err)
	}

	payload, err := json.Marshal(ProposeStrategyPayload{ProposalID: id})
	if err != nil {
		return false, fmt.Errorf("encode the payload of the draft job: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{Kind: queue.KindProposeStrategy, Payload: payload, UniqueKey: ProposeStrategyKey(id)})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return false, fmt.Errorf("queue the draft of proposal %s: %w", id, err)
	}

	err = auditor.Write(ctx, q, audit.Entry{
		ActorKind: audit.ActorSystem, Action: ActionCreated, TargetKind: KindProposal, TargetID: id.String(),
		New: map[string]any{"strategy": *inv.Strategy, "baseVersion": *inv.StrategyVersion, "wrongRatings": wrong},
	})
	if err != nil {
		return false, fmt.Errorf("audit proposal %s: %w", id, err)
	}

	return true, nil
}
