package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// revertPrefix starts the title of a revert merge request.
const revertPrefix = "Revert: "

var errNoUndoOf = fmt.Errorf("%w: the call does not say which proposal it undoes", action.ErrPermanent)

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
	branch, err := undoBranches(inv)
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

	orig, err := m.mergeRequestFrom(ctx, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the merge request to undo: %w", err)
	}

	if orig == nil {
		return action.Result{}, fmt.Errorf("%w: no merge request was made from the branch %s", action.ErrPermanent, branch)
	}

	if stateOf(orig) == gitchange.StateMerged {
		return m.revert(ctx, inv, params, orig, snap, force)
	}

	return m.closeOriginal(ctx, inv, params, orig, branch)
}

// undoBranches checks the invocation of an undo and returns the branch of the
// proposal that is undone.
func undoBranches(inv action.Invocation) (string, error) {
	if inv.UndoOf == "" {
		return "", errNoUndoOf
	}

	branch := action.IdempotencyKey(inv.UndoOf)
	if !branchOfProposal.MatchString(branch) || !branchOfProposal.MatchString(inv.IdempotencyKey) {
		return "", fmt.Errorf("%w: %w", action.ErrPermanent, errNoBranch)
	}

	return branch, nil
}

// closeOriginal closes the merge request, if it is open, deletes its branch
// and records it as closed.
func (m *MR) closeOriginal(ctx context.Context, inv action.Invocation, params Params, orig *apiMergeRequest, branch string) (action.Result, error) {
	title := params.Project + "!" + strconv.Itoa(orig.IID)
	closed, summary := orig, "The merge request "+title+" was closed already; its branch is deleted."

	if stateOf(orig) == gitchange.StateOpen {
		var updated apiMergeRequest

		err := m.client.Send(ctx, http.MethodPut, mergeRequestPath(params.Project, orig.IID), updateRequest{StateEvent: "close"}, &updated)
		if err != nil {
			return action.Result{}, classify("close the merge request", err)
		}

		closed, summary = &updated, "Closed the merge request "+title+" and deleted its branch."
	}

	if err := m.deleteBranch(ctx, params.Project, branch); err != nil {
		return action.Result{}, fmt.Errorf("remove the branch of the closed merge request: %w", err)
	}

	st := statusOf(closed)
	if st.State == gitchange.StateOpen {
		st.State, st.ClosedAt = gitchange.StateClosed, new(time.Now())
	}

	if st.Number == 0 {
		st.Number, st.URL = orig.IID, orig.WebURL
	}

	if err := m.record(ctx, inv.UndoOf, params.Project, st); err != nil {
		return action.Result{}, fmt.Errorf("record the original as %s: %w", st.State, err)
	}

	m.metrics.mergeRequest(resultClosed)

	return action.Result{Summary: summary, Links: []action.Link{{Title: title, URL: orig.WebURL}}}, nil
}

// deleteBranch deletes a branch. A branch that is gone already is not an
// error.
func (m *MR) deleteBranch(ctx context.Context, project, branch string) error {
	err := m.client.Send(ctx, http.MethodDelete, branchPath(project, branch), nil, nil)
	if gitlabapi.IsStatus(err, http.StatusNotFound) {
		return nil
	}

	if err != nil {
		return classify("delete the branch "+branch, err)
	}

	return nil
}

