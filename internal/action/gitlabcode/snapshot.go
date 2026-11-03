package gitlabcode

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// snapshotTarget starts the target of a snapshot, followed by the project.
const snapshotTarget = "gitlab:"

// Snapshot implements action.Snapshotter. It reads the changed files at the
// head of the base branch, refuses what the limits do not allow with an error
// that wraps action.ErrLimit, and describes the change by blob IDs, never by
// content: Before maps each path to the blob it has now, or null for a new
// file; After maps it to the blob of the new content with its status and the
// lines it adds and removes, or, for a deleted file, to its status and the
// lines it removes. Version is the SHA of the base commit.
func (c *Code) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	l, err := c.load(ctx, raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the files the change touches: %w", err)
	}

	if err := c.checkChanges(l); err != nil {
		return action.Snapshot{}, fmt.Errorf("check the changes: %w", err)
	}

	before := make(map[string]any, len(l.changes))
	after := make(map[string]any, len(l.changes))
	left := c.limits.MaxChangedLines

	for _, ch := range l.changes {
		f := l.files[ch.Path]

		updated := ""
		if !ch.Deleted() {
			updated = *ch.Content
		}

		added, removed, within := gitchange.LineCounts(f.Text, updated, left)
		if !within {
			return action.Snapshot{}, fmt.Errorf("the changes add and remove more than the %d lines one merge request may change: %w", c.limits.MaxChangedLines, action.ErrLimit)
		}

		left -= added + removed

		if f.Exists {
			before[ch.Path] = gitchange.BlobRef{Blob: f.SHA}
		} else {
			before[ch.Path] = nil
		}

		after[ch.Path] = afterOf(ch, f, added, removed)
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
		Version: l.base.Commit,
	}, nil
}

// afterOf describes the file as the change leaves it.
func afterOf(c gitchange.PlannedFile, f gitlabgit.ReadFile, added, removed int) gitchange.AfterEntry {
	switch {
	case c.Deleted():
		return gitchange.AfterEntry{Status: gitchange.FileDeleted, LinesRemoved: &removed}
	case !f.Exists:
		return gitchange.AfterEntry{Blob: gitchange.BlobSHA(*c.Content), Status: gitchange.FileAdded, LinesAdded: &added, LinesRemoved: &removed}
	default:
		return gitchange.AfterEntry{Blob: gitchange.BlobSHA(*c.Content), Status: gitchange.FileChanged, LinesAdded: &added, LinesRemoved: &removed}
	}
}
