package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/memory"
)

// The page sizes of GET /services/notes.
const (
	defaultNotesPage = 100
	maxNotesPage     = memory.MaxNotesPageSize
)

// ListServiceNotes returns a page of the services that have notes, by name.
func (s *server) ListServiceNotes(ctx context.Context, req apigen.ListServiceNotesRequestObject) (apigen.ListServiceNotesResponseObject, error) {
	if s.memoryEntries == nil {
		return nil, errMemoryUnavailable
	}

	limit := defaultNotesPage

	if req.Params.Limit != nil {
		if *req.Params.Limit < 1 || *req.Params.Limit > maxNotesPage {
			return nil, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxNotesPage)
		}

		limit = *req.Params.Limit
	}

	page, err := s.memoryEntries.ListNotes(ctx, textOf(req.Params.After), limit)
	if err != nil {
		return nil, fmt.Errorf("list service notes: %w", err)
	}

	list := apigen.ListServiceNotes200JSONResponse{Services: make([]apigen.ServiceNotesSummary, len(page.Services))}

	for i, n := range page.Services {
		list.Services[i] = apigen.ServiceNotesSummary{
			Service: n.Service, Preview: n.Preview, UpdatedAt: n.UpdatedAt, UpdatedByName: nonEmptyPtr(n.UpdatedByName),
		}
	}

	list.NextAfter = nonEmptyPtr(page.Next)

	return list, nil
}

// GetServiceNotes returns the notes about a service, empty when it has
// none.
func (s *server) GetServiceNotes(ctx context.Context, req apigen.GetServiceNotesRequestObject) (apigen.GetServiceNotesResponseObject, error) {
	if s.memoryEntries == nil {
		return nil, errMemoryUnavailable
	}

	n, err := s.memoryEntries.Notes(ctx, req.Name)
	if err != nil {
		return nil, memoryError(err)
	}

	return apigen.GetServiceNotes200JSONResponse(notesOf(n)), nil
}

// PutServiceNotes replaces the notes about a service. Empty text deletes
// them.
func (s *server) PutServiceNotes(ctx context.Context, req apigen.PutServiceNotesRequestObject) (apigen.PutServiceNotesResponseObject, error) {
	if s.memoryEntries == nil {
		return nil, errMemoryUnavailable
	}

	n, err := s.memoryEntries.SetNotes(ctx, memoryActor(ctx), req.Name, req.Body.Text)
	if err != nil {
		return nil, memoryError(err)
	}

	return apigen.PutServiceNotes200JSONResponse(notesOf(n)), nil
}

// notesOf is the API's view of the notes about a service.
func notesOf(n memory.Notes) apigen.ServiceNotes {
	return apigen.ServiceNotes{Service: n.Service, Text: n.Text, UpdatedAt: n.UpdatedAt, UpdatedByName: nonEmptyPtr(n.UpdatedByName)}
}
