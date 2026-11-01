// Package scenario exports confirmed investigations as benchmark scenarios.
//
// A draft is built from the investigation's first alert, its recorded
// source calls and its confirmed verdict and cause. It is cleaned of
// sensitive data, saved as a draft, reviewed and edited by an admin, and
// only then marked reviewed. Only reviewed scenarios can be run or
// downloaded.
package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/source/mock"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

const (
	// idPrefixLen is how long the readable part of a scenario ID may be;
	// the investigation ID adds idSuffixLen hex characters, taken from the
	// random end of the UUID, since its start is a timestamp that repeats
	// for hours.
	idPrefixLen = 60
	idSuffixLen = 12
	// untitled is the title of a scenario whose alert has none.
	untitled = "Exported investigation"
	// rubricIDPrefix starts the IDs of drafted rubric items.
	rubricIDPrefix = "r"
	// tagExported marks every exported scenario.
	tagExported = "exported"
)

var (
	nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
	// errNoID means the document built from a scenario has no id field.
	errNoID = errors.New("the scenario document has no id")
)

// Input is what a draft is built from.
type Input struct {
	InvestigationID uuid.UUID
	Route           string
	Strategy        string
	// Title is the title of the alert.
	Title string
	// Verdict and Cause are the confirmed verdict and cause. Cause may be
	// empty.
	Verdict report.Verdict
	Cause   string
	Event   EventInput
	// Calls are the recorded source calls, in order.
	Calls []investigator.RecordedCall
	// SourceTypes maps a source name to its type in the config, which names
	// its tool snapshot. A source that is missing uses its own name.
	SourceTypes map[string]string
}

// EventInput is the event that opened the investigation.
type EventInput struct {
	// Adapter is the intake adapter type of the endpoint the event came in
	// on, and Config the adapter's own settings, if it has any.
	Adapter string
	Config  *yaml.Node
	// Payload is the original payload, redacted when it was stored.
	Payload    json.RawMessage
	ReceivedAt time.Time
}

// Draft is a cleaned scenario and what the review needs to know about it.
type Draft struct {
	ID    string
	Title string
	// Body is the scenario file.
	Body       []byte
	Provenance []Provenance
	Notes      []Note
}

// Builder builds drafts.
type Builder struct {
	cleaner *scrub.Cleaner
	rubric  RubricDrafter
}

// NewBuilder returns a Builder. rubric may be nil, which leaves the rubric of
// every draft empty.
func NewBuilder(cleaner *scrub.Cleaner, rubric RubricDrafter) *Builder {
	return &Builder{cleaner: cleaner, rubric: rubric}
}

// Build makes the draft of an investigation. q keeps the cost of the model
// call that drafts the rubric; it may be nil when the builder has no rubric
// drafter.
func (b *Builder) Build(ctx context.Context, q db.DBTX, in Input) (Draft, error) {
	if in.Verdict == "" {
		return Draft{}, ErrNotConfirmed
	}

	configs, unavailable, provenance, notes, err := fixturesOf(in.Calls)
	if err != nil {
		return Draft{}, fmt.Errorf("build the fixtures: %w", err)
	}

	doc, err := b.document(in, configs, unavailable)
	if err != nil {
		return Draft{}, fmt.Errorf("build the scenario: %w", err)
	}

	session := b.cleaner.NewSession()

	if _, err := session.Clean(doc); err != nil {
		return Draft{}, fmt.Errorf("clean the scenario: %w", err)
	}

	replaceQueryMarkers(doc)

	title := cleanedTitle(doc)
	id := scenarioID(title, textOf(doc, "strategy"), in.InvestigationID)

	if !setString(doc, "id", id) {
		return Draft{}, errNoID
	}

	notes, err = b.addRubric(ctx, q, doc, session, in, notes)
	if err != nil {
		return Draft{}, fmt.Errorf("add the rubric: %w", err)
	}

	if err := b.cleaner.Check(doc); err != nil {
		return Draft{}, fmt.Errorf("check the scenario: %w", err)
	}

	resetStyles(doc)

	body, err := encodeYAML(doc)
	if err != nil {
		return Draft{}, fmt.Errorf("write the scenario: %w", err)
	}

	if _, err := bench.ParseScenario(body); err != nil {
		return Draft{}, fmt.Errorf("the draft is not a valid scenario: %w", err)
	}

	return Draft{ID: id, Title: title, Body: body, Provenance: provenance, Notes: notes}, nil
}