// revert opens a merge request that restores the files of a merged merge
// request to their earlier content.
func (m *MR) revert(ctx context.Context, inv action.Invocation, params Params, orig *apiMergeRequest, snap action.Snapshot, force bool) (action.Result, error) {
	branch := inv.IdempotencyKey

	if err := m.record(ctx, inv.UndoOf, params.Project, statusOf(orig)); err != nil {
		return action.Result{}, fmt.Errorf("record the original as merged: %w", err)
	}

	mr, err := m.mergeRequestFrom(ctx, params.Project, branch)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if mr == nil {
		branchExists, err := m.branchExists(ctx, params.Project, branch)
		if err != nil {
			return action.Result{}, fmt.Errorf("look for the branch of an earlier attempt: %w", err)
		}

		if !branchExists {
			res, done, err := m.pushRevert(ctx, inv, params, snap, force)
			if err != nil {
				return action.Result{}, fmt.Errorf("revert the files: %w", err)
			}

			if done {
				return res, nil
			}
		}

		mr, err = m.openMergeRequest(ctx, params.Project, m.revertRequest(inv, params, orig, branch))
		if err != nil {
			return action.Result{}, fmt.Errorf("make the revert merge request: %w", err)
		}
	}

	if err := m.record(ctx, inv.ProposalID, params.Project, statusOf(mr)); err != nil {
		return action.Result{}, fmt.Errorf("record the revert merge request: %w", err)
	}

	m.metrics.mergeRequest(resultReverted)

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

	head, err := baseOf(ctx, m.client, params.Project, params.BaseBranch)
	if err != nil {
		return action.Result{}, false, fmt.Errorf("read the base branch: %w", err)
	}

	plan, err := m.planRevert(ctx, newReader(m.client, params.Project, head.commit), sides)
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

	message := revertPrefix + source.CutText(commitTitle(params.Title), maxCommitTitleBytes-len(revertPrefix)) +
		"\n\nSidekick proposal " + inv.ProposalID + ", which undoes " + inv.UndoOf + "\n"

	err = m.commitTo(ctx, params.Project, commitRequest{
		Branch:        inv.IdempotencyKey,
		StartSHA:      head.commit,
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
func (m *MR) planRevert(ctx context.Context, r *reader, sides gitchange.SnapshotFiles) (revertPlan, error) {
	plan := revertPlan{live: make(map[string]*gitchange.BlobRef, len(sides.After))}

	for _, path := range slices.Sorted(maps.Keys(sides.After)) {
		f, err := r.file(ctx, path)
		if err != nil {
			return revertPlan{}, fmt.Errorf("read the file at the base: %w", err)
		}

		if f.exists {
			plan.live[path] = &gitchange.BlobRef{Blob: f.sha}
		} else {
			plan.live[path] = nil
		}

		before := gitchange.BlobOf(sides.Before[path])
		if f.sha == before {
			continue
		}

		if f.sha != sides.After[path].Blob {
			plan.conflicts = append(plan.conflicts, path)
		}

		plan.restores = append(plan.restores, restore{path: path, blob: before, exists: f.exists})
	}

	return plan, nil
}

// restoreActions turns the restores into the actions of a commit. The
// Commits API takes content and not a blob, so the earlier content of each
// file is read by its blob ID.
func (m *MR) restoreActions(ctx context.Context, project string, restores []restore) ([]commitAction, error) {
	actions := make([]commitAction, 0, len(restores))

	for _, r := range restores {
		if r.blob == "" {
			actions = append(actions, commitAction{Action: commitDelete, FilePath: r.path})

			continue
		}

		content, err := readBlobText(ctx, m.client, project, r.blob, r.path+" as it was")
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", r.path, err)
		}

		kind := commitCreate
		if r.exists {
			kind = commitUpdate
		}

		actions = append(actions, commitAction{Action: kind, FilePath: r.path, Content: &content})
	}

	return actions, nil
}

// revertRequest is the merge request that reverts orig. Its text is the
// action's own, apart from the title of the original, so only that is cleaned.
func (m *MR) revertRequest(inv action.Invocation, params Params, orig *apiMergeRequest, branch string) createRequest {
	var b strings.Builder

	fmt.Fprintf(&b, "Reverts the merge request `%s!%d`, which Sidekick opened for proposal `%s`.\n\n", params.Project, orig.IID, inv.UndoOf)
	b.WriteString("The files were not validated: this change restores their earlier content.\n\n---\n")
	fmt.Fprintf(&b, "Opened by Sidekick for proposal `%s`, which undoes `%s`.", inv.ProposalID, inv.UndoOf)

	if link := weblink.Investigation(m.publicURL, inv.InvestigationID); link != "" {
		fmt.Fprintf(&b, " [The investigation](%s).", link)
	}

	return createRequest{
		SourceBranch: branch,
		TargetBranch: params.BaseBranch,
		Title:        mergeRequestTitle(revertPrefix+params.Title, m.limits.draft()),
		Description:  b.String(),
	}
}

// record keeps the state of a merge request for the proposal with the ID, for
// the count of merged merge requests. Where there is no database, it does
// nothing.
func (m *MR) record(ctx context.Context, proposalID, project string, st *gitchange.Status) error {
	if m.db == nil {
		return nil
	}

	if err := gitchange.RecordState(ctx, m.db, proposalID, project, st); err != nil {
		return fmt.Errorf("record the merge request state: %w", err)
	}

	return nil
}

// mergeRequestPath returns the API path of a merge request of a project.
func mergeRequestPath(project string, iid int) string {
	return gitlabapi.ProjectPath(project, "merge_requests", strconv.Itoa(iid))
}
