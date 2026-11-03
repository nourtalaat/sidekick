package gitlabcode

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// freshCalls are the calls of an Execute that makes everything, for the test
// file at the base.
var freshCalls = []string{
	"GET merge_requests",
	"GET repository/branches/" + testBranch,
	"GET repository/branches/main",
	"GET repository/tree",
	"GET repository/tree",
	"GET repository/tree",
	"GET repository/files/" + testPath,
	"POST repository/commits",
	"POST merge_requests",
}

// executeFresh makes a bed on which Execute finds nothing from an earlier
// attempt and writes everything.
func executeFresh(t *testing.T, r repo) *bed {
	t.Helper()

	b := newBed(t, nil)
	b.serveRepo(r)
	b.handleMergeRequests(nil)
	b.expectNoBranch()
	b.expectCommit()
	b.expectOpen(7, true)

	return b
}

func TestExecuteOpensADraftMergeRequest(t *testing.T) {
	t.Parallel()

	b := executeFresh(t, repo{testPath: regular(testBefore)})

	res, err := b.code.Execute(t.Context(), invocation(codeParams()))
	require.NoError(t, err)

	require.Equal(t, "Opened draft merge request "+testProject+"!7", res.Summary)
	require.Equal(t, []action.Link{{Title: testProject + "!7", URL: testWebURL + "7"}}, res.Links)
	require.Equal(t, &action.PullRequestRef{Number: 7, HeadSHA: testCommitSHA}, res.PullRequest)
	require.Equal(t, freshCalls, sent(b.gitlab))

	commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))
	require.Equal(t, testBranch, commit["branch"])
	require.Equal(t, testBaseSHA, commit["start_sha"], "the commit is built on the commit the files were read at")
	require.Equal(t, "Fix the total of an invoice\n\nSidekick proposal "+testProposalID+"\n", commit["commit_message"])
	require.Equal(t, []any{map[string]any{"action": "update", "file_path": testPath, "content": testAfter}}, commit["actions"])

	mr := bodyOf(t, b.gitlab, http.MethodPost, apiPath("merge_requests"))
	require.ElementsMatch(t, []string{"description", "source_branch", "target_branch", "title"}, keysOf(mr), "no assignee, reviewer, auto-merge or squash")
	require.Equal(t, testBranch, mr["source_branch"])
	require.Equal(t, "main", mr["target_branch"])
	require.Equal(t, "Draft: Fix the total of an invoice", mr["title"])
	require.Contains(t, mr["description"], "Opened by Sidekick for proposal `"+testProposalID+"`")
	require.Contains(t, mr["description"], "https://sidekick.example.com/investigations/"+testInvestID)
}

func TestExecuteCreatesUpdatesAndDeletesFiles(t *testing.T) {
	t.Parallel()

	const (
		newPath = "src/billing/new.go"
		oldPath = "src/billing/old.go"
		empty   = "src/billing/empty.go"
	)

	b := newBed(t, nil)
	b.serveRepo(repo{testPath: regular(testBefore), oldPath: regular("package billing\n")})
	b.expectMissing(newPath, empty)
	b.handleMergeRequests(nil)
	b.expectNoBranch()
	b.expectCommit()
	b.expectOpen(8, true)

	p := validParams()
	p.Files = []gitchange.FileChange{
		{Path: testPath, Content: text(testAfter)},
		{Path: newPath, Content: text("package billing\n")},
		{Path: empty, Content: text("")},
	}
	p.Files = append(p.Files, gitchange.FileChange{Path: oldPath, Delete: true})

	b.limits(func(l *Limits) { l.MaxFiles = 4 })

	_, err := b.code.Execute(t.Context(), invocation(p))
	require.NoError(t, err)

	commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))
	require.Equal(t, []any{
		map[string]any{"action": "update", "file_path": testPath, "content": testAfter},
		map[string]any{"action": "create", "file_path": newPath, "content": "package billing\n"},
		map[string]any{"action": "create", "file_path": empty, "content": ""},
		map[string]any{"action": "delete", "file_path": oldPath},
	}, commit["actions"], "a delete has no content, and empty content is sent")
}

