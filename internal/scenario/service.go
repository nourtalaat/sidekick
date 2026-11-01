package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The audit actions of the scenarios.
const (
	ActionDrafted    = "scenario.drafted"
	ActionUpdated    = "scenario.updated"
	ActionReviewed   = "scenario.reviewed"
	ActionDownloaded = "scenario.downloaded"
)

// auditTarget is the target kind of the audit entries.
const auditTarget = "scenario"

var (
	// ErrInvestigationNotFound means there is no such investigation.
	ErrInvestigationNotFound = errors.New("investigation not found")
	// ErrAlreadyReviewed means the scenario is reviewed at that revision
	// already.
	ErrAlreadyReviewed = errors.New("the scenario is reviewed already")
	// ErrInvalid wraps the problems of a scenario file an editor saved.
	ErrInvalid = errors.New("invalid scenario")
)

// Actor is who makes a change, for the audit log.
type Actor struct {
	// Kind is an audit actor kind, such as audit.ActorUser.
	Kind string
	// ID names the actor in the audit log.
	ID string
	// UserID is the user's ID, and nil for an actor that is not a user.
	UserID *uuid.UUID
}

// Options configure a Service.
type Options struct {
	Pool    *pgxpool.Pool
	Cleaner *scrub.Cleaner
	// Rubric drafts rubric items. Nil leaves the rubric of every draft
	// empty.
	Rubric RubricDrafter
	Audit  *audit.Writer
	// Webhooks and Sources are the config's intake endpoints and sources,
	// which name the adapter of an event and the type of a source.
	Webhooks map[string]config.Webhook
	Sources  map[string]config.Section
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Service exports, edits and reviews scenarios.
type Service struct {
	pool     *pgxpool.Pool
	cleaner  *scrub.Cleaner
	builder  *Builder
	audit    *audit.Writer
	webhooks map[string]config.Webhook
	sources  map[string]config.Section
	now      func() time.Time
}

// NewService returns a Service.
func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Service{
		pool: opts.Pool, cleaner: opts.Cleaner, builder: NewBuilder(opts.Cleaner, opts.Rubric),
		audit: opts.Audit, webhooks: opts.Webhooks, sources: opts.Sources, now: now,
	}
}

// Export builds the draft of a confirmed investigation, saves it and
// returns it with true. An investigation that has a scenario already gets
// that scenario back, with false.
func (s *Service) Export(ctx context.Context, investigationID uuid.UUID, actor Actor) (db.Scenario, bool, error) {
	var (
		row     db.Scenario
		created bool
	)

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error

		row, created, err = s.export(ctx, tx, investigationID, actor)
		if err != nil {
			return fmt.Errorf("export: %w", err)
		}

		return nil
	})
	if err != nil {
		return db.Scenario{}, false, fmt.Errorf("export investigation %s: %w", investigationID, err)
	}

	return row, created, nil
}

// Update replaces the file of the scenario at revision, after checking it,
// and puts the scenario back to draft.
func (s *Service) Update(ctx context.Context, id string, revision int32, body []byte, actor Actor) (db.Scenario, error) {
	sc, err := s.check(body)
	if err != nil {
		return db.Scenario{}, fmt.Errorf("check the file: %w", err)
	}

	if sc.ID != id {
		return db.Scenario{}, fmt.Errorf("%w: id: it is %q and cannot change", ErrInvalid, id)
	}

	var row db.Scenario

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)

		row, err = q.UpdateScenarioBody(ctx, db.UpdateScenarioBodyParams{Title: sc.Title, Body: string(body), Now: s.now(), ID: id, Revision: revision})
		if err != nil {
			return s.missing(ctx, q, id, err)
		}

		return s.record(ctx, tx, actor, ActionUpdated, row)
	})
	if err != nil {
		return db.Scenario{}, fmt.Errorf("update scenario %s: %w", id, err)
	}

	return row, nil
}

// Review marks the draft at revision as reviewed by the user. checked is
// the reviewer's confirmation that they checked the scenario for sensitive
// data; without it nothing changes. The file is checked again first.
func (s *Service) Review(ctx context.Context, id string, revision int32, checked bool, reviewer uuid.UUID, actor Actor) (db.Scenario, error) {
	if !checked {
		return db.Scenario{}, ErrNotChecked
	}

	var row db.Scenario

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)

		current, err := q.GetScenario(ctx, id)
		if err != nil {
			return s.missing(ctx, q, id, err)
		}

		if current.Revision != revision {
			return ErrStale
		}

		if current.Status == statusReviewed {
			return ErrAlreadyReviewed
		}

		if _, err := s.check([]byte(current.Body)); err != nil {
			return fmt.Errorf("check the file: %w", err)
		}

		row, err = q.ReviewScenario(ctx, db.ReviewScenarioParams{ReviewedBy: &reviewer, Now: s.now(), ID: id, Revision: revision})
		if err != nil {
			return s.missing(ctx, q, id, err)
		}

		return s.record(ctx, tx, actor, ActionReviewed, row)
	})
	if err != nil {
		return db.Scenario{}, fmt.Errorf("review scenario %s: %w", id, err)
	}

	return row, nil
}

