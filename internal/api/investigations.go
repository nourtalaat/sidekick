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
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The page sizes of GET /investigations.
const (
	defaultInvestigationsPage = 50
	maxInvestigationsPage     = 200
)

const (
	// maxSearchChars is the longest q of GET /investigations.
	maxSearchChars = 200
	// maxDetailEvents is how many events of the group the investigation shows.
	maxDetailEvents = 50
	// maxDetailFollowUps is how many waiting follow-up questions it shows.
	maxDetailFollowUps = 50
	// maxEventBodyBytes is how much of an event's text it shows.
	maxEventBodyBytes = 2000
	// cursorParts is the number of parts of a cursor: a time and an ID.
	cursorParts = 2
)

// investigatorRole is the role of an investigation's connectors that
// holds the model it investigates with.
const investigatorRole = "investigate"

// errBadInvestigationCursor means a cursor is not one GET /investigations
// returned.
var errBadInvestigationCursor = errors.New("not a cursor")

// errNoInvestigation answers a request for an investigation that does not
// exist.
var errNoInvestigation = problemf(http.StatusNotFound, "No investigation has this ID.")

// knownVerdicts are the verdicts the list can filter by.
var knownVerdicts = []report.Verdict{
	report.TruePositive, report.FalsePositive, report.Inconclusive, report.Duplicate, report.KnownIssue, report.Flapping,
}

// knownStatuses are the statuses the list can filter by.
var knownStatuses = []string{
	investigator.StatusQueued, investigator.StatusRunning, investigator.StatusFinal, investigator.StatusFailed, investigator.StatusCancelled,
}

// investigationCursor is where a page starts: after the investigation
// created at createdAt with id.
type investigationCursor struct {
	createdAt time.Time
	id        uuid.UUID
}

// ListInvestigations returns a page of investigations, newest first.
func (s *server) ListInvestigations(ctx context.Context, req apigen.ListInvestigationsRequestObject) (apigen.ListInvestigationsResponseObject, error) {
	params, limit, err := listParams(req.Params)
	if err != nil {
		return nil, err
	}

	rows, err := db.New(customPlan{db: s.pool}).ListInvestigations(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list investigations: %w", err)
	}

	list := apigen.ListInvestigations200JSONResponse{Investigations: make([]apigen.InvestigationSummary, 0, min(len(rows), limit))}

	for i, row := range rows {
		if i == limit {
			cursor := investigationCursor{createdAt: rows[i-1].CreatedAt, id: rows[i-1].ID}.String()
			list.NextCursor = &cursor

			break
		}

		list.Investigations = append(list.Investigations, summaryOf(row))
	}

	return list, nil
}

// GetInvestigation returns one investigation, with its report, the first
// events of its group and its waiting follow-up questions.
func (s *server) GetInvestigation(ctx context.Context, req apigen.GetInvestigationRequestObject) (apigen.GetInvestigationResponseObject, error) {
	queries := db.New(s.pool)

	row, err := queries.GetInvestigationDetail(ctx, req.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoInvestigation
	}

	if err != nil {
		return nil, fmt.Errorf("read investigation %s: %w", req.Id, err)
	}

	inv, err := investigationOf(row)
	if err != nil {
		return nil, fmt.Errorf("investigation %s: %w", req.Id, err)
	}

	events, err := queries.ListGroupEvents(ctx, db.ListGroupEventsParams{GroupID: &row.Investigation.GroupID, PageSize: maxDetailEvents})
	if err != nil {
		return nil, fmt.Errorf("list the events of investigation %s: %w", req.Id, err)
	}

	inv.Events, err = eventsOf(events)
	if err != nil {
		return nil, fmt.Errorf("investigation %s: %w", req.Id, err)
	}

	pending, err := queries.ListPendingFollowUps(ctx, db.ListPendingFollowUpsParams{InvestigationID: req.Id, PageSize: maxDetailFollowUps})
	if err != nil {
		return nil, fmt.Errorf("list the follow-up questions of investigation %s: %w", req.Id, err)
	}

	inv.PendingFollowUps, err = pendingFollowUpsOf(pending)
	if err != nil {
		return nil, fmt.Errorf("investigation %s: %w", req.Id, err)
	}

	return apigen.GetInvestigation200JSONResponse(inv), nil
}

