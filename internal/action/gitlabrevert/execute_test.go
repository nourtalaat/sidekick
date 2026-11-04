package gitlabrevert

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// Calls that change something at GitLab.
const (
	callCreateBranch = "POST repository/branches"
	callRevert       = "POST repository/commits/" + testCommit + "/revert"
	callOpen         = "POST merge_requests"
)

// proposal returns the invocation of the standard commit with the snapshot the
// action takes when the model proposes.
func proposal(t *testing.T, b *bed, p Params) action.Invocation {
	t.Helper()

	inv := invocation(p)

	snap, err := b.rv.Snapshot(t.Context(), inv.Params)
	require.NoError(t, err)

	inv.Snapshot = &snap

	return inv
}

// writes returns the calls the fake received that are not reads, in order.
func writes(b *bed) []string {
	var out []string

	for _, call := range sent(b.w.srv) {
		if !strings.HasPrefix(call, "GET ") {
			out = append(out, call)
		}
	}

	return out
}

func TestExecuteReverts(t *testing.T) {
	t.Parallel()

	t.Run("a commit", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())

		res, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)

		require.Equal(t, "Opened draft merge request "+testProject+"!100", res.Summary)
		require.Equal(t, testWebURL+"100", res.Links[0].URL)
		require.Equal(t, int64(100), res.PullRequest.Number)
		require.Equal(t, testCommit, res.PullRequest.HeadSHA)

		require.Equal(t, []string{callCreateBranch, callRevert, callOpen}, writes(b), "nothing merges, approves or assigns")

		branch := bodyOf(t, b.w.srv, "POST", "repository/branches")
		require.Equal(t, testBranch, branch["branch"])
		require.Equal(t, testHead, branch["ref"], "the branch starts at the head of the base branch")
		require.Equal(t, map[string]any{"branch": testBranch}, bodyOf(t, b.w.srv, "POST", "repository/commits/"+testCommit+"/revert"))

		open := bodyOf(t, b.w.srv, "POST", "merge_requests")
		require.Equal(t, []string{"description", "source_branch", "target_branch", "title"}, sortedKeys(open), "only what the action sets")
		require.Equal(t, testBranch, open["source_branch"])
		require.Equal(t, "main", open["target_branch"])
		require.Equal(t, "Draft: Revert the new replica count", open["title"])

		description, ok := open["description"].(string)
		require.True(t, ok)
		require.Contains(t, description, "It overloads the database.")
		require.Contains(t, description, "Reverts commit `"+testCommit+"`.")
		require.Contains(t, description, "Sidekick-Revert: "+testProposalID)
		require.Contains(t, description, "Opened by Sidekick for proposal `"+testProposalID+"`.")

		require.True(t, b.w.state(100).Draft)
	})

	t.Run("a merged merge request", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		b.w.mergeRequest(12, mergeRequestBody(12, "merged", false, "feature/x", "", testCommit, ""))

		p := commitParams()
		p.Commit, p.MergeRequest = "", 12

		res, err := b.rv.Execute(t.Context(), proposal(t, b, p))
		require.NoError(t, err)
		require.Equal(t, int64(100), res.PullRequest.Number)
		require.Equal(t, []string{callCreateBranch, callRevert, callOpen}, writes(b))
		require.Contains(t, b.w.state(100).Description, "Reverts merge request `!12`.")
	})

	t.Run("the model's title and body cannot make a quick action or a reference", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()

		p := commitParams()
		p.Title = "Revert !12 now"
		p.Body = "/merge\nFixes #3"

		_, err := b.rv.Execute(t.Context(), proposal(t, b, p))
		require.NoError(t, err)

		open := bodyOf(t, b.w.srv, "POST", "merge_requests")
		require.Equal(t, "Draft: Revert `!12` now", open["title"])

		for line := range strings.SplitSeq(open["description"].(string), "\n") {
			require.False(t, strings.HasPrefix(strings.TrimSpace(line), "/"), "a line starts with a slash: %q", line)
		}

		require.Contains(t, open["description"], "`#3`")
	})
}

