package gitlabgit

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// RevertPlan is what restoring the files of a merged change takes.
type RevertPlan struct {
	// Restores are the files that are not at their earlier content.
	Restores []Restore
	// Conflicts are the files that are neither as the change left them nor as
	// they were before it.
	Conflicts []string
	// Live maps every file of the change to the blob it has now, or nil.
	Live map[string]*gitchange.BlobRef
}

// Restore puts one file back to a blob.
type Restore struct {
	Path string
	// Blob is the blob the file had before, or empty for a file that was
	// added, which is deleted.
	Blob string
	// Exists says whether the file is there now.
	Exists bool
}

// PlanRevert reads the files of the change that sides describes at the
// reader's commit and decides what restores them. A file that is at its
// earlier content already needs nothing. A file that was added is deleted. Any
// other file gets the blob it had before. Nothing is written, and no earlier
// content is read yet, so a conflict that stops the revert costs no more
// reads. guard may be nil; when it is set, it runs on every path before the
// path is read, and its error ends the plan.
func PlanRevert(ctx context.Context, r *Reader, sides gitchange.SnapshotFiles, guard func(ctx context.Context, path string) error) (RevertPlan, error) {
	plan := RevertPlan{Live: make(map[string]*gitchange.BlobRef, len(sides.After))}

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		if guard != nil {
			if err := guard(ctx, path); err != nil {
				return RevertPlan{}, fmt.Errorf("check %s: %w", path, err)
			}
		}

		f, err := r.File(ctx, path)
		if err != nil {
			return RevertPlan{}, fmt.Errorf("read the file at the base: %w", err)
		}

		if f.Exists {
			plan.Live[path] = &gitchange.BlobRef{Blob: f.SHA}
		} else {
			plan.Live[path] = nil
		}

		before := gitchange.BlobOf(sides.Before[path])
		if f.SHA == before {
			continue
		}

		if f.SHA != sides.After[path].Blob {
			plan.Conflicts = append(plan.Conflicts, path)
		}

		plan.Restores = append(plan.Restores, Restore{Path: path, Blob: before, Exists: f.Exists})
	}

	return plan, nil
}

// RestoreActions turns the restores into the actions of a commit. The Commits
// API takes content and not a blob, so the earlier content of each file is
// read by its blob ID.
func RestoreActions(ctx context.Context, client *gitlabapi.Client, project string, restores []Restore) ([]CommitAction, error) {
	actions := make([]CommitAction, 0, len(restores))

	for _, r := range restores {
		if r.Blob == "" {
			actions = append(actions, CommitAction{Action: CommitDelete, FilePath: r.Path})

			continue
		}

		content, err := ReadBlobText(ctx, client, project, r.Blob, r.Path+" as it was")
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", r.Path, err)
		}

		kind := CommitCreate
		if r.Exists {
			kind = CommitUpdate
		}

		actions = append(actions, CommitAction{Action: kind, FilePath: r.Path, Content: &content})
	}

	return actions, nil
}
