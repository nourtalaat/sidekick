package gitchange

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestFilesOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		before string
		after  string
		want   []action.ProposalFile
	}{
		{
			name:   "files of the three statuses, sorted by path",
			before: `{"b.yaml": {"blob": "b1"}, "a.yaml": null, "c.yaml": {"blob": "c1"}}`,
			after: `{
				"b.yaml": {"blob": "b2", "status": "changed", "linesAdded": 2, "linesRemoved": 1},
				"a.yaml": {"blob": "a2", "status": "added", "linesAdded": 4, "linesRemoved": 0},
				"c.yaml": {"status": "deleted", "linesRemoved": 7}
			}`,
			want: []action.ProposalFile{
				{Path: "a.yaml", Status: FileAdded, LinesAdded: 4},
				{Path: "b.yaml", Status: FileChanged, LinesAdded: 2, LinesRemoved: 1},
				{Path: "c.yaml", Status: FileDeleted, LinesRemoved: 7},
			},
		},
		{name: "a missing line count is zero", before: `{"a.yaml": {"blob": "a1"}}`, after: `{"a.yaml": {"blob": "a2", "status": "changed"}}`, want: []action.ProposalFile{{Path: "a.yaml", Status: FileChanged}}},
		{name: "the snapshot of a monitor is not files", before: `{"options": {"critical": 80}}`, after: `{"options": {"critical": 90}}`},
		{name: "a definition made of objects is not files", before: `{"options": {"status": {"a": 1}}}`, after: `{"options": {"status": {"a": 2}}}`},
		{name: "an entry with an unknown status is not files", before: `{"a.yaml": null}`, after: `{"a.yaml": {"blob": "a2", "status": "moved"}}`},
		{name: "a delete of the whole target is not files", before: `{"a": 1}`, after: `null`},
		{name: "an empty snapshot is not files", before: `{}`, after: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FilesOf(action.Snapshot{Target: "github:acme/monitors", Before: json.RawMessage(tt.before), After: json.RawMessage(tt.after)})
			require.Equal(t, tt.want, got)
		})
	}
}