func TestExecuteRefusesWhatChangedSince(t *testing.T) {
	t.Parallel()

	t.Run("a file changed after the proposal", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())

		b.w.file(testHead, testPath, "replicas: 20\n")

		_, err := b.rv.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrConflict)
		require.ErrorContains(t, err, `"deploy/values.yaml" changed on the base branch after the proposal was made`)
		require.Empty(t, writes(b), "nothing is written")
	})

	t.Run("a file reverted by someone else since", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())

		b.w.file(testHead, testPath, testBefore)

		res, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "reverted on the base branch already")
		require.Nil(t, res.PullRequest)
		require.Empty(t, writes(b))
	})

	t.Run("the commit is too old by now", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())

		b.w.commit(testCommit, []string{testParent}, testNow.AddDate(0, 0, -10))

		_, err := b.rv.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, writes(b))
	})
}

func TestExecuteWhenGitLabCannotRevert(t *testing.T) {
	t.Parallel()

	const message = "Commit revert failed: Can't perform this action automatically. It may have already been done, or a more recent commit may have updated some of this content. Please perform this action locally."

	b := newBed(t, nil)
	b.w.standard()
	inv := proposal(t, b, commitParams())

	b.w.failRevert(message)

	_, err := b.rv.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrConflict)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, message, "GitLab's reason reaches the approver")
	require.Equal(t, []string{callCreateBranch, callRevert, "DELETE repository/branches/" + testBranch}, writes(b), "the branch is deleted and no merge request is opened")
	require.False(t, b.w.hasBranch(testBranch))
}

func TestExecuteChecksTheInvocation(t *testing.T) {
	t.Parallel()

	t.Run("no snapshot", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()

		_, err := b.rv.Execute(t.Context(), invocation(commitParams()))
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorIs(t, err, errNoSnapshot)
		require.Empty(t, writes(b))
	})

	t.Run("a key that is not a branch of a proposal", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		inv := invocation(commitParams())
		inv.IdempotencyKey = "main"

		_, err := b.rv.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorIs(t, err, gitlabgit.ErrNoBranch)
		require.Empty(t, b.w.srv.Requests())
	})

	t.Run("a project that is not allowed", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		p := commitParams()
		p.Project = "acme/other/project"

		_, err := b.rv.Execute(t.Context(), invocation(p))
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, b.w.srv.Requests())
	})

	t.Run("a snapshot that does not describe the commit", func(t *testing.T) {
		t.Parallel()

		stale := gitchange.AfterEntry{Blob: gitchange.BlobSHA("x"), Status: gitchange.FileChanged}

		tests := []struct {
			name    string
			snap    action.Snapshot
			wantErr string
		}{
			{name: "an empty one", snap: action.Snapshot{}, wantErr: "decode the before side"},
			{name: "no file", snap: action.Snapshot{Before: json.RawMessage(`{}`), After: json.RawMessage(`{}`)}, wantErr: "describes no file"},
			{name: "a null side", snap: action.Snapshot{Before: json.RawMessage(`null`), After: json.RawMessage(`null`)}, wantErr: "describes no file"},
			{name: "a file the commit does not change", snap: action.Snapshot{Before: json.RawMessage(`{}`), After: mustJSON(t, map[string]any{"other.yaml": stale})}, wantErr: "does not change"},
			{name: "a missing before entry", snap: action.Snapshot{Before: json.RawMessage(`{}`), After: mustJSON(t, map[string]any{testPath: gitchange.AfterEntry{Blob: gitchange.BlobSHA(testBefore), Status: gitchange.FileChanged}})}, wantErr: "is not what"},
			{name: "another blob", snap: action.Snapshot{Before: mustJSON(t, map[string]any{testPath: gitchange.BlobRef{Blob: gitchange.BlobSHA(testAfter)}}), After: mustJSON(t, map[string]any{testPath: stale})}, wantErr: "is not what"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				b := newBed(t, nil)
				b.w.standard()

				inv := invocation(commitParams())
				inv.Snapshot = &tt.snap

				_, err := b.rv.Execute(t.Context(), inv)
				require.ErrorIs(t, err, action.ErrPermanent)
				require.ErrorContains(t, err, tt.wantErr)
				require.Empty(t, writes(b))
			})
		}
	})
}

