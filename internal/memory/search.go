package memory

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// SearchParams are what the memory__search tool asks for.
type SearchParams struct {
	// Query is a web-search style query: words, "quoted phrases", or, and
	// -word. It is also what vector search embeds.
	Query string
	// Service and Verdict, when set, keep only the entries of that service
	// or with that verdict.
	Service *string
	Verdict *string
}

// Search returns up to limit enabled entries for the query, best first.
// The entries are the full-text matches of the query. With vec, they are
// merged with the entries nearest in meaning to the query by reciprocal
// rank fusion, as the examples of a prompt are. When the embeddings call
// fails, the result is the full-text matches alone, and a warning is
// logged; a search never fails because of vectors.
func Search(ctx context.Context, q db.DBTX, p SearchParams, limit int32, vec *Vectors) ([]db.SearchMemoryRow, error) {
	depth := limit
	if vec != nil {
		depth = max(limit, fusionDepth)
	}

	rows, err := db.New(q).SearchMemory(ctx, db.SearchMemoryParams{
		Query: p.Query, Service: p.Service, Verdict: p.Verdict, MaxRows: depth,
	})
	if err != nil {
		return nil, fmt.Errorf("search memory: %w", err)
	}

	if vec == nil {
		return rows, nil
	}

	nearest := vec.Nearest(ctx, q, p.Query, NearestFilter{Service: p.Service, Verdict: p.Verdict}, fusionDepth)

	merged := fuse(entriesOf(rows), entriesOf(nearest))
	out := make([]db.SearchMemoryRow, 0, min(len(merged), int(limit)))

	for _, f := range merged {
		if len(out) == int(limit) {
			break
		}

		out = append(out, db.SearchMemoryRow(f.row))
	}

	return out, nil
}
