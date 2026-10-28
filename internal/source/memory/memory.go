package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	mem "github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// Type is the source type this package registers.
const Type = "memory"

// Limits on what one result holds.
const (
	// maxResults is the most entries one search returns.
	maxResults = 10
	// maxEntryText is the most bytes of an entry's summary or cause a
	// result keeps.
	maxEntryText = 1000
	// maxNotesText is the most bytes of a service's notes a result keeps.
	maxNotesText = 8000
)

// Texts of results with no data.
const (
	noMatchText = "No confirmed investigation matches. Only investigations whose verdict a person confirmed are in memory."
	noNotesText = "Nobody has written notes about this service."
)

var (
	// errNoDatabase is returned, wrapped in source.ErrUnavailable, by a
	// source built without a database, as config validate builds it.
	errNoDatabase = errors.New("the memory source has no database")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Config is the config section of a memory source. It has only the keys
// every source shares.
type Config struct {
	source.Settings `yaml:",inline"`
}

// Source reads Sidekick's memory. It is safe for concurrent use.
type Source struct {
	db db.DBTX
	// publicURL is server.publicURL, for links to the investigation pages.
	publicURL string
	// vectors adds the entries nearest in meaning to a search. It is set
	// when vector memory is on, and nil otherwise.
	vectors atomic.Pointer[mem.Vectors]
}

var _ source.Source = (*Source)(nil)

// UseVectors makes searches merge the entries nearest in meaning with the
// full-text matches. Call it once vector memory is set up; with nil, or
// never, a search is full text only.
func (s *Source) UseVectors(v *mem.Vectors) {
	s.vectors.Store(v)
}

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A search that matches nothing, or a service without
// notes, returns a result with NoData set.
func (s *Source) Call(ctx context.Context, call source.Call) (source.Result, error) {
	if s.db == nil {
		return source.Result{}, fmt.Errorf("call %s: %w: %w", call.Tool, source.ErrUnavailable, errNoDatabase)
	}

	var (
		res source.Result
		err error
	)

	switch call.Tool {
	case toolSearch:
		res, err = s.search(ctx, call.Input)
	case toolServiceNotes:
		res, err = s.serviceNotes(ctx, call.Input)
	default:
		err = fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}

	if err != nil {
		return source.Result{}, fmt.Errorf("call %s: %w", call.Tool, err)
	}

	return res, nil
}

// Close releases nothing: the database pool belongs to the app.
func (s *Source) Close() error {
	return nil
}

// search runs search.
func (s *Source) search(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[searchInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	// TODO: [SK-41] search the active lessons too
	rows, err := mem.Search(ctx, s.db, mem.SearchParams{
		Query:   in.Query,
		Service: nonEmpty(in.Service),
		Verdict: nonEmpty(in.Verdict),
	}, maxResults, s.vectors.Load())
	if err != nil {
		return source.Result{}, fmt.Errorf("search memory: %w", err)
	}

	if len(rows) == 0 {
		return source.Result{NoData: true, Text: noMatchText}, nil
	}

	out := searchResult{Entries: make([]entry, 0, len(rows))}
	evidence := make([]source.Evidence, 0, len(rows))

	var truncated bool

	for _, r := range rows {
		e := entry{
			InvestigationID: r.InvestigationID.String(),
			Verdict:         r.Verdict,
			Service:         deref(r.Service),
			AlertName:       deref(r.AlertName),
			Summary:         source.CutText(r.Summary, maxEntryText),
			Cause:           source.CutText(deref(r.Cause), maxEntryText),
			ConfirmedAt:     r.ConfirmedAt.UTC(),
		}
		truncated = truncated || len(e.Summary) < len(r.Summary) || len(e.Cause) < len(deref(r.Cause))

		out.Entries = append(out.Entries, e)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceIncident,
			Title: "Confirmed investigation " + e.InvestigationID,
			URL:   weblink.Investigation(s.publicURL, e.InvestigationID),
			At:    e.ConfirmedAt,
		})
	}

	return structured(out, evidence, truncated)
}

// serviceNotes runs service_notes.
func (s *Source) serviceNotes(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[serviceNotesInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	row, err := db.New(s.db).GetServiceNotes(ctx, in.Service)
	if errors.Is(err, pgx.ErrNoRows) {
		return source.Result{NoData: true, Text: noNotesText}, nil
	}

	if err != nil {
		return source.Result{}, fmt.Errorf("read the notes of %q: %w", in.Service, err)
	}

	notes := source.CutText(row.Text, maxNotesText)

	return structured(notesResult{Service: row.Service, Notes: notes, UpdatedAt: row.UpdatedAt.UTC()}, nil, len(notes) < len(row.Text))
}

// New builds a memory source from its validated config. It reads the
// database in deps.DB; without one, every call fails as unavailable, so
// config validate can still build it.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	return &Source{db: deps.DB, publicURL: deps.PublicURL}, nil
}

// searchResult is the result of search.
type searchResult struct {
	Entries []entry `json:"entries"`
}

// entry is one confirmed investigation in a search result.
type entry struct {
	InvestigationID string    `json:"investigationId"`
	Verdict         string    `json:"confirmedVerdict"`
	Service         string    `json:"service,omitempty"`
	AlertName       string    `json:"alertName,omitempty"`
	Summary         string    `json:"summary"`
	Cause           string    `json:"confirmedCause,omitempty"`
	ConfirmedAt     time.Time `json:"confirmedAt"`
}

// notesResult is the result of service_notes.
type notesResult struct {
	Service   string    `json:"service"`
	Notes     string    `json:"notes"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// structured returns v as a structured result.
func structured(v any, evidence []source.Evidence, truncated bool) (source.Result, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: truncated}, nil
}

// decode decodes a tool's input. The guard has checked it against the
// tool's schema already, so a failure means the schema and the input type
// disagree; it still goes back to the model as a tool error.
func decode[T any](input json.RawMessage) (T, error) {
	var in T

	if len(input) == 0 {
		return in, nil
	}

	if err := json.Unmarshal(input, &in); err != nil {
		return in, &source.ToolError{Message: "the input does not decode: " + err.Error()}
	}

	return in, nil
}

// nonEmpty returns a pointer to s, or nil when s is empty.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}
