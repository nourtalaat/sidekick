package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The limits on service notes.
const (
	// MaxNotesChars is the most characters the notes of a service may have.
	MaxNotesChars = 4000
	// MaxServiceChars is the most characters a service name may have.
	MaxServiceChars = 200
	// MaxNotesPageSize is the most services one page of the notes list holds.
	MaxNotesPageSize = 500
)

// The audit log names of changes to service notes.
const (
	auditNotesUpdated = "service_notes.updated"
	auditNotesDeleted = "service_notes.deleted"

	auditTargetNotes = "service_notes"
)

// Notes are the notes people wrote about a service. A service without notes
// has empty Text and no UpdatedAt.
type Notes struct {
	Service string
	Text    string
	// UpdatedAt is when the notes last changed.
	UpdatedAt *time.Time
	// UpdatedByName is the name of the user who last changed the notes,
	// empty when a token did.
	UpdatedByName string
}

// NotesSummary is a service that has notes, for the list of them.
type NotesSummary struct {
	Service string
	// Preview is the first characters of the notes.
	Preview       string
	UpdatedAt     time.Time
	UpdatedByName string
}

// NotesPage is a page of the services that have notes, by name.
type NotesPage struct {
	Services []NotesSummary
	// Next is set when there are more services: the last service of the
	// page, to pass as the place to start after.
	Next string
}

// notesView is what the audit log shows of notes.
type notesView struct {
	Text string `json:"text"`
}

// Notes returns the notes about a service, which are empty for a service
// that has none. It returns an *InvalidError for a service name that cannot
// be right.
func (s *Store) Notes(ctx context.Context, service string) (Notes, error) {
	service, err := checkService(service)
	if err != nil {
		return Notes{}, err
	}

	return readNotes(ctx, s.pool, service)
}

// ListNotes returns a page of the services that have notes, by name,
// starting after the service after.
func (s *Store) ListNotes(ctx context.Context, after string, pageSize int) (NotesPage, error) {
	size := min(max(pageSize, 1), MaxNotesPageSize)

	rows, err := db.New(store.CustomPlan{DB: s.pool}).ListServiceNotes(ctx, db.ListServiceNotesParams{
		After: after, PageSize: int32(size + 1), //nolint:gosec // At most MaxNotesPageSize + 1.
	})
	if err != nil {
		return NotesPage{}, fmt.Errorf("list service notes: %w", err)
	}

	page := NotesPage{Services: make([]NotesSummary, 0, min(len(rows), size))}

	for i, row := range rows {
		if i == size {
			page.Next = page.Services[size-1].Service

			break
		}

		page.Services = append(page.Services, NotesSummary{
			Service: row.Service, Preview: row.Preview, UpdatedAt: row.UpdatedAt, UpdatedByName: row.UpdatedByName,
		})
	}

	return page, nil
}

// SetNotes replaces the notes about a service and returns them as stored.
// The text is trimmed and redacted, and the checks of its length and
// characters are on the redacted text. Empty text deletes the notes. The
// last write wins. A change writes one audit entry; text equal to the
// stored notes, or empty text for a service without notes, writes nothing.
// It returns an *InvalidError for a service name or text that cannot be
// right.
func (s *Store) SetNotes(ctx context.Context, actor Actor, service, text string) (Notes, error) {
	service, err := checkService(service)
	if err != nil {
		return Notes{}, err
	}

	text, err = s.checkNotes(text)
	if err != nil {
		return Notes{}, err
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)

		if err := q.LockServiceNotes(ctx, service); err != nil {
			return fmt.Errorf("lock: %w", err)
		}

		cur, err := readNotes(ctx, tx, service)
		if err != nil {
			return err
		}

		switch {
		case text == cur.Text:
			return nil
		case text == "":
			if _, err := q.DeleteServiceNotes(ctx, service); err != nil {
				return fmt.Errorf("delete: %w", err)
			}

			return writeNotesAudit(ctx, tx, actor, auditNotesDeleted, service, notesView{Text: cur.Text}, nil)
		}

		if err := q.UpsertServiceNotes(ctx, db.UpsertServiceNotesParams{Service: service, Text: text, UpdatedBy: actor.UserID}); err != nil {
			return fmt.Errorf("write: %w", err)
		}

		var old any
		if cur.Text != "" {
			old = notesView{Text: cur.Text}
		}

		return writeNotesAudit(ctx, tx, actor, auditNotesUpdated, service, old, notesView{Text: text})
	})
	if err != nil {
		return Notes{}, fmt.Errorf("set the notes of service %s: %w", service, err)
	}

	return readNotes(ctx, s.pool, service)
}

// checkNotes returns the notes as they are stored, or an *InvalidError.
func (s *Store) checkNotes(text string) (string, error) {
	text = strings.TrimSpace(text)

	if s.redactor != nil {
		text = s.redactor.Text(text).Text
	}

	switch {
	case utf8.RuneCountInString(text) > MaxNotesChars:
		return "", &InvalidError{Field: "text", Detail: fmt.Sprintf("The notes are longer than %d characters", MaxNotesChars)}
	case strings.ContainsFunc(text, isBadRune):
		return "", &InvalidError{Field: "text", Detail: "The notes have control characters"}
	}

	return text, nil
}

// checkService returns the name of a service as it is stored: trimmed, not
// empty, not long, and on one line.
func checkService(service string) (string, error) {
	service = strings.TrimSpace(service)

	switch {
	case service == "":
		return "", &InvalidError{Field: "service", Detail: "The service is required"}
	case utf8.RuneCountInString(service) > MaxServiceChars:
		return "", &InvalidError{Field: "service", Detail: fmt.Sprintf("The service is longer than %d characters", MaxServiceChars)}
	case strings.ContainsFunc(service, unicode.IsControl):
		return "", &InvalidError{Field: "service", Detail: "The service has control characters"}
	}

	return service, nil
}

// readNotes reads the notes about service through q.
func readNotes(ctx context.Context, q db.DBTX, service string) (Notes, error) {
	row, err := db.New(q).GetServiceNotesDetail(ctx, service)
	if errors.Is(err, pgx.ErrNoRows) {
		return Notes{Service: service}, nil
	}

	if err != nil {
		return Notes{}, fmt.Errorf("read the notes of service %s: %w", service, err)
	}

	return Notes{Service: row.Service, Text: row.Text, UpdatedAt: &row.UpdatedAt, UpdatedByName: row.UpdatedByName}, nil
}

// writeNotesAudit writes the audit entry of a change to the notes of
// service.
func writeNotesAudit(ctx context.Context, tx pgx.Tx, actor Actor, action, service string, before, after any) error {
	return writeAuditFor(ctx, tx, actor, action, auditTargetNotes, service, nil, before, after)
}