func TestExecuteAfterACrash(t *testing.T) {
	t.Parallel()

	t.Run("a repeat makes one branch and one merge request", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())

		first, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)

		second, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)

		require.Equal(t, first.PullRequest.Number, second.PullRequest.Number)
		require.Equal(t, "Found the already opened draft merge request "+testProject+"!100", second.Summary)
		require.Equal(t, []string{callCreateBranch, callRevert, callOpen}, writes(b), "the second call changes nothing")
	})

	t.Run("the branch was made and the revert was not", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.setBranch(testBranch, true)

		res, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Equal(t, int64(100), res.PullRequest.Number)
		require.Equal(t, []string{callRevert, callOpen}, writes(b), "the branch is used, and not made again")
	})

	t.Run("the revert was made and the merge request was not", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.setBranch(testBranch, true)
		b.w.setAhead(1)

		res, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Equal(t, int64(100), res.PullRequest.Number)
		require.Equal(t, []string{callOpen}, writes(b), "the revert is not made twice")
	})

	t.Run("the revert was made and the base branch has moved on", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.setBranch(testBranch, true)
		b.w.setAhead(1)
		b.w.file(testHead, testPath, testBefore)

		_, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Equal(t, []string{callOpen}, writes(b), "the files are not checked again once the revert is made")
	})

	t.Run("the branch has more commits than a revert makes", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.setBranch(testBranch, true)
		b.w.setAhead(2)

		_, err := b.rv.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "has 2 commits")
		require.Empty(t, writes(b))
	})

	t.Run("the branch was made and the files were reverted by someone else", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.setBranch(testBranch, true)
		b.w.file(testHead, testPath, testBefore)

		res, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "reverted on the base branch already")
		require.Equal(t, []string{"DELETE repository/branches/" + testBranch}, writes(b), "the empty branch is deleted")
	})

	t.Run("the merge request was merged since", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.addMergeRequest(&fakeMR{IID: 100, State: "merged", Source: testBranch})

		res, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Equal(t, "Found merge request "+testProject+"!100, which has been merged since it was opened", res.Summary)
		require.Empty(t, writes(b))
	})

	t.Run("the merge request was closed since", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.addMergeRequest(&fakeMR{IID: 100, State: "closed", Draft: true, Source: testBranch})

		res, err := b.rv.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "which has been closed since it was opened")
		require.Empty(t, writes(b))
	})
}

func TestExecuteNeverLeavesAMergeRequestThatIsNotADraft(t *testing.T) {
	t.Parallel()

	t.Run("one an earlier attempt made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.setBranch(testBranch, true)
		b.w.addMergeRequest(&fakeMR{IID: 100, State: "opened", Draft: false, Source: testBranch})

		_, err := b.rv.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "was not a draft")
		require.Equal(t, "closed", b.w.state(100).State)
		require.False(t, b.w.hasBranch(testBranch))
	})

	t.Run("one GitLab opened as a merge request that is not a draft", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.standard()
		inv := proposal(t, b, commitParams())
		b.w.ignoreDraft()

		_, err := b.rv.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.Equal(t, "closed", b.w.state(100).State)
		require.False(t, b.w.hasBranch(testBranch))
	})
}

// mustJSON encodes v.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	return raw
}

// sortedKeys returns the keys of a decoded JSON object, sorted.
func sortedKeys(m map[string]any) []string {
	return slices.Sorted(maps.Keys(m))
}
