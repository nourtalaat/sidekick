package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The page sizes of GET /audit.
const (
	defaultAuditPage = 50
	maxAuditPage     = 200
)

// selfActors are the values of the actor filter that select the entries
// Sidekick made on its own, by the kind of actor. Any other value is the
// ID of a user, a token or a Slack user.
var selfActors = []string{audit.ActorSidekick, audit.ActorPolicy, audit.ActorSystem}

// ListAudit returns a page of the audit log, newest first.
func (s *server) ListAudit(ctx context.Context, req apigen.ListAuditRequestObject) (apigen.ListAuditResponseObject, error) {
	params, limit, err := auditParams(req.Params)
	if err != nil {
		return nil, fmt.Errorf("check the filters: %w", err)
	}

	rows, err := db.New(customPlan{db: s.pool}).ListAuditEntries(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}

	list := apigen.ListAudit200JSONResponse{Entries: make([]apigen.AuditEntry, 0, min(len(rows), limit))}

	for i, row := range rows {
		if i == limit {
			cursor := pageCursor{at: rows[i-1].At, id: rows[i-1].ID}.String()
			list.NextCursor = &cursor

			break
		}

		entry, err := s.auditEntryOf(row)
		if err != nil {
			return nil, fmt.Errorf("audit entry %d: %w", row.ID, err)
		}

		list.Entries = append(list.Entries, entry)
	}

	return list, nil
}

// auditParams checks the filters of GET /audit and returns the query's
// parameters, which ask for one row more than the page size, and the page
// size.
func auditParams(p apigen.ListAuditParams) (db.ListAuditEntriesParams, int, error) {
	limit := defaultAuditPage
	if p.Limit != nil {
		limit = *p.Limit
	}

	if limit < 1 || limit > maxAuditPage {
		return db.ListAuditEntriesParams{}, 0, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxAuditPage)
	}

	params := db.ListAuditEntriesParams{
		AtFrom:          p.From,
		AtTo:            p.To,
		Action:          nonEmpty(p.Action),
		InvestigationID: (*uuid.UUID)(p.Investigation),
		PageSize:        int32(limit + 1), //nolint:gosec // limit is at most maxAuditPage.
	}

	if actor := nonEmpty(p.Actor); actor != nil {
		if slices.Contains(selfActors, *actor) {
			params.ActorKind = actor
		} else {
			params.ActorID = actor
		}
	}

	if p.Cursor != nil {
		c, err := parsePageCursor(*p.Cursor)
		if err != nil {
			return db.ListAuditEntriesParams{}, 0, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		params.CursorAt, params.CursorID = &c.at, &c.id
	}

	return params, limit, nil
}

// auditEntryOf is the API's view of a row of the audit log, with the old
// and new values masked.
func (s *server) auditEntryOf(row db.ListAuditEntriesRow) (apigen.AuditEntry, error) {
	var details struct {
		Old any `json:"old"`
		New any `json:"new"`
	}

	if err := json.Unmarshal(row.Details, &details); err != nil {
		return apigen.AuditEntry{}, fmt.Errorf("decode the details: %w", err)
	}

	return apigen.AuditEntry{
		Id:              row.ID,
		At:              row.At,
		ActorKind:       apigen.AuditEntryActorKind(row.ActorKind),
		ActorId:         row.ActorID,
		ActorName:       nonEmptyText(row.ActorName),
		Action:          row.Action,
		TargetKind:      row.TargetKind,
		TargetId:        row.TargetID,
		InvestigationId: row.InvestigationID,
		Old:             s.redacted(details.Old),
		New:             s.redacted(details.New),
	}, nil
}
