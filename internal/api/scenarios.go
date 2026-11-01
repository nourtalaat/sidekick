package api

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/scenario"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The page sizes of GET /scenarios.
const (
	defaultScenarioPage = 50
	maxScenarioPage     = 100
)

// The codes of the problems that scenarios answer with.
const (
	codeNotConfirmed    = "not_confirmed"
	codeNotReviewed     = "not_reviewed"
	codeAlreadyReviewed = "already_reviewed"
	codeSensitiveData   = "sensitive_data"
	codeNoReplay        = "no_replay"
)

// CreateScenarioDraft exports a confirmed investigation as a scenario draft.
func (s *server) CreateScenarioDraft(ctx context.Context, req apigen.CreateScenarioDraftRequestObject) (apigen.CreateScenarioDraftResponseObject, error) {
	if s.scenarios == nil {
		return nil, errNoScenarios
	}

	row, created, err := s.scenarios.Export(ctx, req.Id, scenarioActor(ctx))
	if err != nil {
		return nil, scenarioError(err)
	}

	out, err := s.scenarioOf(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("describe scenario %s: %w", row.ID, err)
	}

	if created {
		return apigen.CreateScenarioDraft201JSONResponse(out), nil
	}

	return apigen.CreateScenarioDraft200JSONResponse(out), nil
}

// ListScenarios returns a page of scenarios, most recently changed first.
func (s *server) ListScenarios(ctx context.Context, req apigen.ListScenariosRequestObject) (apigen.ListScenariosResponseObject, error) {
	if s.scenarios == nil {
		return nil, errNoScenarios
	}

	limit := defaultScenarioPage
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}

	if limit < 1 || limit > maxScenarioPage {
		return nil, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxScenarioPage)
	}

	after, err := parseScenarioCursor(req.Params.Cursor)
	if err != nil {
		return nil, fmt.Errorf("read the cursor: %w", err)
	}

	status := ""

	if req.Params.Status != nil {
		status = string(*req.Params.Status)
		if status != scenario.StatusDraft && status != scenario.StatusReviewed {
			return nil, problemf(http.StatusBadRequest, "status must be draft or reviewed.")
		}
	}

	rows, next, err := s.scenarios.List(ctx, status, after, limit)
	if err != nil {
		return nil, scenarioError(err)
	}

	list := apigen.ScenarioList{Scenarios: make([]apigen.Scenario, len(rows))}

	for i, r := range rows {
		list.Scenarios[i] = scenarioView(r.ID, r.FromInvestigation, r.Title, r.Status, r.Revision, r.CreatedAt, r.UpdatedAt, r.ReviewedAt, nil, nil)
	}

	if next != nil {
		cursor := strconv.FormatInt(next.UpdatedAt.UnixNano(), 10) + "." + next.ID
		list.NextCursor = &cursor
	}

	return apigen.ListScenarios200JSONResponse(list), nil
}

// GetScenario returns a scenario with what a reviewer needs to check it.
func (s *server) GetScenario(ctx context.Context, req apigen.GetScenarioRequestObject) (apigen.GetScenarioResponseObject, error) {
	if s.scenarios == nil {
		return nil, errNoScenarios
	}

	detail, err := s.scenarios.Detail(ctx, req.Id)
	if err != nil {
		return nil, scenarioError(err)
	}

	scenarioRow, err := s.scenarioOf(ctx, detail.Scenario)
	if err != nil {
		return nil, fmt.Errorf("describe scenario %s: %w", req.Id, err)
	}

	out := apigen.ScenarioDetail{
		Scenario:   scenarioRow,
		Body:       detail.Scenario.Body,
		Notes:      make([]apigen.ScenarioNote, len(detail.Notes)),
		Candidates: make([]apigen.ScenarioCandidate, len(detail.Candidates)),
		Pairs:      make([]apigen.ScenarioPair, len(detail.Pairs)),
	}

	for i, n := range detail.Notes {
		out.Notes[i] = apigen.ScenarioNote{Kind: n.Kind, Text: n.Text}
	}

	for i, c := range detail.Candidates {
		out.Candidates[i] = apigen.ScenarioCandidate{Path: c.Path, Start: c.Start, End: c.End, Reason: c.Reason}
	}

	for i, p := range detail.Pairs {
		out.Pairs[i] = apigen.ScenarioPair{Ref: p.Ref, Label: p.Label, Original: p.Original, Cleaned: p.Cleaned, Note: nonEmpty(&p.Note)}
	}

	return apigen.GetScenario200JSONResponse(out), nil
}

