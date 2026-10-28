// Package memory keeps one entry for each confirmed investigation and
// finds the entries like a new investigation, which the prompt shows the
// model as examples.
//
// An entry is written when a responder confirms an investigation's
// verdict, changed when the confirmation changes, and deleted when the
// confirmation is taken back, all in the transaction that changes the
// confirmation, so an investigation nobody confirmed never appears in
// memory. An admin can disable an entry; a disabled entry never matches.
package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultExamples is how many examples a prompt shows when the config does
// not say.
const DefaultExamples = 3

// MaxExamples is the most examples a prompt may show.
const MaxExamples = 10

// maxMatchText is the most bytes of an event's text the full-text tier
// matches with, so a long alert body does not make a huge query.
const maxMatchText = 2000

// Match is what examples are matched against: the event that opened the
// group under investigation.
type Match struct {
	// Investigation is the investigation the examples are for. Its own
	// entry, if it has one, is never an example.
	Investigation uuid.UUID
	Fingerprint   string
	Service       string
	AlertName     string
	// Text holds the words of the event for the full-text tier.
	Text string
}

// MatchOf returns the Match of investigation, whose group e opened.
func MatchOf(investigation uuid.UUID, e event.Event) Match {
	words := strings.Join([]string{e.AlertName, e.Title, e.Text}, " ")

	return Match{
		Investigation: investigation,
		Fingerprint:   e.Fingerprint,
		Service:       e.Service,
		AlertName:     e.AlertName,
		Text:          source.CutText(strings.TrimSpace(words), maxMatchText),
	}
}

// TODO: [SK-59] add memory_entries to the retention job

// Sync makes the memory entry of the investigation with id match its
// confirmation: a confirmed investigation gets its entry written or
// updated, and the entry of one that is not confirmed is deleted. Both
// queries decide by the investigation's confirmed verdict, so an entry is
// never deleted while its investigation is confirmed, even when the
// group's events are gone. Call it in the transaction that changes the
// confirmation.
func Sync(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	queries := db.New(q)

	written, err := queries.UpsertMemoryEntry(ctx, id)
	if err != nil {
		return fmt.Errorf("write the memory entry of investigation %s: %w", id, err)
	}

	if written > 0 {
		return nil
	}

	if err := queries.DeleteUnconfirmedMemoryEntry(ctx, id); err != nil {
		return fmt.Errorf("delete the memory entry of investigation %s: %w", id, err)
	}

	return nil
}

// Examples returns up to limit enabled entries like m, in tier order:
// entries with the same fingerprint, then entries with the same service
// and alert name, then entries that share words with the event's text.
// Within a tier, the newest confirmation comes first, or for the text tier
// the best text-search rank. No entry appears twice.
func Examples(ctx context.Context, q db.DBTX, m Match, limit int) ([]prompt.Example, error) {
	c := &collector{
		left:    int32(min(max(limit, 0), MaxExamples)), //nolint:gosec // Capped at MaxExamples.
		exclude: []uuid.UUID{m.Investigation},
	}

	queries := db.New(q)

	if m.Fingerprint != "" && c.left > 0 {
		rows, err := queries.ListMemoryByFingerprint(ctx, db.ListMemoryByFingerprintParams{
			Fingerprint: m.Fingerprint, Exclude: c.exclude, MaxRows: c.left,
		})
		if err != nil {
			return nil, fmt.Errorf("list entries with the same fingerprint: %w", err)
		}

		add(c, prompt.MatchFingerprint, rows)
	}

	if m.Service != "" && m.AlertName != "" && c.left > 0 {
		rows, err := queries.ListMemoryByServiceAlert(ctx, db.ListMemoryByServiceAlertParams{
			Service: m.Service, AlertName: m.AlertName, Exclude: c.exclude, MaxRows: c.left,
		})
		if err != nil {
			return nil, fmt.Errorf("list entries with the same service and alert name: %w", err)
		}

		add(c, prompt.MatchServiceAlert, rows)
	}

	if m.Text != "" && c.left > 0 {
		rows, err := queries.ListSimilarMemory(ctx, db.ListSimilarMemoryParams{Text: m.Text, Exclude: c.exclude, MaxRows: c.left})
		if err != nil {
			return nil, fmt.Errorf("list entries with similar text: %w", err)
		}

		add(c, prompt.MatchText, rows)
	}

	return c.examples, nil
}

// entryRow is a row of one of the memory list queries. They all select
// the same columns.
type entryRow interface {
	db.ListMemoryByFingerprintRow | db.ListMemoryByServiceAlertRow | db.ListSimilarMemoryRow
}

// collector gathers the examples of each tier in turn.
type collector struct {
	examples []prompt.Example
	// left is how many more examples fit.
	left int32
	// exclude are the investigations already shown, and the one the
	// examples are for.
	exclude []uuid.UUID
}

// add adds the rows of one tier to c.
func add[R entryRow](c *collector, match prompt.MemoryMatch, rows []R) {
	for _, r := range rows {
		row := db.ListMemoryByFingerprintRow(r)

		c.examples = append(c.examples, prompt.Example{
			InvestigationID: row.InvestigationID.String(),
			Match:           match,
			Verdict:         row.Verdict,
			Service:         deref(row.Service),
			AlertName:       deref(row.AlertName),
			Summary:         row.Summary,
			Cause:           deref(row.Cause),
			ConfirmedAt:     row.ConfirmedAt,
		})
		c.exclude = append(c.exclude, row.InvestigationID)
		c.left--
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}
