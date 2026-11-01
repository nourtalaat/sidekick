package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The limits on what an admin writes, in characters.
const (
	// MaxSummaryChars is the most characters an edited summary may have.
	MaxSummaryChars = 2000
	// MaxCauseChars is the most characters an edited cause may have.
	MaxCauseChars = 2000
	// MaxPageSize is the most entries one page of the list holds.
	MaxPageSize = 200
)

// The audit log names of changes to memory entries.
const (
	auditUpdated  = "memory.updated"
	auditDisabled = "memory.disabled"
	auditEnabled  = "memory.enabled"
	auditDeleted  = "memory.deleted"

	auditTargetEntry = "memory_entry"
)

var (
	// ErrNotFound is returned for a memory entry that does not exist.
	ErrNotFound = errors.New("the memory entry does not exist")

	auditor = audit.NewWriter()
)

// InvalidError is returned for a text that cannot be right. Field is the
// field of the request that is wrong, and Detail says what is wrong in
// words for the person who sent it.
type InvalidError struct {
	Field  string
	Detail string
}

func (e *InvalidError) Error() string {
	return "invalid memory text: " + e.Detail
}

// Actor is who changes memory, as the audit log records them.
type Actor struct {
	// Kind is audit.ActorUser or audit.ActorToken, and ID the user's or
	// token's ID.
	Kind, ID string
	// UserID is the Sidekick user, or nil for a token, which is not one.
	UserID *uuid.UUID
}

// Entry is a stored memory entry.
type Entry struct {
	InvestigationID uuid.UUID
	Service         string
	AlertName       string
	Verdict         string
	Summary         string
	Cause           string
	ConfirmedAt     time.Time
	Disabled        bool
}

// ListParams are the filters and the page of Store.List. Empty ones are not
// used.
type ListParams struct {
	Service string
	Verdict string
	// Search is a web-search style query for the words of an entry.
	Search string
	// Disabled, when set, keeps only the entries that are disabled (true)
	// or enabled (false).
	Disabled *bool
	// AfterAt and AfterID, when AfterID is set, start the page after the
	// entry confirmed then for that investigation.
	AfterAt  time.Time
	AfterID  uuid.UUID
	PageSize int
}

// Page is a page of entries, newest confirmation first.
type Page struct {
	Entries []Entry
	// Next is set when there are more entries: the last entry of the page.
	Next *Entry
}

// Edit replaces the summary, cause and disabled flag of an entry. An empty
// Cause leaves the entry without one.
type Edit struct {
	Summary  string
	Cause    string
	Disabled bool
}

// Store reads and changes memory entries and service notes by hand. Every
// change is audited in the transaction that makes it.
type Store struct {
	pool     *pgxpool.Pool
	redactor *redact.Redactor
	embed    bool
}

// NewStore returns a Store on pool. It redacts the text people write with
// redactor, since it goes into prompts; a nil redactor leaves it as it is.
// With opts.Embed, an edit of an entry's summary or cause enqueues the job
// that embeds it again, in the transaction of the edit.
func NewStore(pool *pgxpool.Pool, redactor *redact.Redactor, opts SyncOptions) *Store {
	return &Store{pool: pool, redactor: redactor, embed: opts.Embed}
}

// List returns a page of entries, newest confirmation first, disabled ones
// too.
func (s *Store) List(ctx context.Context, p ListParams) (Page, error) {
	size := min(max(p.PageSize, 1), MaxPageSize)

	params := db.ListMemoryEntriesParams{
		Service: optional(p.Service), Verdict: optional(p.Verdict), Search: optional(p.Search), Disabled: p.Disabled,
		PageSize: int32(size + 1), //nolint:gosec // At most MaxPageSize + 1.
	}

	if p.AfterID != uuid.Nil {
		params.AfterAt, params.AfterID = &p.AfterAt, &p.AfterID
	}

	rows, err := db.New(store.CustomPlan{DB: s.pool}).ListMemoryEntries(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list memory entries: %w", err)
	}

	page := Page{Entries: make([]Entry, 0, min(len(rows), size))}

	for i, row := range rows {
		if i == size {
			page.Next = &page.Entries[size-1]

			break
		}

		page.Entries = append(page.Entries, entryOf(db.GetMemoryEntryRow(row)))
	}

	return page, nil
}