// Download returns the file of a reviewed scenario and writes the download
// to the audit log. A draft gives ErrNotReviewed.
func (s *Service) Download(ctx context.Context, id string, actor Actor) ([]byte, error) {
	var body []byte

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := reviewedRow(ctx, db.New(tx), id)
		if err != nil {
			return fmt.Errorf("read the reviewed scenario: %w", err)
		}

		body = []byte(row.Body)

		return s.record(ctx, tx, actor, ActionDownloaded, row)
	})
	if err != nil {
		return nil, fmt.Errorf("download scenario %s: %w", id, err)
	}

	return body, nil
}

// Summary is one scenario in a list.
type Summary struct {
	ID                string
	FromInvestigation *uuid.UUID
	Title             string
	Status            string
	Revision          int32
	CreatedAt         time.Time
	ReviewedAt        *time.Time
	UpdatedAt         time.Time
}

// Cursor says where a page of scenarios starts: after the scenario with ID
// that was updated at UpdatedAt.
type Cursor struct {
	UpdatedAt time.Time
	ID        string
}

// List returns a page of at most limit scenarios, newest update first, of
// one status or, when status is empty, of all. It returns the cursor of the
// next page, or nil when this is the last.
func (s *Service) List(ctx context.Context, status string, after *Cursor, limit int) ([]Summary, *Cursor, error) {
	q := db.New(s.pool)

	var (
		at *time.Time
		id *string
	)

	if after != nil {
		at, id = &after.UpdatedAt, &after.ID
	}

	size := int32(limit + 1) //nolint:gosec // The caller bounds limit.

	var out []Summary

	if status == "" {
		rows, err := q.ListScenarios(ctx, db.ListScenariosParams{CursorAt: at, CursorID: id, PageSize: size})
		if err != nil {
			return nil, nil, fmt.Errorf("list scenarios: %w", err)
		}

		for _, r := range rows {
			out = append(out, Summary(r))
		}
	} else {
		rows, err := q.ListScenariosByStatus(ctx, db.ListScenariosByStatusParams{Status: status, CursorAt: at, CursorID: id, PageSize: size})
		if err != nil {
			return nil, nil, fmt.Errorf("list scenarios of status %s: %w", status, err)
		}

		for _, r := range rows {
			out = append(out, Summary(r))
		}
	}

	if len(out) <= limit {
		return out, nil, nil
	}

	out = out[:limit]
	last := out[limit-1]

	return out, &Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}, nil
}

// export is Export in a transaction.
func (s *Service) export(ctx context.Context, tx pgx.Tx, investigationID uuid.UUID, actor Actor) (db.Scenario, bool, error) {
	q := db.New(tx)

	inv, err := q.GetInvestigation(ctx, investigationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Scenario{}, false, ErrInvestigationNotFound
	}

	if err != nil {
		return db.Scenario{}, false, fmt.Errorf("read the investigation: %w", err)
	}

	existing, err := q.GetScenarioByInvestigation(ctx, &investigationID)
	if err == nil {
		return existing, false, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Scenario{}, false, fmt.Errorf("look for a scenario of the investigation: %w", err)
	}

	if inv.ConfirmedVerdict == nil {
		return db.Scenario{}, false, ErrNotConfirmed
	}

	in, err := s.input(ctx, q, inv)
	if err != nil {
		return db.Scenario{}, false, fmt.Errorf("gather the investigation: %w", err)
	}

	draft, err := s.builder.Build(ctx, tx, in)
	if err != nil {
		return db.Scenario{}, false, fmt.Errorf("build the draft: %w", err)
	}

	return s.save(ctx, tx, inv, draft, actor)
}