// document builds the scenario as a YAML node, with the ID still to set.
func (b *Builder) document(in Input, configs map[string]mock.Config, unavailable []string) (*yaml.Node, error) {
	payload, err := jsonNode(in.Event.Payload)
	if err != nil {
		return nil, fmt.Errorf("read the event: %w", err)
	}

	for name, cfg := range configs {
		if !cfg.Unavailable {
			cfg.Tools.Snapshot = snapshotDir + sourceType(in.SourceTypes, name) + ".json"
			configs[name] = cfg
		}
	}

	title := in.Title
	if title == "" {
		title = untitled
	}

	tags := []string{tagExported}
	if in.Strategy != "" {
		tags = append(tags, in.Strategy)
	}

	sc := bench.Scenario{
		ID:       "pending",
		Title:    title,
		Tags:     tags,
		Strategy: in.Strategy,
		Now:      in.Event.ReceivedAt.UTC().Truncate(time.Second),
		Route:    bench.Route{Mode: action.ReadOnly},
		Event:    bench.Event{Adapter: in.Event.Adapter, Payload: payload},
		Sources:  configs,
		Default:  mock.DefaultNoData,
		Expected: bench.Expected{
			Verdict:             in.Verdict,
			RootCause:           bench.RootCause{Summary: in.Cause},
			UnavailableReported: unavailable,
		},
	}

	if cfg := in.Event.Config; cfg != nil && len(cfg.Content) > 0 {
		sc.Event.Config = *cfg
	}

	var doc yaml.Node
	if err := doc.Encode(sc); err != nil {
		return nil, fmt.Errorf("encode the scenario: %w", err)
	}

	return &doc, nil
}

// addRubric drafts the rubric from the cleaned cause, cleans it and adds it
// to the document. It adds a note when there is none.
func (b *Builder) addRubric(ctx context.Context, q db.DBTX, doc *yaml.Node, session *scrub.Session, in Input, notes []Note) ([]Note, error) {
	rootCause := child(child(doc, "expected"), "rootCause")
	cause := textOf(rootCause, "summary")

	switch {
	case cause == "":
		return append(notes, Note{Kind: NoteNoCause, Text: "The investigation has no confirmed cause, so the scenario has no expected cause and no rubric. Write them in the editor."}), nil
	case b.rubric == nil || !b.rubric.Enabled():
		return append(notes, Note{Kind: NoteNoRubric, Text: "No summarize model is configured, so the scenario has no rubric items. Write them in the editor."}), nil
	}

	items, err := b.rubric.Draft(ctx, q, in.Route, string(in.Verdict), cause)
	if err != nil {
		return append(notes, Note{Kind: NoteNoRubric, Text: "The rubric could not be drafted (" + reasonOf(err) + "). Write the items in the editor."}), nil
	}

	for i := range items {
		text, err := session.CleanText(items[i].Text)
		if err != nil {
			return nil, fmt.Errorf("clean rubric item %d: %w", i+1, err)
		}

		items[i].Text = text
		items[i].ID = fmt.Sprintf("%s%d", rubricIDPrefix, i+1)
	}

	if err := appendEncoded(rootCause, "rubric", items); err != nil {
		return nil, fmt.Errorf("add the rubric: %w", err)
	}

	return notes, nil
}

// reasonOf is a short reason for a note, without the error's details.
func reasonOf(err error) string {
	if errors.Is(err, summarize.ErrOverCap) {
		return "a daily spending cap is reached"
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "it timed out"
	}

	return "the summarize model did not answer in a usable way"
}

// replaceQueryMarkers turns each fixture regex that still holds a query into
// a regex of the query's words. Run it after cleaning, so the words come
// from the cleaned query.
func replaceQueryMarkers(doc *yaml.Node) {
	var visit func(n *yaml.Node)

	visit = func(n *yaml.Node) {
		if hasPrefixValue(n, queryMarker) {
			n.Value = termsRegex(strings.TrimPrefix(n.Value, queryMarker))

			return
		}

		for _, c := range n.Content {
			visit(c)
		}
	}

	visit(doc)
}

// cleanedTitle returns the cleaned title of the document.
func cleanedTitle(doc *yaml.Node) string {
	if title := textOf(doc, "title"); title != "" {
		return title
	}

	return untitled
}

// textOf returns the text of the scalar under key in a mapping node, or "".
func textOf(m *yaml.Node, key string) string {
	if n := child(m, key); n != nil {
		return n.Value
	}

	return ""
}

// scenarioID makes a scenario ID from the cleaned title, the strategy and
// the start of the investigation ID.
func scenarioID(title, strategy string, investigationID uuid.UUID) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(strategy+" "+title), "-"), "-")
	if len(slug) > idPrefixLen {
		slug = strings.Trim(slug[:idPrefixLen], "-")
	}

	hex := strings.ReplaceAll(investigationID.String(), "-", "")
	suffix := hex[len(hex)-idSuffixLen:]

	if slug == "" {
		return "scenario-" + suffix
	}

	return slug + "-" + suffix
}

// sourceType returns the type of a source for its snapshot name.
func sourceType(types map[string]string, name string) string {
	if t := types[name]; t != "" {
		return t
	}

	return name
}
