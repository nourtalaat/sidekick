package gitchange

import (
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action"
)

// FilesOf lists the files that the snapshot of a pull request or merge
// request describes, sorted by path, with the lines each adds and removes.
// It reads only the snapshot, so it makes no network call. It returns nil for
// a snapshot that is not of that shape, such as the snapshot of a monitor.
func FilesOf(snap action.Snapshot) []action.ProposalFile {
	sides, err := ReadSnapshot(snap)
	if err != nil || len(sides.After) == 0 {
		return nil
	}

	files := make([]action.ProposalFile, 0, len(sides.After))

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		entry := sides.After[path]

		switch entry.Status {
		case FileChanged, FileAdded, FileDeleted:
		default:
			return nil
		}

		files = append(files, action.ProposalFile{
			Path: path, Status: entry.Status, LinesAdded: valueOf(entry.LinesAdded), LinesRemoved: valueOf(entry.LinesRemoved),
		})
	}

	return files
}

// valueOf returns *p, or 0 for nil.
func valueOf(p *int) int {
	if p == nil {
		return 0
	}

	return *p
}
