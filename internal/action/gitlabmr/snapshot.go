package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// snapshotTarget starts the target of a snapshot, followed by the project.
const snapshotTarget = "gitlab:"

// Snapshot implements action.Snapshotter. It reads the changed files at the
// head of the base branch and describes the change by blob IDs, never by
// content: Before maps each path to the blob it has now, or null for a new
// file; After maps it to the blob of the new content with its status and the
// lines it adds and removes, or, for a deleted file, to its status and the
// lines it removes. Version is the SHA of the base commit. The shape is the
// one of gitchange.SnapshotFiles, so the proposal lists the files and shows
// their line diff as it does for a pull request.
func (m *MR) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	l, err := m.load(ctx, raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the files the change touches: %w", err)
	}

	before := make(map[string]any, len(l.changes))
	after := make(map[string]any, len(l.changes))
	left := m.limits.MaxChangedLines

	for _, c := range l.changes {
		f := l.files[c.Path]

		updated := ""
		if !c.Deleted() {
			updated = *c.Content
		}

		added, removed, within := gitchange.LineCounts(f.text, updated, left)
		if !within {
			return action.Snapshot{}, fmt.Errorf("the changes add and remove more than the %d lines one merge request may change: %w", m.limits.MaxChangedLines, action.ErrLimit)
		}

		left -= added + removed

		if f.exists {
			before[c.Path] = gitchange.BlobRef{Blob: f.sha}
		} else {
			before[c.Path] = nil
		}

		after[c.Path] = afterOf(c, f, added, removed)
	}

	beforeRaw, err := json.Marshal(before)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("encode the before side: %w", err)
	}

	afterRaw, err := json.Marshal(after)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("encode the after side: %w", err)
	}

	return action.Snapshot{
		Target:  snapshotTarget + l.params.Project,
		Before:  beforeRaw,
		After:   afterRaw,
		Version: l.base.commit,
	}, nil
}

// afterOf describes the file as the change leaves it.
func afterOf(c gitchange.PlannedFile, f readFile, added, removed int) gitchange.AfterEntry {
	switch {
	case c.Deleted():
		return gitchange.AfterEntry{Status: gitchange.FileDeleted, LinesRemoved: &removed}
	case !f.exists:
		return gitchange.AfterEntry{Blob: gitchange.BlobSHA(*c.Content), Status: gitchange.FileAdded, LinesAdded: &added, LinesRemoved: &removed}
	default:
		return gitchange.AfterEntry{Blob: gitchange.BlobSHA(*c.Content), Status: gitchange.FileChanged, LinesAdded: &added, LinesRemoved: &removed}
	}
}

// applySnapshot compares the base branch now with the snapshot taken when the
// model proposed. A file at its before blob is written as planned. A file
// already at its after blob needs no change and is dropped from the plan. Any
// other file was changed by someone since, and the merge request would
// overwrite it, so the plan fails with action.ErrConflict naming the file.
func (l *loaded) applySnapshot(snap action.Snapshot) error {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return fmt.Errorf("read the snapshot: %w", err)
	}

	keep := make([]gitchange.PlannedFile, 0, len(l.changes))

	for _, c := range l.changes {
		after, ok := sides.After[c.Path]
		if !ok {
			return fmt.Errorf("%w: the snapshot has no entry for %q", action.ErrPermanent, c.Path)
		}

		if !c.Deleted() && gitchange.BlobSHA(*c.Content) != after.Blob {
			return fmt.Errorf("%w: the content of %q is not the content the snapshot describes", action.ErrPermanent, c.Path)
		}

		live := l.files[c.Path].sha

		switch {
		case live == gitchange.BlobOf(sides.Before[c.Path]):
			keep = append(keep, c)
		case live == after.Blob:
		default:
			return fmt.Errorf("%q changed on the base branch after the proposal was made: %w", c.Path, action.ErrConflict)
		}
	}

	l.changes = keep

	return nil
}
