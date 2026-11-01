package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/lessons"
	"github.com/nourtalaat/sidekick/internal/problem"
)

// The page sizes of GET /lessons.
const (
	defaultLessonsPage = 50
	maxLessonsPage     = lessons.MaxPageSize
)

// maxLessonSearchChars is the longest q of GET /lessons.
const maxLessonSearchChars = 200

// maxUsedLessons is how many lessons an investigation shows as used.
const maxUsedLessons = 50

// The codes of the problems that lesson changes answer with, besides those
// of rule changes.
const codeNotProposed = "not_proposed"

// LessonStore reads and changes lessons. *lessons.Store is one.
type LessonStore interface {
	List(ctx context.Context, p lessons.ListParams) (lessons.Page, error)
	Counts(ctx context.Context) (map[string]int, error)
	Create(ctx context.Context, actor lessons.Actor, n lessons.New) (lessons.Stored, error)
	Update(ctx context.Context, actor lessons.Actor, id uuid.UUID, version int, e lessons.Edit) (lessons.Stored, error)
	Approve(ctx context.Context, actor lessons.Actor, id uuid.UUID, version int) (lessons.Stored, error)
	Delete(ctx context.Context, actor lessons.Actor, id uuid.UUID) error
}

var _ LessonStore = (*lessons.Store)(nil)

// knownLessonStatuses are the statuses the list can filter by.
var knownLessonStatuses = []apigen.LessonStatus{apigen.LessonProposed, apigen.LessonActive, apigen.LessonDisabled, apigen.LessonRejected}

// errBadLessonCursor means a cursor is not one GET /lessons returned.
var errBadLessonCursor = errors.New("not a cursor")

// ListLessons returns a page of lessons, newest first, with the number of
// lessons of each status.
func (s *server) ListLessons(ctx context.Context, req apigen.ListLessonsRequestObject) (apigen.ListLessonsResponseObject, error) {
	params, err := lessonListParams(req.Params)
	if err != nil {
		return nil, err
	}

	page, err := s.lessons.List(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list lessons: %w", err)
	}

	counts, err := s.lessons.Counts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count lessons: %w", err)
	}

	list := apigen.ListLessons200JSONResponse{
		Lessons: make([]apigen.Lesson, len(page.Lessons)),
		Counts: apigen.LessonCounts{
			Proposed: counts[lessons.StatusProposed], Active: counts[lessons.StatusActive],
			Disabled: counts[lessons.StatusDisabled], Rejected: counts[lessons.StatusRejected],
		},
	}

	for i, l := range page.Lessons {
		list.Lessons[i] = lessonOf(l)
	}

	if page.Next != nil {
		cursor := lessonCursor{createdAt: page.Next.CreatedAt, id: page.Next.ID}.String()
		list.NextCursor = &cursor
	}

	return list, nil
}

// CreateLesson stores a lesson an admin wrote.
func (s *server) CreateLesson(ctx context.Context, req apigen.CreateLessonRequestObject) (apigen.CreateLessonResponseObject, error) {
	body := req.Body

	n := lessons.New{Text: body.Text, Scope: lessonScopeOf(body.Scope), Origin: lessons.OriginUI, Status: lessons.StatusActive}
	if body.Status != nil && *body.Status == apigen.NewLessonProposed {
		n.Status = lessons.StatusProposed
	}

	stored, err := s.lessons.Create(ctx, lessonActor(ctx), n)
	if err != nil {
		return nil, lessonError(err)
	}

	return apigen.CreateLesson201JSONResponse(lessonOf(stored)), nil
}

// UpdateLesson edits a lesson, disables or enables it, or rejects it.
func (s *server) UpdateLesson(ctx context.Context, req apigen.UpdateLessonRequestObject) (apigen.UpdateLessonResponseObject, error) {
	body := req.Body

	e := lessons.Edit{Text: body.Text, Scope: lessonScopeOf(body.Scope)}
	if body.Status != nil {
		e.Status = string(*body.Status)
	}

	stored, err := s.lessons.Update(ctx, lessonActor(ctx), req.Id, body.Version, e)
	if err != nil {
		return nil, lessonError(err)
	}

	return apigen.UpdateLesson200JSONResponse(lessonOf(stored)), nil
}