// Get returns one entry, or an error that wraps ErrNotFound.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Entry, error) {
	row, err := db.New(s.pool).GetMemoryEntry(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, fmt.Errorf("memory entry %s: %w", id, ErrNotFound)
	}

	if err != nil {
		return Entry{}, fmt.Errorf("read memory entry %s: %w", id, err)
	}

	return entryOf(row), nil
}

// Update replaces the summary, cause and disabled flag of an entry, and
// returns the entry as stored. Text that differs from the stored text is
// trimmed, redacted and checked; text that equals it is kept as it is, so a
// long summary the model wrote can still be disabled. The last write wins.
// An edit that changes nothing writes nothing, not even an audit entry.
//
// The next change of the investigation's confirmation replaces the summary
// and cause with the investigation's own, and keeps the disabled flag.
//
// It returns errors that wrap ErrNotFound, and an *InvalidError.
func (s *Store) Update(ctx context.Context, actor Actor, id uuid.UUID, e Edit) (Entry, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockEntry(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("lock the entry: %w", err)
		}

		next, err := s.checked(cur, e)
		if err != nil {
			return fmt.Errorf("check the edit: %w", err)
		}

		if next == cur {
			return nil
		}

		rows, err := db.New(tx).UpdateMemoryEntry(ctx, db.UpdateMemoryEntryParams{
			InvestigationID: id, Summary: next.Summary, Cause: optional(next.Cause), Disabled: next.Disabled,
		})
		if err != nil {
			return fmt.Errorf("update: %w", err)
		}

		if rows == 0 {
			return fmt.Errorf("memory entry %s: %w", id, ErrNotFound)
		}

		textChanged := next.Summary != cur.Summary || next.Cause != cur.Cause

		if textChanged && s.embed {
			if err := enqueueEmbed(ctx, tx, id); err != nil {
				return fmt.Errorf("embed the edited entry: %w", err)
			}
		}

		return writeAudit(ctx, tx, actor, updateAction(cur, next), id, cur.edit(), next.edit())
	})
	if err != nil {
		return Entry{}, fmt.Errorf("update memory entry %s: %w", id, err)
	}

	return s.Get(ctx, id)
}

// Delete deletes an entry and its embedding. The audit entry keeps what the
// entry said. A later change of the investigation's confirmation writes a
// new entry; disable an entry to keep it out for good. It returns an error
// that wraps ErrNotFound for an entry that does not exist.
func (s *Store) Delete(ctx context.Context, actor Actor, id uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockEntry(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("lock the entry: %w", err)
		}

		if _, err := db.New(tx).DeleteMemoryEntry(ctx, id); err != nil {
			return fmt.Errorf("delete: %w", err)
		}

		return writeAudit(ctx, tx, actor, auditDeleted, id, cur.view(), nil)
	})
	if err != nil {
		return fmt.Errorf("delete memory entry %s: %w", id, err)
	}

	return nil
}

// editView is what the audit log shows of an entry that was edited.
type editView struct {
	Summary  string `json:"summary"`
	Cause    string `json:"cause,omitempty"`
	Disabled bool   `json:"disabled"`
}

// entryView is what the audit log shows of an entry that was deleted.
type entryView struct {
	Service     string    `json:"service,omitempty"`
	AlertName   string    `json:"alertName,omitempty"`
	Verdict     string    `json:"verdict"`
	Summary     string    `json:"summary"`
	Cause       string    `json:"cause,omitempty"`
	ConfirmedAt time.Time `json:"confirmedAt"`
	Disabled    bool      `json:"disabled"`
}

func (e Entry) view() entryView {
	return entryView{
		Service: e.Service, AlertName: e.AlertName, Verdict: e.Verdict, Summary: e.Summary, Cause: e.Cause,
		ConfirmedAt: e.ConfirmedAt, Disabled: e.Disabled,
	}
}

func (e Entry) edit() editView {
	return editView{Summary: e.Summary, Cause: e.Cause, Disabled: e.Disabled}
}

