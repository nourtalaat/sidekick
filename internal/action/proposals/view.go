package proposals

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// ErrNotFound means no proposal has the ID.
var ErrNotFound = errors.New("no action proposal has this ID")

// View is a proposal with the title of its investigation, for the review
// queue.
type View struct {
	action.Proposal

	// AlertTitle is the title of the alert the investigation is about. It
	// is text from outside, so a screen shows it as plain text.
	AlertTitle string
}

// ListOptions say which proposals List returns.
type ListOptions struct {
	// Statuses are the statuses to list.
	Statuses []action.ProposalStatus
	// After is the cursor of the page: the proposal that comes before the
	// first one returned. The zero value starts with the newest.
	After Cursor
	// PageSize is how many proposals to return at most.
	PageSize int32
}

// Cursor says where a page of List ends. Pass the proposal last on a page
// to get the page after it.
type Cursor struct {
	ProposedAt time.Time
	ID         uuid.UUID
}

// farFuture is the proposed_at that is after every proposal, for the first
// page.
var farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// OfInvestigation returns the proposals of an investigation, oldest first,
// for the publish update. It is what publish.Options.Proposals is set to.
func OfInvestigation(ctx context.Context, q db.DBTX, investigationID uuid.UUID) ([]action.Proposal, error) {
	rows, err := db.New(q).ListInvestigationActionProposals(ctx, investigationID)
	if err != nil {
		return nil, fmt.Errorf("list the proposals of investigation %s: %w", investigationID, err)
	}

	out := make([]action.Proposal, 0, len(rows))

	for _, r := range rows {
		p, err := proposalOf(r.ActionProposal, r.Route, r.DecidedByName)
		if err != nil {
			return nil, fmt.Errorf("proposal %s: %w", r.ActionProposal.ID, err)
		}

		out = append(out, p)
	}

	return out, nil
}

// Get returns the proposal with id, or ErrNotFound.
func Get(ctx context.Context, q db.DBTX, id uuid.UUID) (View, error) {
	r, err := db.New(q).GetActionProposalView(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrNotFound
	}

	if err != nil {
		return View{}, fmt.Errorf("read proposal %s: %w", id, err)
	}

	p, err := proposalOf(r.ActionProposal, r.Route, r.DecidedByName)
	if err != nil {
		return View{}, fmt.Errorf("proposal %s: %w", id, err)
	}

	return View{Proposal: p, AlertTitle: r.AlertTitle}, nil
}

// List returns one page of proposals with one of the statuses, newest
// first. It reads PageSize proposals; ask for one more than the page holds
// to know whether another page follows. A status given twice is read once.
// Each status is read from its own
// part of the index, in order and up to a page, and the pages are merged,
// so the cost of a page does not grow with the number of proposals.
func List(ctx context.Context, q db.DBTX, opts ListOptions) ([]View, error) {
	after := opts.After
	if after.ProposedAt.IsZero() {
		after = Cursor{ProposedAt: farFuture, ID: uuid.Max}
	}

	queries := db.New(q)

	var rows []db.ListActionProposalsRow

	for _, status := range slices.Compact(slices.Sorted(slices.Values(opts.Statuses))) {
		page, err := queries.ListActionProposals(ctx, db.ListActionProposalsParams{
			Status: string(status), BeforeAt: after.ProposedAt, BeforeID: after.ID, PageSize: opts.PageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("list %s proposals: %w", status, err)
		}

		rows = append(rows, page...)
	}

	slices.SortFunc(rows, func(a, b db.ListActionProposalsRow) int {
		return cmp.Or(b.ActionProposal.ProposedAt.Compare(a.ActionProposal.ProposedAt), bytes.Compare(b.ActionProposal.ID[:], a.ActionProposal.ID[:]))
	})

	rows = rows[:min(len(rows), int(opts.PageSize))]
	out := make([]View, 0, len(rows))

	for _, r := range rows {
		p, err := proposalOf(r.ActionProposal, r.Route, r.DecidedByName)
		if err != nil {
			return nil, fmt.Errorf("proposal %s: %w", r.ActionProposal.ID, err)
		}

		out = append(out, View{Proposal: p, AlertTitle: r.AlertTitle})
	}

	return out, nil
}

// proposalOf converts a stored proposal, with the route of its
// investigation and the name of who decided it.
func proposalOf(row db.ActionProposal, route, decidedByName string) (action.Proposal, error) {
	p := action.Proposal{
		ID:                 row.ID.String(),
		InvestigationID:    row.InvestigationID.String(),
		Route:              route,
		Action:             row.Action,
		Params:             row.Params,
		ParamsText:         action.DisplayParams(row.Params),
		Reason:             row.Reason,
		Mode:               action.Mode(row.Mode),
		Status:             action.ProposalStatus(row.Status),
		ProposedAt:         row.ProposedAt,
		ExpiresAt:          row.ExpiresAt,
		DecidedByName:      decidedByName,
		DecidedBySlackUser: deref(row.DecidedBySlackUser),
		DecidedAt:          deref(row.DecidedAt),
		ApprovedByPolicy:   approvedByPolicy(row),
		Error:              deref(row.Error),
	}

	if row.DecidedBy != nil {
		p.DecidedBy = row.DecidedBy.String()
	}

	if row.UndoOf != nil {
		p.UndoOf = row.UndoOf.String()
	}

	snap, err := snapshotOf(row)
	if err != nil {
		return action.Proposal{}, fmt.Errorf("read the snapshot: %w", err)
	}

	if snap != nil {
		changes, err := action.Changes(*snap)
		if err != nil {
			return action.Proposal{}, fmt.Errorf("write the changes: %w", err)
		}

		p.Snapshot, p.Changes = snap, changes

		if p.UndoOf == "" {
			p.Files = gitchange.FilesOf(*snap)
		}
	}

	p.Undoable = row.Undoable && p.Status == action.StatusSucceeded && p.UndoOf == "" && (p.Snapshot == nil || !p.Snapshot.Deletes())

	if len(row.Result) > 0 {
		var res action.Result
		if err := json.Unmarshal(row.Result, &res); err != nil {
			return action.Proposal{}, fmt.Errorf("decode the result: %w", err)
		}

		p.Result = &res
	}

	return p, nil
}

// snapshotOf returns the snapshot stored with the proposal, or nil when it
// has none.
func snapshotOf(row db.ActionProposal) (*action.Snapshot, error) {
	if len(row.Snapshot) == 0 {
		return nil, nil
	}

	var snap action.Snapshot
	if err := json.Unmarshal(row.Snapshot, &snap); err != nil {
		return nil, fmt.Errorf("decode the snapshot: %w", err)
	}

	return &snap, nil
}

// approvedByPolicy reports whether the route's policy approved the
// proposal: it was approved, and no person or token decided it.
func approvedByPolicy(row db.ActionProposal) bool {
	return row.DecidedAt != nil && row.DecidedBy == nil && row.DecidedBySlackUser == nil
}

// deref returns *p, or the zero value for nil.
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}