// ApproveLesson makes a lesson that waits for review active.
func (s *server) ApproveLesson(ctx context.Context, req apigen.ApproveLessonRequestObject) (apigen.ApproveLessonResponseObject, error) {
	stored, err := s.lessons.Approve(ctx, lessonActor(ctx), req.Id, req.Body.Version)
	if err != nil {
		return nil, lessonError(err)
	}

	return apigen.ApproveLesson200JSONResponse(lessonOf(stored)), nil
}

// DeleteLesson deletes a lesson.
func (s *server) DeleteLesson(ctx context.Context, req apigen.DeleteLessonRequestObject) (apigen.DeleteLessonResponseObject, error) {
	if err := s.lessons.Delete(ctx, lessonActor(ctx), req.Id); err != nil {
		return nil, lessonError(err)
	}

	return apigen.DeleteLesson204Response{}, nil
}

// lessonListParams reads the parameters of GET /lessons.
func lessonListParams(p apigen.ListLessonsParams) (lessons.ListParams, error) {
	params := lessons.ListParams{PageSize: defaultLessonsPage}

	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > maxLessonsPage {
			return lessons.ListParams{}, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxLessonsPage)
		}

		params.PageSize = *p.Limit
	}

	if p.Status != nil {
		if !slices.Contains(knownLessonStatuses, *p.Status) {
			return lessons.ListParams{}, problemf(http.StatusBadRequest, "status must be one of %s.", joinLessonStatuses())
		}

		params.Status = string(*p.Status)
	}

	if p.Service != nil {
		params.Service = *p.Service
	}

	if p.Q != nil {
		q := strings.TrimSpace(*p.Q)
		if len(q) > maxLessonSearchChars {
			return lessons.ListParams{}, problemf(http.StatusBadRequest, "q is longer than %d characters.", maxLessonSearchChars)
		}

		params.Search = q
	}

	if p.Cursor != nil {
		c, err := parseLessonCursor(*p.Cursor)
		if err != nil {
			return lessons.ListParams{}, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		params.AfterAt, params.AfterID = c.createdAt, c.id
	}

	return params, nil
}

// joinLessonStatuses lists the statuses for an error message.
func joinLessonStatuses() string {
	names := make([]string, len(knownLessonStatuses))
	for i, s := range knownLessonStatuses {
		names[i] = string(s)
	}

	return strings.Join(names, ", ")
}

// lessonOf is the API's view of a lesson.
func lessonOf(l lessons.Stored) apigen.Lesson {
	out := apigen.Lesson{
		Id: l.ID, Text: l.Text, Scope: lessonScopeView(l.Scope), Status: apigen.LessonStatus(l.Status), Origin: apigen.LessonOrigin(l.Origin),
		Feedback: make([]apigen.LessonFeedback, len(l.Feedback)),
		Helpful:  l.Helpful, Unhelpful: l.Unhelpful, Version: l.Version, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
		LastUsedAt: l.LastUsedAt, CreatedBy: userIDOf(l.CreatedBy), ApprovedBy: userIDOf(l.ApprovedBy),
	}

	for i, f := range l.Feedback {
		out.Feedback[i] = apigen.LessonFeedback{FeedbackId: f.FeedbackID, InvestigationId: f.InvestigationID}
	}

	return out
}

// lessonScopeView is the API's view of a scope.
func lessonScopeView(s lessons.Scope) apigen.LessonScope {
	return apigen.LessonScope{Service: nonEmptyPtr(s.Service), Alert: nonEmptyPtr(s.Alert), Route: nonEmptyPtr(s.Route), Strategy: nonEmptyPtr(s.Strategy)}
}

// lessonScopeOf reads the scope of a request body.
func lessonScopeOf(s *apigen.LessonScope) lessons.Scope {
	if s == nil {
		return lessons.Scope{}
	}

	return lessons.Scope{Service: textOf(s.Service), Alert: textOf(s.Alert), Route: textOf(s.Route), Strategy: textOf(s.Strategy)}
}

