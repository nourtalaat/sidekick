package lessons

import (
	"context"
	"encoding/json"
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
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The origins of a lesson, as lessons.origin holds them.
const (
	OriginFeedback = "feedback"
	OriginUI       = "ui"
	OriginSlack    = "slack"
)

// The other statuses of a lesson, besides StatusProposed and StatusActive.
const (
	StatusDisabled = "disabled"
	StatusRejected = "rejected"
)

// MaxScopeChars is the most characters a scope value may have.
const MaxScopeChars = 200

// MaxPageSize is the most lessons one page holds.
const MaxPageSize = 200

// The audit log names of changes to lessons. A change of status to
// disabled, rejected or back to active has an action of its own.
const (
	auditCreated  = "lesson.created"
	auditUpdated  = "lesson.updated"
	auditApproved = "lesson.approved"
	auditRejected = "lesson.rejected"
	auditDisabled = "lesson.disabled"
	auditEnabled  = "lesson.enabled"
	auditDeleted  = "lesson.deleted"
)

var (
	// ErrNotFound is returned for a lesson that does not exist.
	ErrNotFound = errors.New("the lesson does not exist")
	// ErrStale is returned for a change to a lesson that changed since the
	// caller read it.
	ErrStale = errors.New("the lesson changed since it was read")
	// ErrNotProposed is returned for an approval of a lesson that does not
	// wait for review.
	ErrNotProposed = errors.New("the lesson does not wait for review")
)

// InvalidError is returned for a lesson that cannot be right. Field is the
// field of the request that is wrong, and Detail says what is wrong in
// words for the person who sent it.
type InvalidError struct {
	Field  string
	Detail string
}

func (e *InvalidError) Error() string {
	return "invalid lesson: " + e.Detail
}

// Actor is who changes a lesson, as the audit log records them.
type Actor struct {
	// Kind is audit.ActorUser or audit.ActorToken, and ID the user's or
	// token's ID, or a Slack user ID.
	Kind, ID string
	// UserID is the Sidekick user, or nil for a token or a Slack user, who
	// is not one. It becomes created_by and approved_by.
	UserID *uuid.UUID
}

// FeedbackLink is a feedback row a lesson was drafted from, with the
// investigation it was given on.
type FeedbackLink struct {
	FeedbackID      uuid.UUID `json:"feedbackId"`
	InvestigationID uuid.UUID `json:"investigationId"`
}

// Stored is a stored lesson.
type Stored struct {
	ID     uuid.UUID
	Text   string
	Scope  Scope
	Status string
	Origin string
	// Feedback are the feedback rows the lesson was drafted from.
	Feedback  []FeedbackLink
	CreatedBy *uuid.UUID
	// ApprovedBy is the user who approved the lesson, when a user did.
	ApprovedBy         *uuid.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Helpful, Unhelpful int
	LastUsedAt         *time.Time
	// Version goes up with every change.
	Version int
}

// ListParams are the filters and the page of Store.List. Empty ones are not
// used.
type ListParams struct {
	Status  string
	Service string
	// Search is a web-search style query for the text of a lesson.
	Search string
	// AfterAt and AfterID, when AfterID is set, start the page after the
	// lesson created then with that ID.
	AfterAt  time.Time
	AfterID  uuid.UUID
	PageSize int
}

// Page is a page of lessons, newest first.
type Page struct {
	Lessons []Stored
	// Next is set when there are more lessons: the last lesson of the page.
	Next *Stored
}

// New is a lesson to store.
type New struct {
	Text  string
	Scope Scope
	// Origin is OriginUI or OriginSlack, and Status StatusActive or
	// StatusProposed.
	Origin, Status string
}

// Edit is a change to a lesson. Text and Scope replace the lesson's, and
// Status, when it is not empty, its status.
type Edit struct {
	Text   string
	Scope  Scope
	Status string
}

// Store reads and changes lessons. Every change is audited in the
// transaction that makes it.
type Store struct {
	pool     *pgxpool.Pool
	redactor *redact.Redactor
}

// NewStore returns a Store on pool. It redacts the text of every lesson it
// stores with redactor, since the text goes into prompts; a nil redactor
// leaves the text as it is.
func NewStore(pool *pgxpool.Pool, redactor *redact.Redactor) *Store {
	return &Store{pool: pool, redactor: redactor}
}

// List returns a page of lessons, newest first. It runs with a plan made
// for the filters of the call, since the query has optional filters.
func (s *Store) List(ctx context.Context, p ListParams) (Page, error) {
	size := min(max(p.PageSize, 1), MaxPageSize)

	params := db.ListLessonsParams{
		Status: optional(p.Status), Service: optional(p.Service), Search: optional(p.Search), PageSize: int32(size + 1), //nolint:gosec // At most MaxPageSize + 1.
	}

	if p.AfterID != uuid.Nil {
		params.AfterAt, params.AfterID = &p.AfterAt, &p.AfterID
	}

	rows, err := db.New(store.CustomPlan{DB: s.pool}).ListLessons(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list lessons: %w", err)
	}

	page := Page{Lessons: make([]Stored, 0, min(len(rows), size))}

	for i, row := range rows {
		r, err := recordOf(row)
		if err != nil {
			return Page{}, fmt.Errorf("read lesson %s: %w", row.ID, err)
		}

		if i == size {
			page.Next = &page.Lessons[size-1]

			break
		}

		page.Lessons = append(page.Lessons, r)
	}

	return page, nil
}

// Counts returns how many lessons there are with each status. A status
// with none is not in the map.
func (s *Store) Counts(ctx context.Context) (map[string]int, error) {
	rows, err := db.New(s.pool).CountLessonsByStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("count lessons: %w", err)
	}

	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.Status] = int(r.N)
	}

	return out, nil
}