// checked returns cur with e applied, or an *InvalidError.
func (s *Store) checked(cur Entry, e Edit) (Entry, error) {
	next := cur
	next.Disabled = e.Disabled

	summary, err := s.checkText("summary", e.Summary, cur.Summary, MaxSummaryChars, true)
	if err != nil {
		return Entry{}, fmt.Errorf("check the summary: %w", err)
	}

	cause, err := s.checkText("cause", e.Cause, cur.Cause, MaxCauseChars, false)
	if err != nil {
		return Entry{}, fmt.Errorf("check the cause: %w", err)
	}

	next.Summary, next.Cause = summary, cause

	return next, nil
}

// checkText returns the text to store for the field with the given name
// when the caller sends text and the stored text is stored. Text equal to
// the stored text, once trimmed, is kept as stored and not checked again.
// Other text is trimmed and redacted, and the checks of its length and
// characters are on the redacted text, since that is what is stored.
func (s *Store) checkText(field, text, stored string, maxChars int, required bool) (string, error) {
	text = strings.TrimSpace(text)
	if text == strings.TrimSpace(stored) {
		return stored, nil
	}

	if s.redactor != nil {
		text = s.redactor.Text(text).Text
	}

	switch {
	case text == "" && required:
		return "", &InvalidError{Field: field, Detail: "The " + field + " is required"}
	case utf8.RuneCountInString(text) > maxChars:
		return "", &InvalidError{Field: field, Detail: fmt.Sprintf("The %s is longer than %d characters", field, maxChars)}
	case strings.ContainsFunc(text, isBadRune):
		return "", &InvalidError{Field: field, Detail: "The " + field + " has control characters"}
	}

	return text, nil
}

// isBadRune reports whether r is a control character other than a new
// line or a tab, which hand-written text may hold.
func isBadRune(r rune) bool {
	return unicode.IsControl(r) && r != '\n' && r != '\t'
}

// updateAction returns the audit action of a change from before to after.
// A change of the text is an update, whatever else changed with it; the
// audit entry holds both values.
func updateAction(before, after Entry) string {
	switch {
	case before.Summary != after.Summary || before.Cause != after.Cause:
		return auditUpdated
	case after.Disabled:
		return auditDisabled
	default:
		return auditEnabled
	}
}

// lockEntry reads the entry with id and locks its row for the transaction.
func lockEntry(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Entry, error) {
	row, err := db.New(tx).LockMemoryEntry(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, fmt.Errorf("memory entry %s: %w", id, ErrNotFound)
	}

	if err != nil {
		return Entry{}, fmt.Errorf("lock memory entry %s: %w", id, err)
	}

	return entryOf(db.GetMemoryEntryRow(row)), nil
}

// entryOf converts a row of the memory queries.
func entryOf(row db.GetMemoryEntryRow) Entry {
	return Entry{
		InvestigationID: row.InvestigationID, Service: deref(row.Service), AlertName: deref(row.AlertName), Verdict: row.Verdict,
		Summary: row.Summary, Cause: deref(row.Cause), ConfirmedAt: row.ConfirmedAt, Disabled: row.Disabled,
	}
}

// writeAudit writes the audit entry of a change to the memory entry of
// investigation id.
func writeAudit(ctx context.Context, tx pgx.Tx, actor Actor, action string, id uuid.UUID, before, after any) error {
	if err := writeAuditFor(ctx, tx, actor, action, auditTargetEntry, id.String(), &id, before, after); err != nil {
		return fmt.Errorf("audit the change to memory entry %s: %w", id, err)
	}

	return nil
}

// writeAuditFor writes the audit entry of a change to what kind and target
// name, for the investigation when there is one.
func writeAuditFor(ctx context.Context, tx pgx.Tx, actor Actor, action, kind, target string, investigation *uuid.UUID, before, after any) error {
	err := auditor.Write(ctx, tx, audit.Entry{
		ActorKind: actor.Kind, ActorID: actor.ID, Action: action, TargetKind: kind, TargetID: target,
		InvestigationID: investigation, Old: before, New: after,
	})
	if err != nil {
		return fmt.Errorf("audit the change: %w", err)
	}

	return nil
}

// optional returns a pointer to s, or nil when s is empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