// UpdateScenario replaces the file of a scenario and makes it a draft.
func (s *server) UpdateScenario(ctx context.Context, req apigen.UpdateScenarioRequestObject) (apigen.UpdateScenarioResponseObject, error) {
	if s.scenarios == nil {
		return nil, errNoScenarios
	}

	row, err := s.scenarios.Update(ctx, req.Id, req.Body.Revision, []byte(req.Body.Body), scenarioActor(ctx))
	if err != nil {
		return nil, scenarioError(err)
	}

	out, err := s.scenarioOf(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("describe scenario %s: %w", row.ID, err)
	}

	return apigen.UpdateScenario200JSONResponse(out), nil
}

// ReviewScenario marks a draft as reviewed by the signed-in user.
func (s *server) ReviewScenario(ctx context.Context, req apigen.ReviewScenarioRequestObject) (apigen.ReviewScenarioResponseObject, error) {
	if s.scenarios == nil {
		return nil, errNoScenarios
	}

	p, _ := auth.PrincipalFrom(ctx)
	if p.Kind != auth.KindUser {
		return nil, problemf(http.StatusForbidden, "A scenario is reviewed by a person who is signed in, not by an API token.")
	}

	row, err := s.scenarios.Review(ctx, req.Id, req.Body.Revision, req.Body.SensitiveDataChecked, p.ID, scenarioActor(ctx))
	if err != nil {
		return nil, scenarioError(err)
	}

	out, err := s.scenarioOf(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("describe scenario %s: %w", row.ID, err)
	}

	return apigen.ReviewScenario200JSONResponse(out), nil
}

// GetScenarioFile downloads a reviewed scenario.
func (s *server) GetScenarioFile(ctx context.Context, req apigen.GetScenarioFileRequestObject) (apigen.GetScenarioFileResponseObject, error) {
	if s.scenarios == nil {
		return nil, errNoScenarios
	}

	body, err := s.scenarios.Download(ctx, req.Id, scenarioActor(ctx))
	if err != nil {
		return nil, scenarioError(err)
	}

	return scenarioFile{id: req.Id, body: body}, nil
}

// scenarioFile is the answer to a download: the file as an attachment.
type scenarioFile struct {
	id   string
	body []byte
}

// VisitGetScenarioFileResponse writes the file.
func (f scenarioFile) VisitGetScenarioFileResponse(w http.ResponseWriter) error {
	h := w.Header()
	h.Set("Content-Type", "application/yaml")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.id + ".yaml"}))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(f.body); err != nil {
		return fmt.Errorf("write the scenario file: %w", err)
	}

	return nil
}

// errNoScenarios answers when the server was set up without the scenario
// service.
var errNoScenarios = problemf(http.StatusServiceUnavailable, "Scenario export is not available on this server.")

// scenarioOf is the API's view of a stored scenario.
func (s *server) scenarioOf(ctx context.Context, row db.Scenario) (apigen.Scenario, error) {
	names, err := s.reviewerNames(ctx, optionalIDs(row.ReviewedBy))
	if err != nil {
		return apigen.Scenario{}, fmt.Errorf("read the reviewer: %w", err)
	}

	return scenarioView(row.ID, row.FromInvestigation, row.Title, row.Status, row.Revision, row.CreatedAt, row.UpdatedAt, row.ReviewedAt, row.ReviewedBy, names), nil
}

// scenarioView builds the API's view from a scenario's columns.
func scenarioView(id string, investigation *uuid.UUID, title, status string, revision int32, created, updated time.Time, reviewedAt *time.Time, reviewedBy *uuid.UUID, names map[uuid.UUID]string) apigen.Scenario {
	out := apigen.Scenario{
		Id: id, Title: title, Status: apigen.ScenarioStatus(status), Revision: revision,
		CreatedAt: created, UpdatedAt: updated, ReviewedAt: reviewedAt,
	}

	if investigation != nil {
		out.InvestigationId = investigation
	}

	if reviewedBy != nil {
		if name, ok := names[*reviewedBy]; ok {
			out.ReviewedBy = &name
		}
	}

	return out
}

// reviewerNames returns the names of the users with ids.
func (s *server) reviewerNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	names := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return names, nil
	}

	rows, err := db.New(s.pool).ListUserNames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read the names of the reviewers: %w", err)
	}

	for _, r := range rows {
		names[r.ID] = r.Name
	}

	return names, nil
}

// optionalIDs returns the ID id points to as a list, or none.
func optionalIDs(id *uuid.UUID) []uuid.UUID {
	if id == nil {
		return nil
	}

	return []uuid.UUID{*id}
}