// Get returns one lesson, or an error that wraps ErrNotFound.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Stored, error) {
	return get(ctx, s.pool, id)
}

// Create stores a lesson an admin wrote. A lesson with no scope field is
// global. It returns an *InvalidError for a text or scope that cannot be
// right.
func (s *Store) Create(ctx context.Context, actor Actor, n New) (Stored, error) {
	text, scope, err := s.check(n.Text, n.Scope)
	if err != nil {
		return Stored{}, fmt.Errorf("check the lesson: %w", err)
	}

	id := ids.New()

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := db.New(tx).InsertLesson(ctx, db.InsertLessonParams{
			ID: id, Text: text, ScopeService: optional(scope.Service), ScopeAlert: optional(scope.Alert),
			ScopeRoute: optional(scope.Route), ScopeStrategy: optional(scope.Strategy), Status: n.Status, Origin: n.Origin,
			CreatedBy: actor.UserID,
		})
		if err != nil {
			return fmt.Errorf("insert: %w", err)
		}

		return writeAudit(ctx, tx, actor, auditCreated, id, nil, view{Text: text, Scope: scope, Status: n.Status, Origin: n.Origin})
	})
	if err != nil {
		return Stored{}, fmt.Errorf("create the lesson: %w", err)
	}

	return get(ctx, s.pool, id)
}

// Update replaces the text and scope of a lesson, and its status when
// e.Status is set. version is the version the caller read. A lesson that
// waits for review stays so, or is rejected; an active lesson may be
// disabled and a disabled one enabled. Approving needs Approve. A lesson
// drafted from feedback keeps at least one scope field.
//
// It returns errors that wrap ErrNotFound and ErrStale, and an
// *InvalidError.
func (s *Store) Update(ctx context.Context, actor Actor, id uuid.UUID, version int, e Edit) (Stored, error) {
	text, scope, err := s.check(e.Text, e.Scope)
	if err != nil {
		return Stored{}, fmt.Errorf("check the lesson: %w", err)
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lock(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("lock the lesson: %w", err)
		}

		status, err := nextStatus(cur.Status, e.Status)
		if err != nil {
			return fmt.Errorf("check the status: %w", err)
		}

		if cur.Origin == OriginFeedback && scope.Global() {
			return &InvalidError{Field: "scope", Detail: "A lesson drafted from feedback needs at least one scope field"}
		}

		rows, err := db.New(tx).UpdateLesson(ctx, db.UpdateLessonParams{
			ID: id, Version: int32(version), Text: text, Status: status, //nolint:gosec // A version that does not fit matches no lesson.
			ScopeService: optional(scope.Service), ScopeAlert: optional(scope.Alert),
			ScopeRoute: optional(scope.Route), ScopeStrategy: optional(scope.Strategy),
		})
		if err != nil {
			return fmt.Errorf("update: %w", err)
		}

		if rows == 0 {
			return fmt.Errorf("lesson %s is at version %d, not %d: %w", id, cur.Version, version, ErrStale)
		}

		return writeAudit(ctx, tx, actor, updateAction(cur.Status, status), id, cur.view(), view{Text: text, Scope: scope, Status: status, Origin: cur.Origin})
	})
	if err != nil {
		return Stored{}, fmt.Errorf("update lesson %s: %w", id, err)
	}

	return get(ctx, s.pool, id)
}