// userIDOf returns a pointer to the API's form of a user ID, or nil.
func userIDOf(id *uuid.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}

	return id
}

// nonEmptyPtr returns a pointer to s, or nil when s is empty.
func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// lessonActor is the caller as a lesson change records them.
func lessonActor(ctx context.Context) lessons.Actor {
	p, _ := auth.PrincipalFrom(ctx)

	actor := lessons.Actor{Kind: actorKindOf(p), ID: p.ID.String()}
	if p.Kind == auth.KindUser {
		id := p.ID
		actor.UserID = &id
	}

	return actor
}

// lessonError turns an error of the lessons store into the problem the
// caller sees; other errors stay errors, which answer 500.
func lessonError(err error) error {
	if invalid, ok := errors.AsType[*lessons.InvalidError](err); ok {
		return &Error{
			Status: http.StatusUnprocessableEntity, Code: codeValidation, Detail: invalid.Detail + ".",
			Fields: []problem.FieldError{{Field: invalid.Field, Message: invalid.Detail}},
		}
	}

	switch {
	case errors.Is(err, lessons.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeNotFound, Detail: "No lesson has this ID."}
	case errors.Is(err, lessons.ErrStale):
		return &Error{
			Status: http.StatusConflict, Code: codeStale,
			Detail: "The lesson was changed since you read it. Reload it to see the new version.",
		}
	case errors.Is(err, lessons.ErrNotProposed):
		return &Error{Status: http.StatusConflict, Code: codeNotProposed, Detail: "The lesson does not wait for review."}
	default:
		return fmt.Errorf("change a lesson: %w", err)
	}
}

// lessonCursor is where a page of lessons starts: after the lesson created
// at createdAt with id.
type lessonCursor struct {
	createdAt time.Time
	id        uuid.UUID
}

// String encodes c for the nextCursor of a page.
func (c lessonCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.createdAt.UnixMicro(), 10) + "." + c.id.String()))
}

// parseLessonCursor decodes what lessonCursor.String encoded.
func parseLessonCursor(s string) (lessonCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return lessonCursor{}, errBadLessonCursor
	}

	parts := strings.SplitN(string(raw), ".", cursorParts)
	if len(parts) != cursorParts {
		return lessonCursor{}, errBadLessonCursor
	}

	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return lessonCursor{}, errBadLessonCursor
	}

	id, err := uuid.Parse(parts[1])
	if err != nil {
		return lessonCursor{}, errBadLessonCursor
	}

	return lessonCursor{createdAt: time.UnixMicro(micros).UTC(), id: id}, nil
}

// usedLessonsOf is the API's view of the lessons an investigation used.
func usedLessonsOf(used []lessons.Stored) *[]apigen.UsedLesson {
	out := make([]apigen.UsedLesson, len(used))
	for i, l := range used {
		out[i] = apigen.UsedLesson{Id: l.ID, Text: l.Text, Scope: lessonScopeView(l.Scope), Status: apigen.LessonStatus(l.Status)}
	}

	return &out
}

// stepLessonsOf is the API's view of the lessons record of a step.
func stepLessonsOf(raw []byte) (apigen.StepLessons, error) {
	var rec lessons.StepRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return apigen.StepLessons{}, fmt.Errorf("decode: %w", err)
	}

	out := apigen.StepLessons{
		Used: make([]openapi_types.UUID, len(rec.Used)), LeftOut: make([]apigen.LeftOutLesson, len(rec.LeftOut)),
		LeftOutTotal: rec.LeftOutTotal, Candidates: rec.Candidates,
	}

	copy(out.Used, rec.Used)

	for i, l := range rec.LeftOut {
		out.LeftOut[i] = apigen.LeftOutLesson{Id: l.ID, Reason: apigen.LeftOutLessonReason(l.Reason), Scope: lessonScopeView(l.Scope)}
	}

	return out, nil
}