// scenarioActor is who makes the change that ctx's request asks for.
func scenarioActor(ctx context.Context) scenario.Actor {
	p, _ := auth.PrincipalFrom(ctx)

	actor := scenario.Actor{Kind: actorKindOf(p), ID: p.ID.String()}
	if p.Kind == auth.KindUser {
		actor.UserID = &p.ID
	}

	return actor
}

// parseScenarioCursor decodes the nextCursor of GET /scenarios.
func parseScenarioCursor(cursor *string) (*scenario.Cursor, error) {
	if cursor == nil || *cursor == "" {
		return nil, nil //nolint:nilnil // No cursor means the first page.
	}

	nanos, id, ok := strings.Cut(*cursor, ".")

	n, err := strconv.ParseInt(nanos, 10, 64)
	if !ok || err != nil || id == "" {
		return nil, problemf(http.StatusBadRequest, "The cursor is not one this server gave.")
	}

	return &scenario.Cursor{UpdatedAt: time.Unix(0, n).UTC(), ID: id}, nil
}

// scenarioError turns an error of the scenario service into the problem the
// caller sees; other errors stay errors, which answer 500.
func scenarioError(err error) error {
	var (
		invalid *scenario.InvalidError
		leak    *scrub.LeakError
	)

	switch {
	case errors.Is(err, scenario.ErrInvestigationNotFound):
		return errNoInvestigation
	case errors.Is(err, scenario.ErrNotFound):
		return problemf(http.StatusNotFound, "No such scenario.")
	case errors.Is(err, scenario.ErrNotConfirmed):
		return &Error{Status: http.StatusConflict, Code: codeNotConfirmed, Detail: "Only an investigation whose verdict people confirmed can be exported as a scenario."}
	case errors.Is(err, scenario.ErrNoEvent):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeNoReplay, Detail: "The alert that opened this investigation has no original payload to replay. It came from a Slack or Teams message, or was stored before payloads were kept."}
	case errors.Is(err, scenario.ErrUnknownAdapter):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeNoReplay, Detail: "The webhook endpoint the alert came in on is no longer in the config, so its adapter is not known."}
	case errors.Is(err, scenario.ErrNoSourceCalls):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeNoReplay, Detail: "The investigation called no source tool, so there is nothing to replay."}
	case errors.Is(err, scenario.ErrStale):
		return &Error{Status: http.StatusConflict, Code: codeStale, Detail: "The scenario changed since you read it. Reload it to see the new version."}
	case errors.Is(err, scenario.ErrNotReviewed):
		return &Error{Status: http.StatusConflict, Code: codeNotReviewed, Detail: "The scenario is a draft. Only a reviewed scenario can be run or downloaded."}
	case errors.Is(err, scenario.ErrAlreadyReviewed):
		return &Error{Status: http.StatusConflict, Code: codeAlreadyReviewed, Detail: "The scenario is reviewed already."}
	case errors.Is(err, scenario.ErrNotChecked):
		return &Error{
			Status: http.StatusUnprocessableEntity, Code: codeValidation, Detail: "Check the scenario for sensitive data and say so.",
			Fields: []problem.FieldError{{Field: "sensitiveDataChecked", Message: "must be true: the reviewer checked the scenario for sensitive data"}},
		}
	case errors.As(err, &leak):
		return leakError(leak)
	case errors.As(err, &invalid):
		return invalidError(invalid)
	}

	return fmt.Errorf("scenarios: %w", err)
}

// leakError answers a file that still holds an email, a host name or an IP
// address. It names where, never the value.
func leakError(leak *scrub.LeakError) error {
	fields := make([]problem.FieldError, len(leak.Leaks))
	for i, l := range leak.Leaks {
		fields[i] = problem.FieldError{Field: l.Path, Message: "still holds something that looks like an " + leakWord(l.Kind)}
	}

	return &Error{
		Status: http.StatusUnprocessableEntity, Code: codeSensitiveData, Fields: fields,
		Detail: "The scenario still holds values that look like emails, host names or IP addresses. Replace them with placeholders such as host-1.",
	}
}

// leakWord is the noun for a kind of leak.
func leakWord(kind string) string {
	switch kind {
	case "email":
		return "email address"
	case "ip":
		return "IP address"
	}

	return "host name"
}

// invalidError answers a file that is not a valid scenario.
func invalidError(invalid *scenario.InvalidError) error {
	fields := make([]problem.FieldError, 0, len(invalid.Problems))

	for _, line := range invalid.Problems {
		field, message, ok := strings.Cut(line, ": ")
		if !ok {
			field, message = "", line
		}

		fields = append(fields, problem.FieldError{Field: field, Message: message})
	}

	return &Error{Status: http.StatusUnprocessableEntity, Code: codeValidation, Detail: "The scenario file is not valid.", Fields: fields}
}