// listParams checks the filters of GET /investigations and returns the
// query's parameters, which ask for one row more than the page size, and
// the page size.
func listParams(p apigen.ListInvestigationsParams) (db.ListInvestigationsParams, int, error) {
	limit := defaultInvestigationsPage
	if p.Limit != nil {
		limit = *p.Limit
	}

	if limit < 1 || limit > maxInvestigationsPage {
		return db.ListInvestigationsParams{}, 0, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxInvestigationsPage)
	}

	if p.Verdict != nil && !slices.Contains(knownVerdicts, report.Verdict(*p.Verdict)) {
		return db.ListInvestigationsParams{}, 0, problemf(http.StatusBadRequest, "verdict must be one of %s.", joinVerdicts())
	}

	if p.Status != nil && !slices.Contains(knownStatuses, string(*p.Status)) {
		return db.ListInvestigationsParams{}, 0, problemf(http.StatusBadRequest, "status must be one of %s.", strings.Join(knownStatuses, ", "))
	}

	if p.Q != nil && len([]rune(*p.Q)) > maxSearchChars {
		return db.ListInvestigationsParams{}, 0, problemf(http.StatusBadRequest, "q must be at most %d characters.", maxSearchChars)
	}

	params := db.ListInvestigationsParams{
		CreatedFrom: p.From,
		CreatedTo:   p.To,
		Route:       nonEmpty(p.Route),
		Service:     nonEmpty(p.Service),
		Model:       nonEmpty(p.Model),
		Search:      nonEmpty(p.Q),
		PageSize:    int32(limit + 1), //nolint:gosec // limit is at most maxInvestigationsPage.
	}

	if p.Verdict != nil {
		params.Verdict = new(string(*p.Verdict))
	}

	if p.Status != nil {
		params.Status = new(string(*p.Status))
	}

	if p.Cursor != nil {
		c, err := parseInvestigationCursor(*p.Cursor)
		if err != nil {
			return db.ListInvestigationsParams{}, 0, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		params.CursorAt, params.CursorID = &c.createdAt, &c.id
	}

	return params, limit, nil
}

// summaryOf is the API's view of a row of the list.
func summaryOf(row db.ListInvestigationsRow) apigen.InvestigationSummary {
	s := apigen.InvestigationSummary{
		Id:         row.ID,
		Title:      nonEmpty(row.AlertTitle),
		Service:    row.Service,
		Route:      row.Route,
		Status:     apigen.InvestigationStatus(row.Status),
		CreatedAt:  row.CreatedAt,
		StartedAt:  row.StartedAt,
		FinishedAt: row.FinishedAt,
		DurationMs: durationMs(row.StartedAt, row.FinishedAt),
		CostMicros: row.CostMicros,
		Feedback:   feedbackSummaryOf(row.Correct, row.Wrong),
	}

	if row.Verdict != nil {
		s.Verdict = new(apigen.Verdict(*row.Verdict))
	}

	if row.Confidence != nil {
		s.Confidence = new(apigen.Confidence(*row.Confidence))
	}

	if row.WatcherStatus != "" {
		s.Watcher = &apigen.WatcherSummary{Name: row.WatcherName, Status: apigen.WatcherStatus(row.WatcherStatus)}
	}

	return s
}

// investigationOf is the API's view of an investigation, without the
// events and the follow-up questions.
func investigationOf(row db.GetInvestigationDetailRow) (apigen.Investigation, error) {
	inv := row.Investigation

	out := apigen.Investigation{
		Id:                inv.ID,
		GroupId:           inv.GroupID,
		GroupStatus:       apigen.InvestigationGroupStatus(row.GroupStatus),
		GroupClosedReason: row.GroupClosedReason,
		Title:             nonEmpty(inv.AlertTitle),
		Service:           inv.Service,
		Route:             inv.Route,
		Strategy:          inv.Strategy,
		StrategyVersion:   inv.StrategyVersion,
		Mode:              inv.Mode,
		Status:            apigen.InvestigationStatus(inv.Status),
		Run:               int(inv.Run),
		RunKind:           apigen.RunKind(inv.RunKind),
		StopReason:        inv.StopReason,
		Model:             inv.Model,
		InputTokens:       inv.InputTokens,
		OutputTokens:      inv.OutputTokens,
		CostMicros:        inv.CostMicros,
		ToolCalls:         int(inv.ToolCalls),
		CreatedAt:         inv.CreatedAt,
		StartedAt:         inv.StartedAt,
		FirstFindingAt:    inv.FirstFindingAt,
		FinishedAt:        inv.FinishedAt,
		DurationMs:        durationMs(inv.StartedAt, inv.FinishedAt),
		CancelRequestedAt: inv.CancelRequestedAt,
		EventCount:        int(row.EventCount),
		Events:            []apigen.Event{},
		PendingFollowUps:  []apigen.PendingFollowUp{},
	}

	if inv.Verdict != nil {
		out.Verdict = new(apigen.Verdict(*inv.Verdict))
	}

	if inv.Confidence != nil {
		out.Confidence = new(apigen.Confidence(*inv.Confidence))
	}

	if inv.ConfirmedVerdict != nil && inv.ConfirmedAt != nil {
		out.Confirmation = &apigen.Confirmation{Verdict: apigen.Verdict(*inv.ConfirmedVerdict), Cause: inv.ConfirmedCause, At: *inv.ConfirmedAt}
	}

	connector, err := connectorOf(inv.Connectors)
	if err != nil {
		return apigen.Investigation{}, fmt.Errorf("read the connectors: %w", err)
	}

	out.Connector = connector

	if len(inv.Report) > 0 {
		rep, err := reportOf(inv.Report)
		if err != nil {
			return apigen.Investigation{}, fmt.Errorf("read the report: %w", err)
		}

		out.Report = &rep
	}

	return out, nil
}

