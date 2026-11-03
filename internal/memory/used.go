package memory

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Use is an example that the prompt of a run showed. When Present is false
// the memory entry is gone, and Summary, Cause, Verdict and Disabled are
// empty.
type Use struct {
	// ExampleID is the investigation the example came from.
	ExampleID uuid.UUID
	Match     prompt.MemoryMatch
	// Rank is the example's position in the prompt, from 1.
	Rank int
	// Present says whether the memory entry of the example still exists.
	Present bool
	// Summary, Cause and Verdict are the entry's as they are now, with the
	// confirmed verdict.
	Summary, Cause, Verdict string
	Disabled                bool
	// Title is the alert title of the example's investigation, empty when
	// the investigation is gone or never had one.
	Title string
}

// Record notes that the prompt of run of the investigation showed examples,
// in order. An example recorded for the run already is left as it is. Call it
// in the transaction that saves the prompt.
func Record(ctx context.Context, q db.DBTX, investigationID uuid.UUID, run int32, examples []prompt.Example) error {
	if len(examples) == 0 {
		return nil
	}

	ids, matches, err := exampleColumns(examples)
	if err != nil {
		return fmt.Errorf("read the examples of investigation %s: %w", investigationID, err)
	}

	err = db.New(q).InsertMemoryUses(ctx, db.InsertMemoryUsesParams{
		InvestigationID: investigationID, Run: run, ExampleIds: ids, Matches: matches,
	})
	if err != nil {
		return fmt.Errorf("record the memory examples of investigation %s, run %d: %w", investigationID, run, err)
	}

	return nil
}

// UsedBy returns up to limit examples that the prompt of run of the
// investigation showed, in the order of the prompt, with their text and
// state as they are now.
func UsedBy(ctx context.Context, q db.DBTX, investigationID uuid.UUID, run int32, limit int) ([]Use, error) {
	rows, err := db.New(q).ListMemoryUsedBy(ctx, db.ListMemoryUsedByParams{
		InvestigationID: investigationID, Run: run, MaxRows: int32(min(max(limit, 0), MaxExamples)), //nolint:gosec // Capped at MaxExamples.
	})
	if err != nil {
		return nil, fmt.Errorf("list the memory examples of investigation %s, run %d: %w", investigationID, run, err)
	}

	out := make([]Use, len(rows))
	for i, r := range rows {
		out[i] = Use{
			ExampleID: r.ExampleID, Match: prompt.MemoryMatch(r.Match), Rank: int(r.Rank), Present: r.Present,
			Summary: deref(r.Summary), Cause: deref(r.Cause), Verdict: deref(r.Verdict), Disabled: deref(r.Disabled),
			Title: deref(r.AlertTitle),
		}
	}

	return out, nil
}

// exampleColumns returns the investigation IDs and the matches of examples,
// in their order.
func exampleColumns(examples []prompt.Example) ([]uuid.UUID, []string, error) {
	ids := make([]uuid.UUID, len(examples))
	matches := make([]string, len(examples))

	for i, ex := range examples {
		id, err := uuid.Parse(ex.InvestigationID)
		if err != nil {
			return nil, nil, fmt.Errorf("example %d has the investigation ID %q: %w", i+1, ex.InvestigationID, err)
		}

		ids[i], matches[i] = id, string(ex.Match)
	}

	return ids, matches, nil
}
