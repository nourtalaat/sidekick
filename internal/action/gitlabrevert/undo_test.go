package gitlabrevert

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// Calls that change something at GitLab when an undo runs.
const (
	callPutBack  = "POST repository/commits"
	callCloseOne = "PUT merge_requests/100"
)

// origIID is the IID of the merge request of the proposal that is undone, when
// the test makes it.
const origIID = 50

// blob serves the blob with the text, by its ID.
func (w *world) blob(text string) {
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/blobs/"+gitchange.BlobSHA(text)), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, blobBody(text))
	})
}

// removeFile takes a file out of the repository at the ref.
func (w *world) removeFile(ref, path string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.files[ref], path)
}

// undoInvocation turns the invocation of a proposal into the call that undoes
// it, as a new proposal.
func undoInvocation(inv action.Invocation) action.Invocation {
	inv.ProposalID = testUndoID
	inv.IdempotencyKey = action.IdempotencyKey(testUndoID)
	inv.UndoOf = testProposalID
	inv.Snapshot = nil

	return inv
}

// mergedRevert makes the world as it is after a person merged the revert merge
// request of the proposal: the head has what the parent had.
func mergedRevert(b *bed) {
	b.w.addMergeRequest(&fakeMR{IID: origIID, State: "merged", Draft: true, Source: testBranch})
	b.w.file(testHead, testPath, testBefore)
	b.w.blob(testAfter)
}

func TestUndoOfAMergeRequestThatIsNotMerged(t *testing.T) {
	t.Parallel()

	t.Run("an open one is closed and its branch is deleted", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.addMergeRequest(&fakeMR{IID: 100, State: "opened", Draft: true, Source: testBranch})
		b.w.setBranch(testBranch, true)

		res, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.NoError(t, err)
		require.Equal(t, "Closed the merge request "+testProject+"!100 and deleted its branch.", res.Summary)
		require.Equal(t, []string{callCloseOne, "DELETE repository/branches/" + testBranch}, writes(b))
		require.Equal(t, "closed", b.w.state(100).State)
		require.False(t, b.w.hasBranch(testBranch))
	})

	t.Run("a closed one only has its branch deleted", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.addMergeRequest(&fakeMR{IID: 100, State: "closed", Draft: true, Source: testBranch})
		b.w.setBranch(testBranch, true)

		res, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "was closed already; its branch is deleted")
		require.Equal(t, []string{"DELETE repository/branches/" + testBranch}, writes(b))
	})

	t.Run("a repeat changes nothing more", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.addMergeRequest(&fakeMR{IID: 100, State: "opened", Draft: true, Source: testBranch})
		b.w.setBranch(testBranch, true)

		undo := undoInvocation(inv)

		_, err := b.rv.Undo(t.Context(), undo, *inv.Snapshot, false)
		require.NoError(t, err)

		before := len(writes(b))

		_, err = b.rv.Undo(t.Context(), undo, *inv.Snapshot, false)
		require.NoError(t, err)
		require.Equal(t, []string{callCloseOne, "DELETE repository/branches/" + testBranch, "DELETE repository/branches/" + testBranch}, writes(b), "the repeat only tries to delete a branch that is gone")
		require.Equal(t, 2, before)
	})
}