// eventsOf is the API's view of events.
func eventsOf(rows []db.Event) ([]apigen.Event, error) {
	out := make([]apigen.Event, 0, len(rows))

	for _, row := range rows {
		ev, err := store.EventOf(row)
		if err != nil {
			return nil, fmt.Errorf("read event %s: %w", row.ID, err)
		}

		view := apigen.Event{
			Id:         row.ID,
			Kind:       apigen.EventKind(ev.Kind),
			Intake:     ev.Intake,
			AlertName:  nonEmpty(&ev.AlertName),
			Title:      nonEmpty(&ev.Title),
			Body:       nonEmpty(new(source.CutText(ev.Text, maxEventBodyBytes))),
			Service:    nonEmpty(&ev.Service),
			Severity:   nonEmpty(new(string(ev.Severity))),
			Labels:     ev.Labels,
			Links:      linksOf(ev.Links),
			ReceivedAt: ev.ReceivedAt,
		}

		if view.Labels == nil {
			view.Labels = map[string]string{}
		}

		if ev.Sender.Name != "" || ev.Sender.Kind != "" {
			view.Sender = &apigen.Sender{Kind: new(apigen.SenderKind(ev.Sender.Kind)), Name: nonEmpty(&ev.Sender.Name)}
		}

		out = append(out, view)
	}

	return out, nil
}

// pendingFollowUpsOf is the API's view of the follow-up questions that
// wait in the inbox.
func pendingFollowUpsOf(rows []db.ListPendingFollowUpsRow) ([]apigen.PendingFollowUp, error) {
	out := make([]apigen.PendingFollowUp, 0, len(rows))

	for _, row := range rows {
		var body struct {
			Text   string `json:"text"`
			Author string `json:"author"`
		}

		if err := json.Unmarshal(row.Body, &body); err != nil {
			return nil, fmt.Errorf("decode follow-up question %d: %w", row.ID, err)
		}

		out = append(out, apigen.PendingFollowUp{Text: body.Text, Author: nonEmpty(&body.Author), CreatedAt: row.CreatedAt})
	}

	return out, nil
}

// feedbackSummaryOf counts the ratings and says what they add up to.
func feedbackSummaryOf(correct, wrong int64) apigen.FeedbackSummary {
	summary := apigen.None

	switch {
	case correct > 0 && wrong > 0:
		summary = apigen.Mixed
	case correct > 0:
		summary = apigen.Correct
	case wrong > 0:
		summary = apigen.Wrong
	}

	return apigen.FeedbackSummary{Correct: int(correct), Wrong: int(wrong), Summary: summary}
}

// connectorOf returns the model connector of the investigate role in an
// investigation's connectors, if it has one.
func connectorOf(raw json.RawMessage) (*string, error) {
	var roles map[string]struct {
		Connector string `json:"connector"`
	}

	if err := json.Unmarshal(raw, &roles); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	return nonEmpty(new(roles[investigatorRole].Connector)), nil
}

// durationMs returns how long the run took, when it has ended.
func durationMs(started, finished *time.Time) *int64 {
	if started == nil || finished == nil {
		return nil
	}

	return new(finished.Sub(*started).Milliseconds())
}

// nonEmpty returns s, or nil when s is nil or empty.
func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}

	return s
}

// linksOf keeps the links with an http or https address.
func linksOf(links []event.Link) []apigen.EventLink {
	out := make([]apigen.EventLink, 0, len(links))

	for _, l := range links {
		if isWebURL(l.URL) {
			out = append(out, apigen.EventLink{Title: l.Title, Url: l.URL})
		}
	}

	return out
}

// String encodes c for the nextCursor of a page.
func (c investigationCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.createdAt.UnixMicro(), 10) + "." + c.id.String()))
}

// parseInvestigationCursor decodes what investigationCursor.String
// encoded.
func parseInvestigationCursor(s string) (investigationCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return investigationCursor{}, errBadInvestigationCursor
	}

	parts := strings.SplitN(string(raw), ".", cursorParts)
	if len(parts) != cursorParts {
		return investigationCursor{}, errBadInvestigationCursor
	}

	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return investigationCursor{}, errBadInvestigationCursor
	}

	id, err := uuid.Parse(parts[1])
	if err != nil {
		return investigationCursor{}, errBadInvestigationCursor
	}

	return investigationCursor{createdAt: time.UnixMicro(micros).UTC(), id: id}, nil
}

// joinVerdicts lists the verdicts for an error message.
func joinVerdicts() string {
	names := make([]string, len(knownVerdicts))
	for i, v := range knownVerdicts {
		names[i] = string(v)
	}

	return strings.Join(names, ", ")
}
