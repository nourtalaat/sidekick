package gitlabcode

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// The files of the change that the undo tests revert.
const (
	undoNewPath = "src/billing/new.go"
	undoOldPath = "src/billing/old.go"
	undoNewText = "package billing\n"
	undoOldText = "package billing\n\nvar x = 1\n"
)

// undoInvocation returns the invocation that undoes the proposal of
// undoParams.
func undoInvocation(params Params) action.Invocation {
	inv := invocation(params)
	inv.ProposalID = testUndoID
	inv.IdempotencyKey = action.IdempotencyKey(testUndoID)
	inv.UndoOf = testProposalID

	return inv
}

// undoParams returns the parameters of the proposal that is undone.
func undoParams() Params {
	p := validParams()
	p.Files = []gitchange.FileChange{
		{Path: testPath, Content: text(testAfter)},
		{Path: undoNewPath, Content: text(undoNewText)},
		{Path: undoOldPath, Delete: true},
	}

	return p
}

// undoSnapshot returns the snapshot of a change that updates a file, adds one
// and deletes one, as Snapshot makes it.
func undoSnapshot() action.Snapshot {
	return action.Snapshot{
		Target:  "gitlab:" + testProject,
		Version: testBaseSHA,
		Before: json.RawMessage(`{
			"` + testPath + `": {"blob": "` + blobOf(testBefore) + `"},
			"` + undoNewPath + `": null,
			"` + undoOldPath + `": {"blob": "` + blobOf(undoOldText) + `"}}`),
		After: json.RawMessage(`{
			"` + testPath + `": {"blob": "` + blobOf(testAfter) + `", "status": "changed", "linesAdded": 1, "linesRemoved": 1},
			"` + undoNewPath + `": {"blob": "` + blobOf(undoNewText) + `", "status": "added", "linesAdded": 1, "linesRemoved": 0},
			"` + undoOldPath + `": {"status": "deleted", "linesRemoved": 3}}`),
	}
}

// mergedBed returns a bed whose original merge request was merged and whose
// base branch holds the files as the change left them. The revert branch does
// not exist.
func mergedBed(t *testing.T) *bed {
	t.Helper()

	return mergedBedWith(t, repo{testPath: regular(testAfter), undoNewPath: regular(undoNewText)}, true)
}

// mergedBedWith is mergedBed for a base branch that holds live, and for a
// revert merge request that GitLab makes a draft or not.
func mergedBedWith(t *testing.T, live repo, draft bool) *bed {
	t.Helper()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
	b.serveRepo(live)
	b.expectMissing(undoOldPath)
	b.expectBlob(testBefore)
	b.expectBlob(undoOldText)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
	b.expectCommit()
	b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(30, "opened", draft, testUndoBranch)))

	return b
}

func TestUndoClosesAnOpenMergeRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "opened", true))})
	b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(mergeRequestBody(12, "closed", true)))
	b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), noContent)

	res, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Equal(t, "Closed the merge request "+testProject+"!12 and deleted its branch.", res.Summary)
	require.Equal(t, []string{"GET merge_requests", "PUT merge_requests/12", "DELETE repository/branches/" + testBranch}, sent(b.gitlab))
	require.JSONEq(t, `{"state_event":"close"}`, string(b.gitlab.Requests()[1].Body))
}

func TestUndoOfAClosedMergeRequestOnlyDeletesTheBranch(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "closed", true))})
	b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), failed(http.StatusNotFound, "404 Branch Not Found"))

	res, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err, "a branch that is gone already is not an error")
	require.Contains(t, res.Summary, "was closed already")
	require.Equal(t, []string{"DELETE repository/branches/" + testBranch}, writes(b.gitlab))
}

