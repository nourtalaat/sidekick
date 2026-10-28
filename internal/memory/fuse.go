package memory

import (
	"sort"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// RRFConstant is the k of reciprocal rank fusion: an entry that is at rank
// r of a list, counting from 1, adds 1/(RRFConstant+r) to its score. 60 is
// the value the method's authors found to work across many lists.
const RRFConstant = 60

// matchRow is a memory entry as the list queries return it.
type matchRow = db.ListMemoryByFingerprintRow

// ranked is a list of entries, best first, from one kind of search.
type ranked []matchRow

// fused is an entry with its fusion score and the lists it was in.
type fused struct {
	row   matchRow
	score float64
	// inFirst is true when the entry is in the first list, the full-text
	// one, so a vector-only entry can be told apart.
	inFirst bool
}

// fuse merges ranked lists by reciprocal rank fusion: an entry scores the
// sum of 1/(RRFConstant+rank) over the lists it is in, and the entries come
// back by score, highest first. Entries with the same score keep the order
// in which they first appear when the lists are read one after the other,
// so the result does not depend on map order. An entry is in the result
// once.
func fuse(lists ...ranked) []fused {
	byID := map[uuid.UUID]*fused{}

	var order []*fused

	for i, list := range lists {
		for rank, e := range list {
			f, seen := byID[e.InvestigationID]
			if !seen {
				f = &fused{row: e}
				byID[e.InvestigationID] = f
				order = append(order, f)
			}

			f.score += 1 / float64(RRFConstant+rank+1)
			f.inFirst = f.inFirst || i == 0
		}
	}

	sort.SliceStable(order, func(a, b int) bool { return order[a].score > order[b].score })

	out := make([]fused, len(order))
	for i, f := range order {
		out[i] = *f
	}

	return out
}
