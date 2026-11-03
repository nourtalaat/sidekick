package githubcode

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

const (
	newGoPath  = "internal/billing/new.go"
	oldGoPath  = "internal/billing/old.go"
	newGoText  = "package billing\n\nvar New = 2\n"
	oldGoText  = "#!/bin/sh\necho old\n"
	origHead   = "acme:sidekick/" + testProposalID
	revertHead = "acme:sidekick/" + testUndoID
)

// undoBranch is the branch of the revert pull request.
var undoBranch = "sidekick/" + testUndoID

// mergedPull is a merged pull request.
func mergedPull(number int) string {
	return `{"number":` + strconv.Itoa(number) + `,"draft":true,"state":"closed","merged_at":"2026-11-03T10:00:00Z","html_url":"https://github.com/acme/app/pull/` + strconv.Itoa(number) + `","head":{"sha":"` + testCommitSHA + `"}}`
}

// closedPull is a pull request closed without a merge.
func closedPull(number int) string {
	return `{"number":` + strconv.Itoa(number) + `,"draft":true,"state":"closed","closed_at":"2026-11-03T10:00:00Z","html_url":"https://github.com/acme/app/pull/` + strconv.Itoa(number) + `","head":{"sha":"` + testCommitSHA + `"}}`
}

// undoSnapshot is the snapshot of a pull request that changed the Go file,
// added a file and deleted an executable script.
func undoSnapshot(version string) action.Snapshot {
	return action.Snapshot{
		Target: "github:" + testRepo,
		Before: json.RawMessage(`{
			"` + testGoPath + `": {"blob": "` + gitchange.BlobSHA(testGoBefore) + `"},
			"` + newGoPath + `": null,
			"` + oldGoPath + `": {"blob": "` + gitchange.BlobSHA(oldGoText) + `"}
		}`),
		After: json.RawMessage(`{
			"` + testGoPath + `": {"blob": "` + gitchange.BlobSHA(testGoAfter) + `", "status": "changed", "linesAdded": 1, "linesRemoved": 1},
			"` + newGoPath + `": {"blob": "` + gitchange.BlobSHA(newGoText) + `", "status": "added", "linesAdded": 3, "linesRemoved": 0},
			"` + oldGoPath + `": {"status": "deleted", "linesRemoved": 2}
		}`),
		Version: version,
	}
}

// undoInvocation is the call that undoes the proposal testProposalID.
func undoInvocation() action.Invocation {
	inv := invocation(validParams())
	inv.ProposalID = testUndoID
	inv.IdempotencyKey = action.IdempotencyKey(testUndoID)
	inv.UndoOf = testProposalID

	return inv
}

// mergedState is what the base branch holds after the pull request merged.
func mergedState() repoFiles {
	return repoFiles{testGoPath: regular(testGoAfter), newGoPath: regular(newGoText)}
}

func TestUndoClosesAnOpenPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{origHead: "[" + pullBody(21, true) + "]"})
	b.github.Handle(http.MethodPatch, repoPath("pulls/21"), reply(closedPull(21)))
	b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakeNoContent())

	res, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)
	require.Equal(t, "Closed the pull request acme/app#21 and deleted its branch.", res.Summary)
	require.Equal(t, "closed", bodyOf(t, b.github, http.MethodPatch, repoPath("pulls/21"))["state"])
}

func TestUndoOfAClosedPullRequestOnlyDeletesTheBranch(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{origHead: "[" + closedPull(21) + "]"})
	b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), replyStatus(http.StatusUnprocessableEntity, `{"message":"Reference does not exist"}`))

	res, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "was closed already")

	for _, r := range sent(b.github) {
		require.NotContains(t, r, "PATCH")
	}
}

func TestUndoRevertsAMergedPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]"})
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), notFound)
	b.serveBase(mergedState())
	b.serveCommit(testEarlierSHA, repoFiles{oldGoPath: executable(oldGoText)})
	b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, `{"sha":"`+testTreeSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, `{"sha":"`+testCommitSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusCreated, refBody("refs/heads/"+undoBranch, testCommitSHA)))
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(22, true)))

	res, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Opened the revert draft pull request acme/app#22")
	require.Equal(t, &action.PullRequestRef{Number: 22, HeadSHA: testCommitSHA}, res.PullRequest)

	tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, treeSHA(testBaseSHA, ""), tree["base_tree"])
	require.Equal(t, []any{
		map[string]any{"path": testGoPath, "mode": "100644", "type": "blob", "sha": gitchange.BlobSHA(testGoBefore)},
		map[string]any{"path": newGoPath, "mode": "100644", "type": "blob", "sha": nil},
		map[string]any{"path": oldGoPath, "mode": "100755", "type": "blob", "sha": gitchange.BlobSHA(oldGoText)},
	}, tree["tree"], "a deleted file comes back with the mode it had")

	commit := bodyOf(t, b.github, http.MethodPost, repoPath("git/commits"))
	require.Equal(t, "Revert: Fix the invoice total\n\nSidekick proposal "+testUndoID+", which undoes "+testProposalID+"\n", commit["message"])

	pull := bodyOf(t, b.github, http.MethodPost, repoPath("pulls"))
	require.Equal(t, undoBranch, pull["head"])
	require.Equal(t, "Revert: Fix the invoice total", pull["title"])
	require.Equal(t, true, pull["draft"])
}

func TestUndoRevertKeepsTheLiveModeOfAChangedFile(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]"})
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), notFound)
	b.serveBase(repoFiles{testGoPath: executable(testGoAfter), newGoPath: regular(newGoText), oldGoPath: regular(oldGoText)})
	b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, `{"sha":"`+testTreeSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, `{"sha":"`+testCommitSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusCreated, refBody("refs/heads/"+undoBranch, testCommitSHA)))
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(22, true)))

	_, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)

	tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, []any{
		map[string]any{"path": testGoPath, "mode": "100755", "type": "blob", "sha": gitchange.BlobSHA(testGoBefore)},
		map[string]any{"path": newGoPath, "mode": "100644", "type": "blob", "sha": nil},
	}, tree["tree"], "the file the pull request deleted is back already, so only two entries remain")
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	const moved = "package billing\n\nfunc Total() int {\n\treturn 99\n}\n"

	setup := func(t *testing.T) *bed {
		t.Helper()

		b := newBed(t, nil)
		b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]"})
		b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), notFound)
		b.serveBase(repoFiles{testGoPath: regular(moved), newGoPath: regular(newGoText)})
		b.serveCommit(testEarlierSHA, repoFiles{oldGoPath: regular(oldGoText)})

		return b
	}

	t.Run("without force it carries the live files", func(t *testing.T) {
		t.Parallel()

		_, err := setup(t).code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
		require.ErrorIs(t, err, action.ErrConflict)

		conflict, ok := errors.AsType[*action.ConflictError](err)
		require.True(t, ok)

		var live map[string]*gitchange.BlobRef

		require.NoError(t, json.Unmarshal(conflict.Live, &live))
		require.Equal(t, &gitchange.BlobRef{Blob: gitchange.BlobSHA(moved)}, live[testGoPath])
		require.Equal(t, &gitchange.BlobRef{Blob: gitchange.BlobSHA(newGoText)}, live[newGoPath])
		require.Nil(t, live[oldGoPath])
	})

	t.Run("with force it overwrites them", func(t *testing.T) {
		t.Parallel()

		b := setup(t)
		b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, `{"sha":"`+testTreeSHA+`"}`))
		b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, `{"sha":"`+testCommitSHA+`"}`))
		b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusCreated, refBody("refs/heads/"+undoBranch, testCommitSHA)))
		b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(22, true)))

		_, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), true)
		require.NoError(t, err)
	})
}

func TestUndoWithNothingToRevert(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]"})
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), notFound)
	b.serveBase(repoFiles{testGoPath: regular(testGoBefore), oldGoPath: regular(oldGoText)})

	res, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Nothing to revert")
}

func TestUndoAfterACrashFindsTheRevertPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]", revertHead: "[" + pullBody(22, true) + "]"})
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), reply(refBody("refs/heads/"+undoBranch, testCommitSHA)))

	res, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)
	require.EqualValues(t, 22, res.PullRequest.Number)

	for _, r := range sent(b.github) {
		require.False(t, strings.HasPrefix(r, "POST "), "nothing is written again: %s", r)
	}
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		inv       func() action.Invocation
		snap      func() action.Snapshot
		live      repoFiles
		limits    func(l *Limits)
		pulls     map[string]string
		wantErr   string
		wantIs    error
		noRequest bool
	}{
		{
			name:      "the call does not say what it undoes",
			inv:       func() action.Invocation { inv := undoInvocation(); inv.UndoOf = ""; return inv },
			snap:      func() action.Snapshot { return undoSnapshot("") },
			wantErr:   "does not say which proposal it undoes",
			noRequest: true,
		},
		{
			name:      "an undo with a key that is not a branch",
			inv:       func() action.Invocation { inv := undoInvocation(); inv.IdempotencyKey = "x"; return inv },
			snap:      func() action.Snapshot { return undoSnapshot("") },
			wantErr:   "no idempotency key",
			wantIs:    action.ErrPermanent,
			noRequest: true,
		},
		{
			name:    "no pull request was made",
			inv:     undoInvocation,
			snap:    func() action.Snapshot { return undoSnapshot("") },
			pulls:   map[string]string{},
			wantErr: "no pull request was made from the branch",
			wantIs:  action.ErrPermanent,
		},
		{
			name:    "a file that is a symlink now",
			inv:     undoInvocation,
			snap:    func() action.Snapshot { return undoSnapshot(testEarlierSHA) },
			pulls:   map[string]string{origHead: "[" + mergedPull(21) + "]"},
			live:    repoFiles{testGoPath: {mode: modeSymlink, typ: typeBlob, content: "x.go"}, newGoPath: regular(newGoText)},
			wantErr: "is a symlink",
			wantIs:  action.ErrPermanent,
		},
		{
			name:    "a path a refused glob matches now",
			inv:     undoInvocation,
			snap:    func() action.Snapshot { return undoSnapshot(testEarlierSHA) },
			pulls:   map[string]string{origHead: "[" + mergedPull(21) + "]"},
			live:    mergedState(),
			limits:  func(l *Limits) { l.RefusedPaths = []string{"internal/billing/new.go"} },
			wantErr: "limits.refusedPaths",
			wantIs:  action.ErrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)

			if tt.pulls != nil {
				b.handlePulls(tt.pulls)
				b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), notFound)
			}

			if tt.live != nil {
				b.serveBase(tt.live)
			}

			_, err := b.code.Undo(t.Context(), tt.inv(), tt.snap(), false)
			require.ErrorContains(t, err, tt.wantErr)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}

			if tt.noRequest {
				require.Empty(t, b.github.Requests())
			}
		})
	}
}

func TestUndoNeverLeavesARevertPullRequestThatIsNotADraft(t *testing.T) {
	t.Parallel()

	t.Run("an open one is closed", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]", revertHead: "[" + pullInState(22, false, "open") + "]"})
		b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), reply(refBody("refs/heads/"+undoBranch, testCommitSHA)))
		b.github.Handle(http.MethodPatch, repoPath("pulls/22"), reply(pullInState(22, false, "closed")))
		b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+undoBranch), fakeNoContent())

		_, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "did not open acme/app#22 as a draft")
		require.Equal(t, "closed", bodyOf(t, b.github, http.MethodPatch, repoPath("pulls/22"))["state"])
	})

	t.Run("a closed one after a branch delete that failed", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]", revertHead: "[" + pullInState(22, false, "closed") + "]"})
		b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), reply(refBody("refs/heads/"+undoBranch, testCommitSHA)))
		b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+undoBranch), fakeNoContent())

		_, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
		require.ErrorIs(t, err, action.ErrPermanent)

		for _, r := range sent(b.github) {
			require.NotContains(t, r, "PATCH", "a closed pull request is not closed again")
		}
	})

	t.Run("a failed delete is tried again", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handlePulls(map[string]string{origHead: "[" + mergedPull(21) + "]", revertHead: "[" + pullInState(22, false, "closed") + "]"})
		branchBody := reply(refBody("refs/heads/"+undoBranch, testCommitSHA))
		b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), branchBody, branchBody)
		b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+undoBranch), replyStatus(http.StatusBadGateway, `{"message":"bad gateway"}`), fakeNoContent())

		_, err := b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
		require.Error(t, err)
		require.False(t, isPermanent(err))

		_, err = b.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
		require.ErrorIs(t, err, action.ErrPermanent)
	})
}