func TestUndoRevertsAMergedMergeRequest(t *testing.T) {
	t.Parallel()

	b := mergedBed(t)

	res, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Opened the revert draft merge request "+testProject+"!30")
	require.Contains(t, res.Summary, "regular file, even if it was executable")
	require.Equal(t, &action.PullRequestRef{Number: 30, HeadSHA: testCommitSHA}, res.PullRequest)
	require.Equal(t, []string{"POST repository/commits", "POST merge_requests"}, writes(b.gitlab))

	commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))
	require.Equal(t, testUndoBranch, commit["branch"])
	require.Equal(t, testBaseSHA, commit["start_sha"])
	require.Equal(t, "Revert: Fix the total of an invoice\n\nSidekick proposal "+testUndoID+", which undoes "+testProposalID+"\n", commit["commit_message"])
	require.Equal(t, []any{
		map[string]any{"action": "update", "file_path": testPath, "content": testBefore},
		map[string]any{"action": "delete", "file_path": undoNewPath},
		map[string]any{"action": "create", "file_path": undoOldPath, "content": undoOldText},
	}, commit["actions"])

	mr := bodyOf(t, b.gitlab, http.MethodPost, apiPath("merge_requests"))
	require.ElementsMatch(t, []string{"description", "source_branch", "target_branch", "title"}, keysOf(mr))
	require.Equal(t, testUndoBranch, mr["source_branch"])
	require.Equal(t, "Draft: Revert: Fix the total of an invoice", mr["title"])
	require.Contains(t, mr["description"], "which undoes `"+testProposalID+"`")
}

func TestUndoConflictAndForce(t *testing.T) {
	t.Parallel()

	const edited = "package billing\n\n// someone edited this\n"

	t.Run("a file changed since is a conflict that carries the live blobs", func(t *testing.T) {
		t.Parallel()

		b := mergedBedWith(t, repo{testPath: regular(edited), undoNewPath: regular(undoNewText)}, true)

		_, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.ErrorIs(t, err, action.ErrConflict)

		conflict, ok := errors.AsType[*action.ConflictError](err)
		require.True(t, ok)

		var live map[string]*gitchange.BlobRef

		require.NoError(t, json.Unmarshal(conflict.Live, &live))
		require.Equal(t, blobOf(edited), live[testPath].Blob)
		require.Nil(t, live[undoOldPath])
		require.Empty(t, writes(b.gitlab))
	})

	t.Run("force overwrites it", func(t *testing.T) {
		t.Parallel()

		b := mergedBedWith(t, repo{testPath: regular(edited), undoNewPath: regular(undoNewText)}, true)

		_, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), true)
		require.NoError(t, err)
		require.Equal(t, []string{"POST repository/commits", "POST merge_requests"}, writes(b.gitlab))
	})
}

func TestUndoWithNothingToRevert(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
	b.serveRepo(repo{testPath: regular(testBefore), undoOldPath: regular(undoOldText)})
	b.expectMissing(undoNewPath)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)

	res, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Nothing to revert")
	require.Empty(t, writes(b.gitlab))
}

