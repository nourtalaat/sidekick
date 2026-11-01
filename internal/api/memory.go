package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/report"
)

// The page sizes of GET /memory/entries.
const (
	defaultMemoryPage = 50
	maxMemoryPage     = memory.MaxPageSize
)

// maxMemorySearchChars is the longest q of GET /memory/entries.
const maxMemorySearchChars = 200

// errMemoryUnavailable answers the memory operations of a server that was
// started without a memory store.
var errMemoryUnavailable = problemf(http.StatusServiceUnavailable, "Memory management is not available.")

// MemoryStore reads and changes memory entries and service notes by hand.
// *memory.Store is one.
type MemoryStore interface {
	List(ctx context.Context, p memory.ListParams) (memory.Page, error)
	Get(ctx context.Context, id uuid.UUID) (memory.Entry, error)
	Update(ctx context.Context, actor memory.Actor, id uuid.UUID, e memory.Edit) (memory.Entry, error)
	Delete(ctx context.Context, actor memory.Actor, id uuid.UUID) error
}

var _ MemoryStore = (*memory.Store)(nil)

// ListMemoryEntries returns a page of memory entries, newest confirmation
// first.
func (s *server) ListMemoryEntries(ctx context.Context, req apigen.ListMemoryEntriesRequestObject) (apigen.ListMemoryEntriesResponseObject, error) {
	if s.memoryEntries == nil {
		return nil, errMemoryUnavailable
	}

	params, err := memoryListParams(req.Params)
	if err != nil {
		return nil, err
	}

	page, err := s.memoryEntries.List(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list memory entries: %w", err)
	}

	list := apigen.ListMemoryEntries200JSONResponse{Entries: make([]apigen.MemoryEntry, len(page.Entries))}

	for i, e := range page.Entries {
		list.Entries[i] = memoryEntryOf(e)
	}

	if page.Next != nil {
		cursor := timeCursor{at: page.Next.ConfirmedAt, id: page.Next.InvestigationID}.String()
		list.NextCursor = &cursor
	}

	return list, nil
}

// GetMemoryEntry returns one memory entry.
func (s *server) GetMemoryEntry(ctx context.Context, req apigen.GetMemoryEntryRequestObject) (apigen.GetMemoryEntryResponseObject, error) {
	if s.memoryEntries == nil {
		return nil, errMemoryUnavailable
	}

	e, err := s.memoryEntries.Get(ctx, req.Id)
	if err != nil {
		return nil, memoryError(err)
	}

	return apigen.GetMemoryEntry200JSONResponse(memoryEntryOf(e)), nil
}

// UpdateMemoryEntry edits a memory entry, disables it or enables it.
func (s *server) UpdateMemoryEntry(ctx context.Context, req apigen.UpdateMemoryEntryRequestObject) (apigen.UpdateMemoryEntryResponseObject, error) {
	if s.memoryEntries == nil {
		return nil, errMemoryUnavailable
	}

	body := req.Body

	e, err := s.memoryEntries.Update(ctx, memoryActor(ctx), req.Id, memory.Edit{Summary: body.Summary, Cause: textOf(body.Cause), Disabled: body.Disabled})
	if err != nil {
		return nil, memoryError(err)
	}

	return apigen.UpdateMemoryEntry200JSONResponse(memoryEntryOf(e)), nil
}

// DeleteMemoryEntry deletes a memory entry.
func (s *server) DeleteMemoryEntry(ctx context.Context, req apigen.DeleteMemoryEntryRequestObject) (apigen.DeleteMemoryEntryResponseObject, error) {
	if s.memoryEntries == nil {
		return nil, errMemoryUnavailable
	}

	if err := s.memoryEntries.Delete(ctx, memoryActor(ctx), req.Id); err != nil {
		return nil, memoryError(err)
	}

	return apigen.DeleteMemoryEntry204Response{}, nil
}

// memoryListParams reads the parameters of GET /memory/entries.
func memoryListParams(p apigen.ListMemoryEntriesParams) (memory.ListParams, error) {
	params := memory.ListParams{PageSize: defaultMemoryPage, Disabled: p.Disabled}

	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > maxMemoryPage {
			return memory.ListParams{}, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxMemoryPage)
		}

		params.PageSize = *p.Limit
	}

	if p.Verdict != nil {
		if !slices.Contains(knownVerdicts, report.Verdict(*p.Verdict)) {
			return memory.ListParams{}, problemf(http.StatusBadRequest, "verdict must be one of %s.", joinVerdicts())
		}

		params.Verdict = string(*p.Verdict)
	}

	if p.Service != nil {
		params.Service = *p.Service
	}

	if p.Q != nil {
		q := strings.TrimSpace(*p.Q)
		if len([]rune(q)) > maxMemorySearchChars {
			return memory.ListParams{}, problemf(http.StatusBadRequest, "q is longer than %d characters.", maxMemorySearchChars)
		}

		params.Search = q
	}

	if p.Cursor != nil {
		c, err := parseTimeCursor(*p.Cursor)
		if err != nil {
			return memory.ListParams{}, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		params.AfterAt, params.AfterID = c.at, c.id
	}

	return params, nil
}

// memoryEntryOf is the API's view of a memory entry.
func memoryEntryOf(e memory.Entry) apigen.MemoryEntry {
	return apigen.MemoryEntry{
		InvestigationId: e.InvestigationID, Service: nonEmptyPtr(e.Service), AlertName: nonEmptyPtr(e.AlertName),
		Verdict: apigen.Verdict(e.Verdict), Summary: e.Summary, Cause: nonEmptyPtr(e.Cause), ConfirmedAt: e.ConfirmedAt, Disabled: e.Disabled,
	}
}

// memoryActor is the caller as a memory change records them.
func memoryActor(ctx context.Context) memory.Actor {
	p, _ := auth.PrincipalFrom(ctx)

	actor := memory.Actor{Kind: actorKindOf(p), ID: p.ID.String()}
	if p.Kind == auth.KindUser {
		id := p.ID
		actor.UserID = &id
	}

	return actor
}

// memoryError turns an error of the memory store into the problem the
// caller sees; other errors stay errors, which answer 500.
func memoryError(err error) error {
	if invalid, ok := errors.AsType[*memory.InvalidError](err); ok {
		return &Error{
			Status: http.StatusUnprocessableEntity, Code: codeValidation, Detail: invalid.Detail + ".",
			Fields: []problem.FieldError{{Field: invalid.Field, Message: invalid.Detail}},
		}
	}

	if errors.Is(err, memory.ErrNotFound) {
		return &Error{Status: http.StatusNotFound, Code: codeNotFound, Detail: "No memory entry has this ID."}
	}

	return fmt.Errorf("change memory: %w", err)
}