func TestExecuteKeepsQuickActionsAndReferencesOutOfGitLab(t *testing.T) {
	t.Parallel()

	b := executeFresh(t, repo{testPath: regular(testBefore)})

	p := codeParams()
	p.Title = "/merge now, closes #12"
	p.Body = "Why.\n/merge\n  /approve\r\n/assign @alex\nCloses group/project#3"

	_, err := b.code.Execute(t.Context(), invocation(p))
	require.NoError(t, err)

	for _, call := range []struct{ method, path string }{
		{http.MethodPost, apiPath("merge_requests")}, {http.MethodPost, apiPath("repository/commits")},
	} {
		for key, value := range bodyOf(t, b.gitlab, call.method, call.path) {
			s, ok := value.(string)
			if !ok {
				continue
			}

			for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r", ""), "\n") {
				require.False(t, strings.HasPrefix(strings.TrimLeft(line, " \t"), "/"), "%s: a line starts with a slash: %q", key, line)
			}
		}
	}

	mr := bodyOf(t, b.gitlab, http.MethodPost, apiPath("merge_requests"))
	require.Contains(t, mr["description"], "`/merge`")
	require.Contains(t, mr["description"], "`group/project#3`")
	require.True(t, strings.HasPrefix(mr["title"].(string), "Draft: "))
}

func TestExecuteAfterAnEarlierAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(b *bed)
		wantCalls []string
		wantIID   int64
		wantWord  string
	}{
		{
			name: "the merge request is open already",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "opened", true))})
			},
			wantCalls: []string{"GET merge_requests"},
			wantIID:   12,
			wantWord:  "Found the already opened draft merge request " + testProject + "!12",
		},
		{
			name: "the branch exists and the merge request was not made",
			setup: func(b *bed) {
				b.handleMergeRequests(nil)
				b.expectBranch()
				b.expectOpen(13, true)
			},
			wantCalls: []string{"GET merge_requests", "GET repository/branches/" + testBranch, "POST merge_requests"},
			wantIID:   13,
			wantWord:  "Opened draft merge request " + testProject + "!13",
		},
		{
			name: "the merge request was merged since, and its branch is gone",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(14, "merged", false))})
			},
			wantCalls: []string{"GET merge_requests"},
			wantIID:   14,
			wantWord:  "which has been merged since it was opened",
		},
		{
			name: "the merge request was closed since",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(15, "closed", true))})
			},
			wantCalls: []string{"GET merge_requests"},
			wantIID:   15,
			wantWord:  "which has been closed since it was opened",
		},
		{
			name: "another attempt opened it while this one did",
			setup: func(b *bed) {
				var lists atomic.Int32

				b.gitlab.HandleFunc(http.MethodGet, apiPath("merge_requests"), func(w http.ResponseWriter, _ *http.Request) {
					if lists.Add(1) == 1 {
						writeJSON(w, http.StatusOK, "[]")

						return
					}

					writeJSON(w, http.StatusOK, listOf(mergeRequestBody(16, "opened", true)))
				})
				b.expectBranch()
				b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "Another open merge request already exists for this source branch"))
			},
			wantCalls: []string{"GET merge_requests", "GET repository/branches/" + testBranch, "POST merge_requests", "GET merge_requests"},
			wantIID:   16,
			wantWord:  "Found the already opened draft merge request " + testProject + "!16",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			tt.setup(b)

			res, err := b.code.Execute(t.Context(), invocation(codeParams()))
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.wantWord)
			require.Equal(t, tt.wantIID, res.PullRequest.Number)
			require.Equal(t, tt.wantCalls, sent(b.gitlab))
		})
	}
}