func TestUndoAfterACrash(t *testing.T) {
	t.Parallel()

	t.Run("the revert branch exists and the merge request was not made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), reply(branchBody(testCommitSHA)))
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(31, "opened", true, testUndoBranch)))

		res, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.NoError(t, err)
		require.Equal(t, int64(31), res.PullRequest.Number)
		require.Equal(t, []string{"POST merge_requests"}, writes(b.gitlab), "the files are not written again")
	})

	t.Run("the revert merge request exists and is a draft", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{
			testBranch:     listOf(mergeRequestBody(12, "merged", false)),
			testUndoBranch: listOf(mergeRequestBodyFrom(31, "opened", true, testUndoBranch)),
		})

		res, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.NoError(t, err)
		require.Equal(t, int64(31), res.PullRequest.Number)
		require.Empty(t, writes(b.gitlab))
	})

	t.Run("the revert merge request exists and is not a draft", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{
			testBranch:     listOf(mergeRequestBody(12, "merged", false)),
			testUndoBranch: listOf(mergeRequestBodyFrom(31, "opened", false, testUndoBranch)),
		})
		b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/31"), reply(mergeRequestBodyFrom(31, "closed", false, testUndoBranch)))
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testUndoBranch), noContent)

		_, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.Equal(t, []string{"PUT merge_requests/31", "DELETE repository/branches/" + testUndoBranch}, writes(b.gitlab))
	})

	t.Run("the revert merge request that GitLab made is not a draft", func(t *testing.T) {
		t.Parallel()

		b := mergedBedWith(t, repo{testPath: regular(testAfter), undoNewPath: regular(undoNewText)}, false)
		b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/30"), reply(mergeRequestBodyFrom(30, "closed", false, testUndoBranch)))
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testUndoBranch), noContent)

		_, err := b.code.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.ErrorIs(t, err, action.ErrPermanent)
	})
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	snapshotWith := func(path string) action.Snapshot {
		return action.Snapshot{
			Before: json.RawMessage(`{"` + path + `": {"blob": "` + blobOf("x\n") + `"}}`),
			After:  json.RawMessage(`{"` + path + `": {"blob": "` + blobOf("y\n") + `", "status": "changed"}}`),
		}
	}

	tests := []struct {
		name    string
		setup   func(b *bed)
		inv     func() action.Invocation
		snap    action.Snapshot
		wantErr []error
		want    string
	}{
		{
			name: "a project the config no longer lists",
			inv:  func() action.Invocation { p := undoParams(); p.Project = "acme/other"; return undoInvocation(p) },
			snap: undoSnapshot(), wantErr: []error{action.ErrLimit, action.ErrPermanent}, want: "is not one of limits.projects",
		},
		{
			name: "a call that does not say what it undoes",
			inv:  func() action.Invocation { inv := undoInvocation(undoParams()); inv.UndoOf = ""; return inv },
			snap: undoSnapshot(), wantErr: []error{gitlabgit.ErrNoUndoOf, action.ErrPermanent},
		},
		{
			name:  "no merge request was made from the branch",
			setup: func(b *bed) { b.handleMergeRequests(nil) },
			inv:   func() action.Invocation { return undoInvocation(undoParams()) },
			snap:  undoSnapshot(), wantErr: []error{action.ErrPermanent}, want: "no merge request was made",
		},
		{
			name: "a path that a refused glob matches now",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
				b.serveRepo(repo{".github/workflows/ci.yml": regular("y\n")})
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
			},
			inv:  func() action.Invocation { return undoInvocation(undoParams()) },
			snap: snapshotWith(".github/workflows/ci.yml"), wantErr: []error{action.ErrLimit, action.ErrPermanent}, want: "CI and build automation",
		},
		{
			name: "a file that is a symlink now",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
				b.serveRepo(repo{testPath: node{text: "t", mode: gitlabgit.ModeSymlink}})
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
			},
			inv:  func() action.Invocation { return undoInvocation(undoParams()) },
			snap: snapshotWith(testPath), wantErr: []error{action.ErrLimit, action.ErrPermanent}, want: "is a symlink",
		},
		{
			name: "a file that is a directory now",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
				b.serveRepo(repo{testPath + "/x.go": regular("y\n")})
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
			},
			inv:  func() action.Invocation { return undoInvocation(undoParams()) },
			snap: snapshotWith(testPath), wantErr: []error{action.ErrLimit, action.ErrPermanent}, want: "is a directory",
		},
		{
			name: "an empty snapshot",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
			},
			inv:  func() action.Invocation { return undoInvocation(undoParams()) },
			snap: action.Snapshot{}, wantErr: []error{action.ErrPermanent},
		},
		{
			name: "a snapshot with null sides",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
			},
			inv:  func() action.Invocation { return undoInvocation(undoParams()) },
			snap: action.Snapshot{Before: []byte("null"), After: []byte("null")}, wantErr: []error{action.ErrPermanent}, want: "lists no files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			if tt.setup != nil {
				tt.setup(b)
			}

			require.NotPanics(t, func() {
				_, err := b.code.Undo(t.Context(), tt.inv(), tt.snap, false)
				require.Error(t, err)

				for _, want := range tt.wantErr {
					require.ErrorIs(t, err, want)
				}

				if tt.want != "" {
					require.ErrorContains(t, err, tt.want)
				}
			})
			require.Empty(t, writes(b.gitlab))
		})
	}
}