// save stores a draft. When another request stored the investigation's
// scenario first, it returns that one instead.
func (s *Service) save(ctx context.Context, tx pgx.Tx, inv db.Investigation, draft Draft, actor Actor) (db.Scenario, bool, error) {
	q := db.New(tx)

	provenance, err := json.Marshal(draft.Provenance)
	if err != nil {
		return db.Scenario{}, false, fmt.Errorf("encode the provenance: %w", err)
	}

	notes, err := json.Marshal(draft.Notes)
	if err != nil {
		return db.Scenario{}, false, fmt.Errorf("encode the notes: %w", err)
	}

	row, err := q.InsertScenario(ctx, db.InsertScenarioParams{
		ID: draft.ID, FromInvestigation: &inv.ID, Title: draft.Title, Body: string(draft.Body),
		Provenance: provenance, Notes: notes, CreatedBy: actor.UserID, Now: s.now(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := q.GetScenarioByInvestigation(ctx, &inv.ID)
		if gerr != nil {
			return db.Scenario{}, false, fmt.Errorf("read the scenario another request stored: %w", gerr)
		}

		return existing, false, nil
	}

	if err != nil {
		return db.Scenario{}, false, fmt.Errorf("store the draft: %w", err)
	}

	if err := s.record(ctx, tx, actor, ActionDrafted, row); err != nil {
		return db.Scenario{}, false, fmt.Errorf("record the draft: %w", err)
	}

	return row, true, nil
}

// input gathers what the builder needs from the investigation.
func (s *Service) input(ctx context.Context, q *db.Queries, inv db.Investigation) (Input, error) {
	ev, err := q.GetFirstAlertEventOfGroup(ctx, inv.GroupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Input{}, ErrNoEvent
	}

	if err != nil {
		return Input{}, fmt.Errorf("read the first alert: %w", err)
	}

	hook, ok := s.webhooks[ev.Intake]
	if !ok {
		return Input{}, fmt.Errorf("%w: %s", ErrUnknownAdapter, ev.Intake)
	}

	steps, err := q.ListSteps(ctx, inv.ID)
	if err != nil {
		return Input{}, fmt.Errorf("read the steps: %w", err)
	}

	calls, err := investigator.RecordedCalls(steps)
	if err != nil {
		return Input{}, fmt.Errorf("read the source calls: %w", err)
	}

	types := make(map[string]string, len(s.sources))
	for name, section := range s.sources {
		types[name] = section.Type
	}

	return Input{
		InvestigationID: inv.ID, Route: inv.Route, Strategy: textPtr(inv.Strategy), Title: textPtr(inv.AlertTitle),
		Verdict: report.Verdict(textPtr(inv.ConfirmedVerdict)), Cause: confirmedCause(inv),
		Event:       EventInput{Adapter: hook.Type, Config: hook.Node, Payload: ev.Raw, ReceivedAt: ev.ReceivedAt},
		Calls:       calls,
		SourceTypes: types,
	}, nil
}

// check reads a scenario file and fails when it is not a valid scenario or
// holds an email, a host name or an IP address.
func (s *Service) check(body []byte) (bench.Scenario, error) {
	sc, err := bench.ParseScenario(body)
	if err != nil {
		return bench.Scenario{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return bench.Scenario{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	if err := s.cleaner.Check(&doc); err != nil {
		return bench.Scenario{}, fmt.Errorf("check for sensitive data: %w", err)
	}

	return sc, nil
}

// missing turns "no rows" from a conditional update into ErrNotFound when
// there is no such scenario, and ErrStale when it exists at another
// revision.
func (s *Service) missing(ctx context.Context, q *db.Queries, id string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("write the scenario: %w", err)
	}

	if _, gerr := q.GetScenario(ctx, id); errors.Is(gerr, pgx.ErrNoRows) {
		return ErrNotFound
	} else if gerr != nil {
		return fmt.Errorf("read the scenario: %w", gerr)
	}

	return ErrStale
}

// record writes the audit entry of a change. It holds the scenario's
// status and revision, never its file.
func (s *Service) record(ctx context.Context, q db.DBTX, actor Actor, action string, row db.Scenario) error {
	err := s.audit.Write(ctx, q, audit.Entry{
		ActorKind: actor.Kind, ActorID: actor.ID, Action: action, TargetKind: auditTarget, TargetID: row.ID,
		InvestigationID: row.FromInvestigation, New: map[string]any{"status": row.Status, "revision": row.Revision},
	})
	if err != nil {
		return fmt.Errorf("write the audit entry: %w", err)
	}

	return nil
}

// confirmedCause returns the cause people confirmed, or the report's cause
// when they confirmed the report's verdict without writing one.
func confirmedCause(inv db.Investigation) string {
	if cause := textPtr(inv.ConfirmedCause); cause != "" {
		return cause
	}

	var rep report.Report
	if len(inv.Report) == 0 || json.Unmarshal(inv.Report, &rep) != nil {
		return ""
	}

	if string(rep.Verdict) == textPtr(inv.ConfirmedVerdict) {
		return rep.Cause
	}

	return ""
}

// textPtr returns the text s points to, or "".
func textPtr(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
