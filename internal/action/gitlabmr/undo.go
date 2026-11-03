package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// revertPrefix starts the title of a revert merge request.
const revertPrefix = "Revert: "

// Undo implements action.Undoer. It undoes the merge request that the
// proposal inv.UndoOf made.
//
// A merge request that is not merged is closed and its branch is deleted. One
// that was merged is reverted with a new merge request, which still needs
// review and merging: the revert restores the earlier content of the files
// through the Commits API, as GitHub's does through the Git data API and not
// with GitLab's revert call, so the two hosts behave alike. It is not run
// through the validator, because it restores what was there. Before it opens
// one, it checks that each file on the base branch is still as the merged
// merge request left it. If one is not, the error is an *action.ConflictError
// that carries the files as they are now, and force makes the revert
// overwrite them.
//
// It is safe to repeat: it finds the revert merge request, or the revert
// branch, of an earlier attempt, and closing a closed merge request changes
// nothing.
func (m *MR) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	branch, err := gitlabgit.UndoBranch(inv)
	if err != nil {
		return action.Result{}, fmt.Errorf("check the call to undo: %w", err)
	}

	params, err := decodeParams(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the parameters of the proposal: %w", err)
	}

	if !m.limits.allowsProject(params.Project) {
		return action.Result{}, fmt.Errorf("project %q is not one of limits.projects (%s): %w", params.Project, strings.Join(m.limits.Projects, ", "), action.ErrLimit)
	}

	orig, err := gitlabgit.MergeRequestFrom(ctx, m.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the merge request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no merge request was made from the branch %s", action.ErrPermanent, branch)
	}

	if gitlabgit.StateOf(orig) == gitchange.StateMerged {
		return m.revert(ctx, inv, params, orig, snap, force)
	}

	res, err := gitlabgit.CloseOriginal(ctx, m.client, m.db, m.metrics.Metrics, inv, params.Project, orig, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("close the merge request to undo: %w", err)
	}

	return res, nil
}

// revert opens a merge request that restores the files of a merged merge
// request to their earlier content.
func (m *MR) revert(ctx context.Context, inv action.Invocation, params Params, orig *gitlabgit.MergeRequest, snap action.Snapshot, force bool) (action.Result, error) {
	branch := inv.IdempotencyKey

	if err := gitlabgit.Record(ctx, m.db, inv.UndoOf, params.Project, gitlabgit.StatusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	mr, branchExists, err := gitlabgit.Existing(ctx, m.client, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if mr == nil {
		if !branchExists {
			res, done, err := m.pushRevert(ctx, inv, params, snap, force)
			if err != nil {
				return action.Result{}, fmt.Errorf("revert the files: %w", err)
			}

			if done {
				return res, nil
			}
		}

		mr, err = gitlabgit.OpenMergeRequest(ctx, m.client, params.Project, m.revertRequest(inv, params, orig, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the revert merge request: %w", err)
		}
	}

	if err := gitlabgit.Record(ctx, m.db, inv.ProposalID, params.Project, gitlabgit.StatusOf(mr)); err != nil {
		return action.Result{}, fmt.Errorf("record the revert merge request: %w", err)
	}

	m.metrics.MergeRequest(gitlabgit.ResultReverted)

	title := params.Project + "!" + strconv.Itoa(mr.IID)

	return action.Result{
		Summary: "Opened the revert merge request " + title + ". It still needs review and merging. Its files were not validated, because it restores their earlier content.",
		Links:   []action.Link{{Title: title, URL: mr.WebURL}},
	}, nil
}

// pushRevert checks the files on the base branch against the snapshot and
// writes the commit that restores their earlier content on the revert
// branch. done is true, with the result, when there is nothing to restore.
func (m *MR) pushRevert(ctx context.Context, inv action.Invocation, params Params, snap action.Snapshot, force bool) (res action.Result, done bool, err error) {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the snapshot: %w", err)
	}

	head, err := gitlabgit.BaseOf(ctx, m.client, params.Project, params.BaseBranch)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the base branch: %w", err)
	}

	plan, err := m.planRevert(ctx, gitlabgit.NewReader(m.client, params.Project, head.Commit), sides)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("plan the revert: %w", err)
	}

	if len(plan.conflicts) > 0 && !force {
		live, merr := json.Marshal(plan.live)
		if merr != nil {
			return action.Result{}, false, fmt.Errorf("encode the files as they are now: %w", merr)
		}

		return action.Result{}, false, &action.ConflictError{Live: live}
	}

	if len(plan.restores) == 0 {
		return action.Result{Summary: "Nothing to revert: the files are as they were before the merge request."}, true, nil
	}

	actions, err := m.restoreActions(ctx, params.Project, plan.restores)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the earlier content of the files: %w", err)
	}

	message := revertPrefix + source.CutText(gitlabgit.CommitTitle(params.Title), gitlabgit.MaxCommitTitleBytes-len(revertPrefix)) +
		"\n\nSidekick proposal " + inv.ProposalID + ", which undoes " + inv.UndoOf + "\n"

	_, err = gitlabgit.CommitTo(ctx, m.client, params.Project, gitlabgit.CommitRequest{
		Branch:        inv.IdempotencyKey,
		StartSHA:      head.Commit,
		CommitMessage: message,
		Actions:       actions,
	})
	if err != nil {
		return action.Result{}, false, fmt.Errorf("push the revert branch: %w", err)
	}

	return action.Result{}, false, nil
}

