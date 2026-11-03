package githubrevert

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// snapshotTarget starts the target of a snapshot, followed by owner/name.
const snapshotTarget = "github:"

// Snapshot implements action.Snapshotter. It reads what the commit changed
// and the files at the head of the base branch, and describes the revert by
// blob SHAs, never by content: Before maps each path to the blob it has now
// (the one the commit wrote), or null for a file the commit deleted; After
// maps it to the blob the revert writes (the one the parent had) with its
// status and the lines it adds and removes, or, for a file the commit added,
// to its status and the lines it removes. Version is the SHA of the base
// commit.
//
// A file at the head that is not what the commit wrote refuses the whole
// revert, naming the file. A file that is at what the parent had already is
// left out, and when every file is, there is nothing to revert.
func (r *Revert) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	l, err := r.load(ctx, raw, false)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read what to revert: %w", err)
	}

	pending, err := l.pending()
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("check the files at the base branch: %w", err)
	}

	before := make(map[string]any, len(pending))
	after := make(map[string]any, len(pending))
	left := r.limits.MaxChangedLines

	for _, f := range pending {
		t, err := r.readTexts(ctx, l, f)
		if err != nil {
			return action.Snapshot{}, fmt.Errorf("read %q: %w", f.path, err)
		}

		added, removed, within := gitchange.LineCounts(t.now, t.reverts, left)
		if !within {
			return action.Snapshot{}, fmt.Errorf("the revert adds and removes more than the %d lines one revert may change: %w", r.limits.MaxChangedLines, action.ErrLimit)
		}

		left -= added + removed

		before[f.path] = beforeOf(f)
		after[f.path] = afterOf(f, added, removed)
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
		Version: l.head.Commit,
	}, nil
}

// pending returns the files the revert has to write, which are at what the
// commit wrote. A file at what the parent had needs nothing. Any other file
// was changed after the commit, and writing the parent's content over it
// would undo that work, so the revert is refused.
func (l *loaded) pending() ([]file, error) {
	var pending []file

	for _, f := range l.files {
		switch f.live {
		case f.commit:
			pending = append(pending, f)
		case f.parent:
		default:
			return nil, fmt.Errorf("%w: %q changed on the base branch after %s, so reverting it would overwrite later work; use github.code_change to fix it, or revert it by hand", action.ErrPermanent, f.path, l.target.what)
		}
	}

	if len(pending) == 0 {
		return nil, fmt.Errorf("%w: every file %s changed is back to what it was before already", action.ErrPermanent, l.target.what)
	}

	return pending, nil
}

// beforeOf describes the file as it is now: the blob the commit wrote, or
// nothing for a file the commit deleted.
func beforeOf(f file) *gitchange.BlobRef {
	if !f.commit.exists() {
		return nil
	}

	return &gitchange.BlobRef{Blob: f.commit.blob}
}

// afterOf describes the file as the revert leaves it.
func afterOf(f file, added, removed int) gitchange.AfterEntry {
	switch {
	case !f.parent.exists():
		return gitchange.AfterEntry{Status: gitchange.FileDeleted, LinesRemoved: &removed}
	case !f.commit.exists():
		return gitchange.AfterEntry{Blob: f.parent.blob, Status: gitchange.FileAdded, LinesAdded: &added, LinesRemoved: &removed}
	default:
		return gitchange.AfterEntry{Blob: f.parent.blob, Status: gitchange.FileChanged, LinesAdded: &added, LinesRemoved: &removed}
	}
}

// applySnapshot compares the base branch now with the snapshot taken when the
// model proposed, and returns the files to write. A file at the blob the
// snapshot says it had is written. A file already at the blob the revert
// writes is left out. Any other file was changed by someone since, and the
// revert would overwrite it, so the call fails with action.ErrConflict naming
// the file. A snapshot that does not describe the commit fails for good.
func (l *loaded) applySnapshot(snap action.Snapshot) ([]file, error) {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return nil, fmt.Errorf("read the snapshot: %w", err)
	}

	var writes []file

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		f, err := l.described(sides, path)
		if err != nil {
			return nil, fmt.Errorf("read the snapshot of %q: %w", path, err)
		}

		switch f.live {
		case f.commit:
			writes = append(writes, f)
		case f.parent:
		default:
			return nil, fmt.Errorf("%q changed on the base branch after the proposal was made: %w", path, action.ErrConflict)
		}
	}

	return writes, nil
}

// described returns the file of the commit that the snapshot describes at
// the path. It fails for good if the commit does not change the file the way
// the snapshot says.
func (l *loaded) described(sides gitchange.SnapshotFiles, path string) (file, error) {
	f, ok := l.byPath(path)
	if !ok {
		return file{}, fmt.Errorf("%w: %s does not change %q, which the snapshot says", action.ErrPermanent, l.target.what, path)
	}

	if sides.After[path].Blob != f.parent.blob || gitchange.BlobOf(sides.Before[path]) != f.commit.blob {
		return file{}, fmt.Errorf("%w: the snapshot of %q is not what %s changed", action.ErrPermanent, path, l.target.what)
	}

	return f, nil
}