func TestUndoOfAMergedRevert(t *testing.T) {
	t.Parallel()

	t.Run("a merge request puts the files back", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)

		res, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.NoError(t, err)

		require.Contains(t, res.Summary, "Opened the draft merge request "+testProject+"!100")
		require.Equal(t, int64(100), res.PullRequest.Number)
		require.Equal(t, []string{callPutBack, callOpen}, writes(b), "nothing merges")

		commit := bodyOf(t, b.w.srv, "POST", "repository/commits")
		require.Equal(t, testUndoBranch, commit["branch"])
		require.Equal(t, testHead, commit["start_sha"], "the commit is built on the head of the base branch")
		require.Contains(t, commit["commit_message"], "Sidekick-Revert: "+testUndoID)
		require.Equal(t, []any{map[string]any{"action": "update", "file_path": testPath, "content": testAfter}}, commit["actions"])

		open := bodyOf(t, b.w.srv, "POST", "merge_requests")
		require.Equal(t, testUndoBranch, open["source_branch"])
		require.Equal(t, "Draft: Undo: Revert the new replica count", open["title"])
		require.Contains(t, open["description"], "Sidekick-Revert: "+testUndoID)
		require.True(t, b.w.state(100).Draft)
	})

	t.Run("a file the commit added is created again", func(t *testing.T) {
		t.Parallel()

		const path = "deploy/new.yaml"

		const text = "a: 1\nb: 2\n"

		b := newBed(t, nil)
		b.w.standard()
		b.w.diff(testCommit, diffEntry(path, path, "0", "100644"))
		b.w.file(testHead, path, text)
		b.w.file(testCommit, path, text)
		inv := proposal(t, b, commitParams())

		b.w.addMergeRequest(&fakeMR{IID: origIID, State: "merged", Draft: true, Source: testBranch})
		b.w.removeFile(testHead, path)
		b.w.blob(text)

		_, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.NoError(t, err)

		commit := bodyOf(t, b.w.srv, "POST", "repository/commits")
		require.Equal(t, []any{map[string]any{"action": "create", "file_path": path, "content": text}}, commit["actions"])
	})

	t.Run("files that are as the commit left them need nothing", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.addMergeRequest(&fakeMR{IID: origIID, State: "merged", Draft: true, Source: testBranch})

		res, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "Nothing to put back")
		require.Nil(t, res.PullRequest)
		require.Empty(t, writes(b))
	})

	t.Run("a file changed since the revert is a conflict that carries the live blobs", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)
		b.w.file(testHead, testPath, "replicas: 99\n")

		_, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.ErrorIs(t, err, action.ErrConflict)
		require.Empty(t, writes(b))

		conflict, ok := errors.AsType[*action.ConflictError](err)
		require.True(t, ok)

		var live map[string]*gitchange.BlobRef

		require.NoError(t, json.Unmarshal(conflict.Live, &live))
		require.Equal(t, gitchange.BlobSHA("replicas: 99\n"), live[testPath].Blob)
	})

	t.Run("force overwrites the file that changed", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)
		b.w.file(testHead, testPath, "replicas: 99\n")

		_, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, true)
		require.NoError(t, err)
		require.Equal(t, []string{callPutBack, callOpen}, writes(b))
	})

	t.Run("a repeat finds the merge request it made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)

		undo := undoInvocation(inv)

		first, err := b.rv.Undo(t.Context(), undo, *inv.Snapshot, false)
		require.NoError(t, err)

		second, err := b.rv.Undo(t.Context(), undo, *inv.Snapshot, false)
		require.NoError(t, err)

		require.Equal(t, first.PullRequest.Number, second.PullRequest.Number)
		require.Equal(t, []string{callPutBack, callOpen}, writes(b), "one branch, one merge request")
	})

	t.Run("a crash after the branch was made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)
		b.w.setBranch(testUndoBranch, true)

		res, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.NoError(t, err)
		require.Equal(t, int64(100), res.PullRequest.Number)
		require.Equal(t, []string{callOpen}, writes(b), "the branch is used, and no second commit is made")
	})

	t.Run("a merge request that is not a draft is closed", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)
		b.w.ignoreDraft()

		_, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.Equal(t, "closed", b.w.state(100).State)
		require.False(t, b.w.hasBranch(testUndoBranch))
	})

	t.Run("a path that is refused now", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)
		b.rv.limits.RefusedPaths = []string{"deploy/**"}

		_, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, writes(b))
	})

	t.Run("a file that is not text now", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		mergedRevert(b)
		b.w.file(testHead, testPath, "a\x00b")

		_, err := b.rv.Undo(t.Context(), undoInvocation(inv), *inv.Snapshot, false)
		require.ErrorIs(t, err, action.ErrLimit, "the approver reads why")
		require.Empty(t, writes(b))
	})

	t.Run("a snapshot that is missing or empty", func(t *testing.T) {
		t.Parallel()

		for name, snap := range map[string]action.Snapshot{
			"the zero snapshot": {},
			"no files":          {Before: json.RawMessage(`{}`), After: json.RawMessage(`{}`)},
			"null sides":        {Before: json.RawMessage(`null`), After: json.RawMessage(`null`)},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				b := newBed(t, nil)
				b.w.standard()
				inv := proposal(t, b, commitParams())
				mergedRevert(b)

				_, err := b.rv.Undo(t.Context(), undoInvocation(inv), snap, false)
				require.ErrorIs(t, err, action.ErrPermanent)
				require.Empty(t, writes(b))
			})
		}
	})
}

func TestUndoChecksTheCall(t *testing.T) {
	t.Parallel()

	t.Run("a project that is not allowed", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		p := commitParams()
		p.Project = "acme/other/project"

		_, err := b.rv.Undo(t.Context(), undoInvocation(invocation(p)), action.Snapshot{}, false)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, b.w.srv.Requests())
	})

	t.Run("no proposal to undo", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		inv := undoInvocation(invocation(commitParams()))
		inv.UndoOf = ""

		_, err := b.rv.Undo(t.Context(), inv, action.Snapshot{}, false)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.Empty(t, b.w.srv.Requests())
	})

	t.Run("no merge request was made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		_, err := b.rv.Undo(t.Context(), undoInvocation(invocation(commitParams())), action.Snapshot{}, false)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "no merge request was made from the branch")
		require.Empty(t, writes(b))
	})

	t.Run("parameters that do not decode", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		inv := undoInvocation(invocation(commitParams()))
		inv.Params = json.RawMessage(`{"project":"acme/platform/deploy","draft":true}`)

		_, err := b.rv.Undo(t.Context(), inv, action.Snapshot{}, false)
		require.Error(t, err)
		require.Empty(t, b.w.srv.Requests())
	})
}
