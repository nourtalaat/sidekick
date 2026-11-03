package githubpr

import (
	"context"
	"crypto/sha1" //nolint:gosec // Git names a blob by the SHA-1 of its content, which is what the snapshot compares.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// snapshotTarget starts the target of a snapshot, followed by owner/name.
const snapshotTarget = "github:"

// Snapshot implements action.Snapshotter. It reads the changed files at the
// head of the base branch and describes the change by blob SHAs, never by
// content: Before maps each path to the blob it has now, or null for a new
// file; After maps it to the blob of the new content with its status and the
// lines it adds and removes, or, for a deleted file, to its status and the
// lines it removes. Version is the SHA of the base commit.
func (p *PR) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	l, err := p.load(ctx, raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the files the change touches: %w", err)
	}

	before := make(map[string]any, len(l.changes))
	after := make(map[string]any, len(l.changes))
	left := p.limits.MaxChangedLines

	for _, c := range l.changes {
		f := l.files[c.Path]

		updated := ""
		if !c.Deleted() {
			updated = *c.Content
		}

		added, removed, within := gitchange.LineCounts(f.text, updated, left)
		if !within {
			return action.Snapshot{}, fmt.Errorf("the changes add and remove more than the %d lines one pull request may change: %w", p.limits.MaxChangedLines, action.ErrLimit)
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
		Target:  snapshotTarget + l.params.Repo,
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
		return gitchange.AfterEntry{Blob: blobSHA(*c.Content), Status: gitchange.FileAdded, LinesAdded: &added, LinesRemoved: &removed}
	default:
		return gitchange.AfterEntry{Blob: blobSHA(*c.Content), Status: gitchange.FileChanged, LinesAdded: &added, LinesRemoved: &removed}
	}
}

// applySnapshot compares the base branch now with the snapshot taken when the
// model proposed. A file at its before blob is written as planned. A file
// already at its after blob needs no change and is dropped from the plan. Any
// other file was changed by someone since, and the pull request would
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

		if !c.Deleted() && blobSHA(*c.Content) != after.Blob {
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

// blobSHA returns the SHA Git gives a blob with this content: the SHA-1 of
// "blob <length>", a NUL byte and the content.
func blobSHA(content string) string {
	h := sha1.New() //nolint:gosec // see the import.

	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = io.WriteString(h, content)

	return hex.EncodeToString(h.Sum(nil))
}
