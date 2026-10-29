package feedback

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The actions of the audit entries of the fix mark.
const (
	ActionFixApplied   = "fix.applied"
	ActionFixUnapplied = "fix.unapplied"
)

// ErrNoFix is returned when a fix is marked on an investigation that did
// not end as a false positive, which has no fix to apply.
var ErrNoFix = errors.New("only a finished false positive has a fix to mark")

// Marker is who marks a fix: a user, or an API token whose ID is in UserID.
type Marker struct {
	UserID uuid.UUID
	// Token says UserID is the ID of an API token.
	Token bool
}

// FixMark is the state of the mark after SetFixApplied.
type FixMark struct {
	// Applied says whether the fix is marked as applied.
	Applied bool
	// At is when it was marked, while it is.
	At *time.Time
	// Changed says whether the call changed the mark.
	Changed bool
}

// fixView is what the audit log records of the mark.
type fixView struct {
	Applied bool `json:"applied"`
}

// SetFixApplied marks the fix of a false positive as applied, or takes the
// mark back, in q's transaction, and writes the audit row in the same one.
// The dashboard counts a marked fix as a noisy alert fixed. It holds a lock
// on the investigation's row. A call that finds the mark as it asks for
// changes nothing and writes no audit row. It returns errors that wrap
// ErrNotFound, and ErrNoFix when applied is true for an investigation that
// is not a finished false positive.
func SetFixApplied(ctx context.Context, q db.DBTX, id uuid.UUID, applied bool, by Marker) (FixMark, error) {
	queries := db.New(q)

	inv, err := queries.LockInvestigation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return FixMark{}, fmt.Errorf("investigation %s: %w", id, ErrNotFound)
	}

	if err != nil {
		return FixMark{}, fmt.Errorf("lock investigation %s: %w", id, err)
	}

	if (inv.FixAppliedAt != nil) == applied {
		return FixMark{Applied: applied, At: inv.FixAppliedAt}, nil
	}

	if applied && (inv.Status != statusFinal || inv.Verdict == nil || *inv.Verdict != string(report.FalsePositive)) {
		return FixMark{}, fmt.Errorf("investigation %s: %w", id, ErrNoFix)
	}

	at, err := storeFixMark(ctx, queries, id, applied, by)
	if err != nil {
		return FixMark{}, fmt.Errorf("investigation %s: %w", id, err)
	}

	action := ActionFixUnapplied
	if applied {
		action = ActionFixApplied
	}

	err = auditor.Write(ctx, q, audit.Entry{
		ActorKind:       by.actorKind(),
		ActorID:         by.UserID.String(),
		Action:          action,
		TargetKind:      auditTarget,
		TargetID:        id.String(),
		InvestigationID: &id,
		Old:             fixView{Applied: !applied},
		New:             fixView{Applied: applied},
	})
	if err != nil {
		return FixMark{}, fmt.Errorf("audit the fix mark of investigation %s: %w", id, err)
	}

	return FixMark{Applied: applied, At: at, Changed: true}, nil
}

// storeFixMark sets or clears the mark and returns the time it was set.
func storeFixMark(ctx context.Context, queries *db.Queries, id uuid.UUID, applied bool, by Marker) (*time.Time, error) {
	if !applied {
		if err := queries.ClearFixApplied(ctx, id); err != nil {
			return nil, fmt.Errorf("clear the fix mark: %w", err)
		}

		return nil, nil //nolint:nilnil // No mark, so no time.
	}

	at, err := queries.SetFixApplied(ctx, db.SetFixAppliedParams{ID: id, By: &by.UserID})
	if err != nil {
		return nil, fmt.Errorf("set the fix mark: %w", err)
	}

	return &at, nil
}

// actorKind is the kind of actor the marker is in an audit entry.
func (m Marker) actorKind() string {
	if m.Token {
		return audit.ActorToken
	}

	return audit.ActorUser
}
