package memory

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fuseEntry is an entry that only has an ID, which is all fuse reads.
func fuseEntry(n byte) matchRow {
	return matchRow{InvestigationID: uuid.UUID{15: n}}
}

func TestFuse(t *testing.T) {
	t.Parallel()

	e1, e2, e3, e4, e5 := fuseEntry(1), fuseEntry(2), fuseEntry(3), fuseEntry(4), fuseEntry(5)

	tests := []struct {
		name  string
		lists []ranked
		// want are the entries by score, with the score worked out by hand
		// from 1/(60+rank).
		want []fused
	}{
		{
			name:  "five entries in two lists",
			lists: []ranked{{e1, e2, e3}, {e4, e1, e5, e3}},
			want: []fused{
				// 1/61 + 1/62 = 0.032522
				{row: e1, score: 1.0/61 + 1.0/62, inFirst: true},
				// 1/63 + 1/64 = 0.031498
				{row: e3, score: 1.0/63 + 1.0/64, inFirst: true},
				// 1/61 = 0.016393
				{row: e4, score: 1.0 / 61},
				// 1/62 = 0.016129
				{row: e2, score: 1.0 / 62, inFirst: true},
				// 1/63 = 0.015873
				{row: e5, score: 1.0 / 63},
			},
		},
		{
			name:  "equal scores keep the order of first appearance",
			lists: []ranked{{e1}, {e2}},
			want: []fused{
				{row: e1, score: 1.0 / 61, inFirst: true},
				{row: e2, score: 1.0 / 61},
			},
		},
		{
			name:  "one list keeps its order",
			lists: []ranked{{e3, e1, e2}},
			want: []fused{
				{row: e3, score: 1.0 / 61, inFirst: true},
				{row: e1, score: 1.0 / 62, inFirst: true},
				{row: e2, score: 1.0 / 63, inFirst: true},
			},
		},
		{
			name:  "an empty first list leaves the second as it is",
			lists: []ranked{nil, {e2, e1}},
			want: []fused{
				{row: e2, score: 1.0 / 61},
				{row: e1, score: 1.0 / 62},
			},
		},
		{name: "no lists", lists: nil, want: []fused{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := fuse(tt.lists...)
			require.Len(t, got, len(tt.want))

			for i, w := range tt.want {
				require.Equal(t, w.row.InvestigationID, got[i].row.InvestigationID, "entry %d", i)
				require.InDelta(t, w.score, got[i].score, 1e-12, "score of entry %d", i)
				require.Equal(t, w.inFirst, got[i].inFirst, "list of entry %d", i)
			}
		})
	}
}

func TestRRFConstant(t *testing.T) {
	t.Parallel()

	require.Equal(t, 60, RRFConstant)
}