// Approve makes a lesson that waits for review active, noting actor as the
// approver. version is the version the caller read. It returns errors that
// wrap ErrNotFound, ErrStale and ErrNotProposed.
func (s *Store) Approve(ctx context.Context, actor Actor, id uuid.UUID, version int) (Stored, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lock(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("lock the lesson: %w", err)
		}

		if cur.Status != StatusProposed {
			return fmt.Errorf("lesson %s is %s: %w", id, cur.Status, ErrNotProposed)
		}

		rows, err := db.New(tx).ApproveLesson(ctx, db.ApproveLessonParams{ID: id, Version: int32(version), ApprovedBy: actor.UserID}) //nolint:gosec // As above.
		if err != nil {
			return fmt.Errorf("approve: %w", err)
		}

		if rows == 0 {
			return fmt.Errorf("lesson %s is at version %d, not %d: %w", id, cur.Version, version, ErrStale)
		}

		return writeAudit(ctx, tx, actor, auditApproved, id, cur.view(), view{Text: cur.Text, Scope: cur.scope(), Status: StatusActive, Origin: cur.Origin})
	})
	if err != nil {
		return Stored{}, fmt.Errorf("approve lesson %s: %w", id, err)
	}

	return get(ctx, s.pool, id)
}

// Delete deletes a lesson. The audit entry keeps its text. It returns an
// error that wraps ErrNotFound for a lesson that does not exist.
func (s *Store) Delete(ctx context.Context, actor Actor, id uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lock(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("lock the lesson: %w", err)
		}

		if _, err := db.New(tx).DeleteLesson(ctx, id); err != nil {
			return fmt.Errorf("delete: %w", err)
		}

		return writeAudit(ctx, tx, actor, auditDeleted, id, cur.view(), nil)
	})
	if err != nil {
		return fmt.Errorf("delete lesson %s: %w", id, err)
	}

	return nil
}

// Used returns the lessons an investigation used, in the order they were
// written, with their text and status as they are now.
func Used(ctx context.Context, q db.DBTX, investigationID uuid.UUID, limit int) ([]Stored, error) {
	rows, err := db.New(q).ListLessonsUsedBy(ctx, db.ListLessonsUsedByParams{InvestigationID: investigationID, MaxRows: int32(limit)}) //nolint:gosec // A small limit.
	if err != nil {
		return nil, fmt.Errorf("list the lessons of investigation %s: %w", investigationID, err)
	}

	out := make([]Stored, len(rows))
	for i, r := range rows {
		out[i] = Stored{
			ID: r.ID, Text: r.Text, Status: r.Status,
			Scope: Scope{Service: deref(r.ScopeService), Alert: deref(r.ScopeAlert), Route: deref(r.ScopeRoute), Strategy: deref(r.ScopeStrategy)},
		}
	}

	return out, nil
}

// view is a lesson as the audit log shows it.
type view struct {
	Text   string `json:"text"`
	Scope  Scope  `json:"scope"`
	Status string `json:"status"`
	Origin string `json:"origin"`
}

// locked is the lesson a change holds a lock on.
type locked db.LockLessonRow

func (l locked) scope() Scope {
	return Scope{Service: deref(l.ScopeService), Alert: deref(l.ScopeAlert), Route: deref(l.ScopeRoute), Strategy: deref(l.ScopeStrategy)}
}

func (l locked) view() view {
	return view{Text: l.Text, Scope: l.scope(), Status: l.Status, Origin: l.Origin}
}

// lock locks the lesson with id for the transaction.
func lock(ctx context.Context, tx pgx.Tx, id uuid.UUID) (locked, error) {
	row, err := db.New(tx).LockLesson(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return locked{}, fmt.Errorf("lesson %s: %w", id, ErrNotFound)
	}

	if err != nil {
		return locked{}, fmt.Errorf("lock lesson %s: %w", id, err)
	}

	return locked(row), nil
}

// get reads the lesson with id.
func get(ctx context.Context, q db.DBTX, id uuid.UUID) (Stored, error) {
	row, err := db.New(q).GetLesson(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Stored{}, fmt.Errorf("lesson %s: %w", id, ErrNotFound)
	}

	if err != nil {
		return Stored{}, fmt.Errorf("read lesson %s: %w", id, err)
	}

	r, err := recordOf(db.ListLessonsRow(row))
	if err != nil {
		return Stored{}, fmt.Errorf("read lesson %s: %w", id, err)
	}

	return r, nil
}