func TestExecuteChecksTheDraftOnEveryPath(t *testing.T) {
	t.Parallel()

	closeCall := "PUT merge_requests/12"
	deleteCall := "DELETE repository/branches/" + testBranch

	tests := []struct {
		name       string
		setup      func(b *bed)
		wantWrites []string
	}{
		{
			name: "an open merge request that is not a draft is closed and its branch deleted",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "opened", false))})
				b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(mergeRequestBody(12, "closed", false)))
				b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), noContent)
			},
			wantWrites: []string{closeCall, deleteCall},
		},
		{
			name: "a closed merge request that is not a draft loses its branch",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "closed", false))})
				b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), noContent)
			},
			wantWrites: []string{deleteCall},
		},
		{
			name: "a merge request that GitLab is merging is left alone",
			setup: func(b *bed) {
				b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "locked", false))})
			},
		},
		{
			name: "a merge request that GitLab made without the draft state",
			setup: func(b *bed) {
				b.serveRepo(repo{testPath: regular(testBefore)})
				b.handleMergeRequests(nil)
				b.expectNoBranch()
				b.expectCommit()
				b.expectOpen(12, false)
				b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(mergeRequestBody(12, "closed", false)))
				b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), noContent)
			},
			wantWrites: []string{"POST repository/commits", "POST merge_requests", closeCall, deleteCall},
		},
		{
			name: "a merge request found after a conflict that is not a draft",
			setup: func(b *bed) {
				var lists atomic.Int32

				b.gitlab.HandleFunc(http.MethodGet, apiPath("merge_requests"), func(w http.ResponseWriter, _ *http.Request) {
					if lists.Add(1) == 1 {
						writeJSON(w, http.StatusOK, "[]")

						return
					}

					writeJSON(w, http.StatusOK, listOf(mergeRequestBody(12, "opened", false)))
				})
				b.expectBranch()
				b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "Another open merge request already exists"))
				b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(mergeRequestBody(12, "closed", false)))
				b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), noContent)
			},
			wantWrites: []string{"POST merge_requests", closeCall, deleteCall},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			tt.setup(b)

			_, err := b.code.Execute(t.Context(), invocation(codeParams()))
			require.ErrorIs(t, err, action.ErrPermanent, "Sidekick does not try again")
			require.Equal(t, tt.wantWrites, writes(b.gitlab))
		})
	}
}

func TestExecuteTwiceWithTheSameInvocationChangesThingsOnce(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.serveRepo(repo{testPath: regular(testBefore)})
	b.expectNoBranch()
	b.expectCommit()
	b.expectOpen(21, true)

	var lists atomic.Int32

	b.gitlab.HandleFunc(http.MethodGet, apiPath("merge_requests"), func(w http.ResponseWriter, _ *http.Request) {
		if lists.Add(1) == 1 {
			writeJSON(w, http.StatusOK, "[]")

			return
		}

		writeJSON(w, http.StatusOK, listOf(mergeRequestBodyFrom(21, "opened", true, testBranch)))
	})

	inv := invocation(codeParams())

	first, err := b.code.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := b.code.Execute(t.Context(), inv)
	require.NoError(t, err)

	require.Equal(t, first.PullRequest.Number, second.PullRequest.Number)
	require.Equal(t, []string{"POST repository/commits", "POST merge_requests"}, writes(b.gitlab), "one commit and one merge request in all")
}

func TestExecuteWhenAnotherAttemptMadeTheBranchFirst(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.serveRepo(repo{testPath: regular(testBefore)})
	b.handleMergeRequests(nil)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), notFound, reply(branchBody(testCommitSHA)))
	b.gitlab.Handle(http.MethodPost, apiPath("repository/commits"), failed(http.StatusBadRequest, "A branch called "+testBranch+" already exists"))
	b.expectOpen(22, true)

	res, err := b.code.Execute(t.Context(), invocation(codeParams()))
	require.NoError(t, err)
	require.Equal(t, int64(22), res.PullRequest.Number)
}

func TestExecuteRefusesWhatTheLimitsDoNotAllowBeforeAnyCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params func() Params
		limits func(l *Limits)
		want   string
	}{
		{name: "a project that is not listed", params: func() Params { p := codeParams(); p.Project = "acme/other"; return p }, want: "is not one of limits.projects"},
		{name: "a refused path", params: func() Params { p := validParams(); p.Files = change(".github/workflows/ci.yml", "x\n"); return p }, want: "CI and build automation"},
		{name: "CODEOWNERS", params: func() Params { p := validParams(); p.Files = change("CODEOWNERS", "x\n"); return p }, want: "ownership and review rules"},
		{name: "a .env file", params: func() Params { p := validParams(); p.Files = change("src/.env", "X=1\n"); return p }, want: "secrets and keys"},
		{
			name: "a delete that is not allowed", params: func() Params {
				p := validParams()
				p.Files = []gitchange.FileChange{{Path: testPath, Delete: true}}
				return p
			},
			limits: func(l *Limits) { l.AllowDelete = false }, want: "limits.allowDelete is false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)

			_, err := b.code.Execute(t.Context(), invocation(tt.params()))
			require.ErrorContains(t, err, tt.want)
			require.ErrorIs(t, err, action.ErrLimit)
			require.Empty(t, b.gitlab.Requests())
		})
	}
}

