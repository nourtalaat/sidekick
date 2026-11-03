package githubcode

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// allowedCalls are the kinds of calls to GitHub the action makes. It never
// merges, turns on auto-merge, asks for a review or assigns anyone, so none of
// those is on the list. A new kind of call fails TestOnlyTheAllowedCalls until
// someone adds it here on purpose.
var allowedCalls = []string{
	"GET /repos/acme/app/git/blobs/{sha}",
	"GET /repos/acme/app/git/commits/{sha}",
	"GET /repos/acme/app/git/ref/heads/{branch}",
	"GET /repos/acme/app/git/trees/{sha}",
	"GET /repos/acme/app/pulls",
	"POST /repos/acme/app/git/commits",
	"POST /repos/acme/app/git/refs",
	"POST /repos/acme/app/git/trees",
	"POST /repos/acme/app/pulls",
	"PATCH /repos/acme/app/pulls/{number}",
	"DELETE /repos/acme/app/git/refs/heads/{branch}",
}

// callShapes turn the parts of a path that change from call to call into
// names.
var callShapes = []struct {
	pattern *regexp.Regexp
	with    string
}{
	{regexp.MustCompile(`/git/(blobs|commits|trees)/[0-9a-f]{40}$`), "/git/$1/{sha}"},
	{regexp.MustCompile(`/git/ref/heads/.+$`), "/git/ref/heads/{branch}"},
	{regexp.MustCompile(`/git/refs/heads/.+$`), "/git/refs/heads/{branch}"},
	{regexp.MustCompile(`/pulls/\d+$`), "/pulls/{number}"},
}

// kindOf returns the kind of a call, such as "PATCH /repos/acme/app/pulls/{number}".
func kindOf(call string) string {
	for _, s := range callShapes {
		call = s.pattern.ReplaceAllString(call, s.with)
	}

	return call
}

func TestOnlyTheAllowedCalls(t *testing.T) {
	t.Parallel()

	var calls []string

	// A change that is made, with a read of the file at the base.
	opened := newBed(t, func(l *Limits) { l.AllowDelete = true })
	opened.expectNoBranch()
	opened.serveBase(repoFiles{testGoPath: regular(testGoBefore), oldGoPath: regular(oldGoText)})
	opened.expectPush()
	opened.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(1, true)))

	params := validParams()
	params.Files = append(params.Files, gitchange.FileChange{Path: oldGoPath, Delete: true})

	_, err := opened.code.Execute(t.Context(), invocation(params))
	require.NoError(t, err)

	calls = append(calls, sent(opened.github)...)

	// A repeat that finds the pull request.
	repeat := newBed(t, nil)
	repeat.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testBranch), reply(refBody("refs/heads/"+testBranch, testCommitSHA)))
	repeat.handlePulls(map[string]string{origHead: "[" + pullBody(1, true) + "]"})

	_, err = repeat.code.Execute(t.Context(), invocation(validParams()))
	require.NoError(t, err)

	calls = append(calls, sent(repeat.github)...)

	// A pull request that GitHub did not make a draft.
	notDraft := newBed(t, nil)
	notDraft.expectNoBranch()
	notDraft.serveBase(repoFiles{testGoPath: regular(testGoBefore)})
	notDraft.expectPush()
	notDraft.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(2, false)))
	notDraft.github.Handle(http.MethodPatch, repoPath("pulls/2"), reply(closedPull(2)))
	notDraft.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakeNoContent())

	_, err = notDraft.code.Execute(t.Context(), invocation(validParams()))
	require.ErrorIs(t, err, action.ErrPermanent)

	calls = append(calls, sent(notDraft.github)...)

	// Undo of an open pull request.
	closing := newBed(t, nil)
	closing.handlePulls(map[string]string{origHead: "[" + pullBody(3, true) + "]"})
	closing.github.Handle(http.MethodPatch, repoPath("pulls/3"), reply(closedPull(3)))
	closing.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakeNoContent())

	_, err = closing.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)

	calls = append(calls, sent(closing.github)...)

	// Undo of a merged pull request.
	reverting := newBed(t, nil)
	reverting.handlePulls(map[string]string{origHead: "[" + mergedPull(4) + "]"})
	reverting.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+undoBranch), notFound)
	reverting.serveBase(mergedState())
	reverting.serveCommit(testEarlierSHA, repoFiles{oldGoPath: executable(oldGoText)})
	reverting.expectPush()
	reverting.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(5, true)))

	_, err = reverting.code.Undo(t.Context(), undoInvocation(), undoSnapshot(testEarlierSHA), false)
	require.NoError(t, err)

	calls = append(calls, sent(reverting.github)...)

	// The read of a blob for the approver, and the status job.
	reading := newBed(t, nil)
	reading.github.Handle(http.MethodGet, repoPath("git/blobs/"+gitchange.BlobSHA("x\n")), reply("x\n"))
	reading.handlePulls(map[string]string{origHead: "[" + pullBody(6, true) + "]"})

	_, err = reading.code.ReadBlob(t.Context(), rawParams(t, validParams()), gitchange.BlobSHA("x\n"))
	require.NoError(t, err)

	_, _, err = reading.code.PullRequestState(t.Context(), rawParams(t, validParams()), testBranch)
	require.NoError(t, err)

	calls = append(calls, sent(reading.github)...)

	seen := map[string]bool{}

	for _, call := range calls {
		kind := kindOf(call)
		seen[kind] = true

		require.Contains(t, allowedCalls, kind, "a new kind of call to GitHub: add it to allowedCalls only on purpose, and never a merge, auto-merge, review request or assignment")
	}

	for _, kind := range allowedCalls {
		require.True(t, seen[kind], "no flow makes %s, so the list is out of date", kind)
	}
}

func TestNoCodeThatMergesOrRequestsReviews(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		".Merge(", ".MergeAsync(", "RequestReviewers", "AddAssignees", "EnableAutoMerge", "AutoMerge", "graphql", "CreateReview", "MarkReadyForReview",
	}

	for _, dir := range []string{".", "../githubgit"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		require.NotEmpty(t, files)

		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}

			src, err := os.ReadFile(f) //nolint:gosec // The path is a source file of this repository.
			require.NoError(t, err)

			for _, word := range forbidden {
				require.NotContains(t, string(src), word, "%s must not merge, turn on auto-merge, ask for a review or assign anyone", f)
			}
		}
	}
}