// revertPlan is what restoring the files takes.
type revertPlan struct {
	// restores are the files that are not at their earlier content.
	restores []restore
	// conflicts are the files that are neither as the merge request left
	// them nor as they were before it.
	conflicts []string
	// live maps every file of the change to the blob it has now, or nil.
	live map[string]*gitchange.BlobRef
}

// restore puts one file back to a blob.
type restore struct {
	path string
	// blob is the blob the file had before, or empty for a file that was
	// added, which is deleted.
	blob string
	// exists says whether the file is there now.
	exists bool
}

// planRevert reads the files of the change on the base branch and decides
// what restores them. A file that is at its earlier content already needs
// nothing. A file that was added is deleted. Any other file gets the blob it
// had before. Nothing is written, and no earlier content is read yet, so a
// conflict that stops the revert costs no more reads.
func (m *MR) planRevert(ctx context.Context, r *gitlabgit.Reader, sides gitchange.SnapshotFiles) (revertPlan, error) {
	plan := revertPlan{live: make(map[string]*gitchange.BlobRef, len(sides.After))}

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		f, err := r.File(ctx, path)
		if err != nil {
			return revertPlan{}, fmt.Errorf("read the file at the base: %w", err)
		}

		if f.Exists {
			plan.live[path] = &gitchange.BlobRef{Blob: f.SHA}
		} else {
			plan.live[path] = nil
		}

		before := gitchange.BlobOf(sides.Before[path])
		if f.SHA == before {
			continue
		}

		if f.SHA != sides.After[path].Blob {
			plan.conflicts = append(plan.conflicts, path)
		}

		plan.restores = append(plan.restores, restore{path: path, blob: before, exists: f.Exists})
	}

	return plan, nil
}

// restoreActions turns the restores into the actions of a commit. The
// Commits API takes content and not a blob, so the earlier content of each
// file is read by its blob ID.
func (m *MR) restoreActions(ctx context.Context, project string, restores []restore) ([]gitlabgit.CommitAction, error) {
	actions := make([]gitlabgit.CommitAction, 0, len(restores))

	for _, r := range restores {
		if r.blob == "" {
			actions = append(actions, gitlabgit.CommitAction{Action: gitlabgit.CommitDelete, FilePath: r.path})

			continue
		}

		content, err := gitlabgit.ReadBlobText(ctx, m.client, project, r.blob, r.path+" as it was")
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", r.path, err)
		}

		kind := gitlabgit.CommitCreate
		if r.exists {
			kind = gitlabgit.CommitUpdate
		}

		actions = append(actions, gitlabgit.CommitAction{Action: kind, FilePath: r.path, Content: &content})
	}

	return actions, nil
}

// revertRequest is the merge request that reverts orig. Its text is the
// action's own, apart from the title of the original, so only that is cleaned.
func (m *MR) revertRequest(inv action.Invocation, params Params, orig *gitlabgit.MergeRequest, branch string) gitlabgit.CreateRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Reverts the merge request `%s!%d`, which Sidekick opened for proposal `%s`.\n\n", params.Project, orig.IID, inv.UndoOf)
	b.WriteString("The files were not validated: this change restores their earlier content.\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`, which undoes `%s`.", inv.ProposalID, inv.UndoOf)

	if link := weblink.Investigation(m.publicURL, inv.InvestigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	return gitlabgit.CreateRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        gitlabgit.MergeRequestTitle(revertPrefix+params.Title, m.limits.draft(), fallbackTitle),
		Description:  b.String(),
	}
}