func TestExecuteNeedsTheBranchOfTheProposal(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	inv := invocation(codeParams())
	inv.IdempotencyKey = "main"

	_, err := b.code.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorIs(t, err, gitlabgit.ErrNoBranch)
	require.Empty(t, b.gitlab.Requests())
}

func TestExecuteComparesWithTheSnapshot(t *testing.T) {
	t.Parallel()

	snapshotOf := func(t *testing.T, base string) action.Snapshot {
		t.Helper()

		b := newBed(t, nil)
		b.serveRepo(repo{testPath: regular(base)})

		snap, err := b.code.Snapshot(t.Context(), rawParams(t, codeParams()))
		require.NoError(t, err)

		return snap
	}

	t.Run("a file that is as it was is written", func(t *testing.T) {
		t.Parallel()

		snap := snapshotOf(t, testBefore)
		b := executeFresh(t, repo{testPath: regular(testBefore)})

		inv := invocation(codeParams())
		inv.Snapshot = &snap

		_, err := b.code.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Equal(t, []string{"POST repository/commits", "POST merge_requests"}, writes(b.gitlab))
	})

	t.Run("a file that someone changed since is a conflict", func(t *testing.T) {
		t.Parallel()

		snap := snapshotOf(t, testBefore)
		b := executeFresh(t, repo{testPath: regular(testBefore + "// edited\n")})

		inv := invocation(codeParams())
		inv.Snapshot = &snap

		_, err := b.code.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrConflict)
		require.ErrorContains(t, err, testPath)
		require.Empty(t, writes(b.gitlab))
	})

	t.Run("a file that is already at the new content needs no merge request", func(t *testing.T) {
		t.Parallel()

		snap := snapshotOf(t, testBefore)
		b := executeFresh(t, repo{testPath: regular(testAfter)})

		inv := invocation(codeParams())
		inv.Snapshot = &snap

		res, err := b.code.Execute(t.Context(), inv)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "on the base branch already")
		require.Nil(t, res.PullRequest)
		require.Empty(t, writes(b.gitlab))
	})

	t.Run("a base branch that now needs more changed lines than the limit", func(t *testing.T) {
		t.Parallel()

		snap := snapshotOf(t, testBefore)
		b := executeFresh(t, repo{testPath: regular(testBefore)})
		b.limits(func(l *Limits) { l.MaxChangedLines = 1 })

		inv := invocation(codeParams())
		inv.Snapshot = &snap

		_, err := b.code.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, writes(b.gitlab))
	})
}

func TestExecuteNeverPanicsOnAPartialSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		snap action.Snapshot
	}{
		{name: "an empty snapshot", snap: action.Snapshot{}},
		{name: "null sides", snap: action.Snapshot{Before: []byte("null"), After: []byte("null")}},
		{name: "no entry for the path", snap: action.Snapshot{Before: []byte(`{}`), After: []byte(`{"src/other.go":{"blob":"x","status":"changed"}}`)}},
		{name: "an entry for another content", snap: action.Snapshot{Before: []byte(`{}`), After: []byte(`{"` + testPath + `":{"status":"changed"}}`)}},
		{name: "sides that are not objects", snap: action.Snapshot{Before: []byte(`[1]`), After: []byte(`"x"`)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := executeFresh(t, repo{testPath: regular(testBefore)})

			inv := invocation(codeParams())
			inv.Snapshot = &tt.snap

			require.NotPanics(t, func() {
				_, err := b.code.Execute(t.Context(), inv)
				require.ErrorIs(t, err, action.ErrPermanent)
			})
			require.Empty(t, writes(b.gitlab))
		})
	}
}

// keysOf returns the keys of a decoded JSON object.
func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}