// recordOf converts a row of the list.
func recordOf(row db.ListLessonsRow) (Stored, error) {
	var links []FeedbackLink
	if len(row.FeedbackLinks) > 0 {
		if err := json.Unmarshal(row.FeedbackLinks, &links); err != nil {
			return Stored{}, fmt.Errorf("decode the feedback links: %w", err)
		}
	}

	return Stored{
		ID: row.ID, Text: row.Text, Status: row.Status, Origin: row.Origin, Feedback: links,
		Scope:     Scope{Service: deref(row.ScopeService), Alert: deref(row.ScopeAlert), Route: deref(row.ScopeRoute), Strategy: deref(row.ScopeStrategy)},
		CreatedBy: row.CreatedBy, ApprovedBy: row.ApprovedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Helpful: int(row.Helpful), Unhelpful: int(row.Unhelpful), LastUsedAt: row.LastUsedAt, Version: int(row.Version),
	}, nil
}

// check returns the text and scope of a lesson as they are stored, or an
// *InvalidError. The text is trimmed and redacted, and the checks of its
// length and characters are on the redacted text, since that is what is
// stored; a scope value is trimmed and may not span lines, and is not
// redacted, since it is a name that must match an event exactly.
func (s *Store) check(text string, scope Scope) (string, Scope, error) {
	text = strings.TrimSpace(text)

	if s.redactor != nil {
		text = s.redactor.Text(text).Text
	}

	switch {
	case text == "":
		return "", Scope{}, &InvalidError{Field: "text", Detail: "The text is required"}
	case utf8.RuneCountInString(text) > MaxTextChars:
		return "", Scope{}, &InvalidError{Field: "text", Detail: fmt.Sprintf("The text is longer than %d characters", MaxTextChars)}
	case strings.ContainsFunc(text, isBadRune):
		return "", Scope{}, &InvalidError{Field: "text", Detail: "The text has control characters"}
	}

	for _, f := range []struct {
		name  string
		value *string
	}{{"scope.service", &scope.Service}, {"scope.alert", &scope.Alert}, {"scope.route", &scope.Route}, {"scope.strategy", &scope.Strategy}} {
		*f.value = strings.TrimSpace(*f.value)

		switch {
		case utf8.RuneCountInString(*f.value) > MaxScopeChars:
			return "", Scope{}, &InvalidError{Field: f.name, Detail: fmt.Sprintf("%s is longer than %d characters", f.name, MaxScopeChars)}
		case strings.ContainsFunc(*f.value, unicode.IsControl):
			return "", Scope{}, &InvalidError{Field: f.name, Detail: f.name + " has control characters"}
		}
	}

	return text, scope, nil
}

// isBadRune reports whether r is a control character other than a new
// line or a tab, which a hand-written lesson may hold.
func isBadRune(r rune) bool {
	return unicode.IsControl(r) && r != '\n' && r != '\t'
}

// nextStatus returns the status a lesson with status cur gets from an edit
// that asks for want, or an *InvalidError for a change that is not allowed.
func nextStatus(cur, want string) (string, error) {
	if want == "" || want == cur {
		return cur, nil
	}

	switch {
	case cur == StatusProposed && want == StatusRejected:
		return want, nil
	case cur == StatusActive && want == StatusDisabled, cur == StatusDisabled && want == StatusActive:
		return want, nil
	case want == StatusActive && (cur == StatusProposed || cur == StatusRejected):
		return "", &InvalidError{Field: "status", Detail: "A lesson that waits for review or was rejected becomes active by approving it"}
	default:
		return "", &InvalidError{Field: "status", Detail: fmt.Sprintf("A %s lesson cannot become %s", cur, want)}
	}
}

// updateAction returns the audit action of a change of status from before
// to after.
func updateAction(before, after string) string {
	switch {
	case before == after:
		return auditUpdated
	case after == StatusRejected:
		return auditRejected
	case after == StatusDisabled:
		return auditDisabled
	default:
		return auditEnabled
	}
}

// writeAudit writes the audit entry of a change to lesson id.
func writeAudit(ctx context.Context, tx pgx.Tx, actor Actor, action string, id uuid.UUID, before, after any) error {
	err := auditor.Write(ctx, tx, audit.Entry{
		ActorKind: actor.Kind, ActorID: actor.ID, Action: action, TargetKind: auditTargetLesson, TargetID: id.String(), Old: before, New: after,
	})
	if err != nil {
		return fmt.Errorf("audit the change: %w", err)
	}

	return nil
}
