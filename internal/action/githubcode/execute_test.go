package githubcode

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestExecuteOpensADraftPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.expectNoBranch()
	b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(12, true)))

	params := validParams()
	params.Body = "The total ignored the second line. cc @octocat, fixes #9"

	res, err := b.code.Execute(t.Context(), invocation(params))
	require.NoError(t, err)
	require.Equal(t, "Opened draft pull request acme/app#12", res.Summary)
	require.Equal(t, []action.Link{{Title: "acme/app#12", URL: "https://github.com/acme/app/pull/12"}}, res.Links)
	require.Equal(t, &action.PullRequestRef{Number: 12, HeadSHA: testCommitSHA}, res.PullRequest)

	require.Equal(t, []string{
		"GET " + repoPath("git/ref/heads/"+testBranch),
		"GET " + repoPath("git/ref/heads/main"),
		"GET " + repoPath("git/commits/"+testBaseSHA),
		"GET " + repoPath("git/trees/"+treeSHA(testBaseSHA, "")),
		"GET " + repoPath("git/trees/"+treeSHA(testBaseSHA, "internal")),
		"GET " + repoPath("git/trees/"+treeSHA(testBaseSHA, "internal/billing")),
		"GET " + repoPath("git/blobs/"+gitchange.BlobSHA(testGoBefore)),
		"POST " + repoPath("git/trees"),
		"POST " + repoPath("git/commits"),
		"POST " + repoPath("git/refs"),
		"POST " + repoPath("pulls"),
	}, sent(b.github))

	tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, treeSHA(testBaseSHA, ""), tree["base_tree"])
	require.Equal(t, []any{map[string]any{"path": testGoPath, "mode": "100644", "type": "blob", "content": testGoAfter}}, tree["tree"])

	commit := bodyOf(t, b.github, http.MethodPost, repoPath("git/commits"))
	require.Equal(t, testTreeSHA, commit["tree"])
	require.Equal(t, []any{testBaseSHA}, commit["parents"])
	require.Equal(t, "Fix the invoice total\n\nSidekick proposal "+testProposalID+"\n", commit["message"])

	ref := bodyOf(t, b.github, http.MethodPost, repoPath("git/refs"))
	require.Equal(t, map[string]any{"ref": "refs/heads/" + testBranch, "sha": testCommitSHA}, ref)

	pull := bodyOf(t, b.github, http.MethodPost, repoPath("pulls"))
	require.Equal(t, testBranch, pull["head"])
	require.Equal(t, "main", pull["base"])
	require.Equal(t, "Fix the invoice total", pull["title"])
	require.Equal(t, true, pull["draft"])

	body, ok := pull["body"].(string)
	require.True(t, ok)
	require.Contains(t, body, "cc `@octocat`, fixes `#9`")
	require.Contains(t, body, "Opened by Sidekick for proposal `"+testProposalID+"`.")
	require.Contains(t, body, "https://sidekick.example.com")

	for _, key := range []string{"reviewers", "assignees", "maintainer_can_modify", "auto_merge"} {
		require.NotContains(t, pull, key)
	}
}

func TestExecuteKeepsModesAndWritesNewFilesAsRegular(t *testing.T) {
	t.Parallel()

	b := newBed(t, func(l *Limits) { l.AllowDelete = true })
	b.expectNoBranch()
	b.serveBase(repoFiles{
		"scripts/run.sh":     executable("#!/bin/sh\necho 1\n"),
		"scripts/old.sh":     executable("#!/bin/sh\n"),
		"cmd/main.go":        regular("package main\n"),
		"internal/keep/x.go": regular("package keep\n"),
	})
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(3, true)))

	params := validParams()
	params.Files = []gitchange.FileChange{
		{Path: "scripts/run.sh", Content: new("#!/bin/sh\necho 2\n")},
		{Path: "scripts/old.sh", Delete: true},
		{Path: "cmd/new.go", Content: new("package main\n\nvar X = 1\n")},
	}

	_, err := b.code.Execute(t.Context(), invocation(params))
	require.NoError(t, err)

	tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, []any{
		map[string]any{"path": "scripts/run.sh", "mode": "100755", "type": "blob", "content": "#!/bin/sh\necho 2\n"},
		map[string]any{"path": "scripts/old.sh", "mode": "100755", "type": "blob", "sha": nil},
		map[string]any{"path": "cmd/new.go", "mode": "100644", "type": "blob", "content": "package main\n\nvar X = 1\n"},
	}, tree["tree"])
}

func TestExecuteRetryAfterCrash(t *testing.T) {
	t.Parallel()

	const headBranch = "acme:" + "sidekick/" + testProposalID

	tests := []struct {
		name     string
		branch   bool
		pulls    map[string]string
		wantSent []string
		wantPR   int64
		wantWord string
	}{
		{
			name:     "the pull request exists already",
			branch:   true,
			pulls:    map[string]string{headBranch: "[" + pullBody(12, true) + "]"},
			wantSent: []string{"GET /repos/acme/app/git/ref/heads/" + testBranch, "GET /repos/acme/app/pulls"},
			wantPR:   12,
			wantWord: "Found the already opened draft pull request acme/app#12",
		},
		{
			name:   "the branch exists and the pull request was not made",
			branch: true,
			pulls:  map[string]string{},
			wantSent: []string{
				"GET /repos/acme/app/git/ref/heads/" + testBranch, "GET /repos/acme/app/pulls", "POST /repos/acme/app/pulls",
			},
			wantPR:   13,
			wantWord: "Opened draft pull request acme/app#13",
		},
		{
			name:     "a pull request closed or merged since is returned as it is",
			branch:   true,
			pulls:    map[string]string{headBranch: `[{"number":14,"draft":true,"state":"closed","merged_at":"2026-11-03T10:00:00Z","html_url":"https://github.com/acme/app/pull/14","head":{"sha":"` + testCommitSHA + `"}}]`},
			wantSent: []string{"GET /repos/acme/app/git/ref/heads/" + testBranch, "GET /repos/acme/app/pulls"},
			wantPR:   14,
			wantWord: "which has been merged since it was opened",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testBranch), reply(refBody("refs/heads/"+testBranch, testCommitSHA)))
			b.handlePulls(tt.pulls)
			b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(13, true)))

			res, err := b.code.Execute(t.Context(), invocation(validParams()))
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.wantWord)
			require.Equal(t, tt.wantPR, res.PullRequest.Number)

			require.Equal(t, tt.wantSent, sent(b.github))
		})
	}
}

func TestExecuteTwiceWithTheSameInvocationChangesThingsOnce(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})

	// The first attempt finds no branch and writes everything.
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testBranch), notFound, reply(refBody("refs/heads/"+testBranch, testCommitSHA)))
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(21, true)))
	b.handlePulls(map[string]string{"acme:" + testBranch: "[" + pullBody(21, true) + "]"})

	inv := invocation(validParams())

	first, err := b.code.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := b.code.Execute(t.Context(), inv)
	require.NoError(t, err)

	require.Equal(t, first.PullRequest, second.PullRequest)

	writes := 0

	for _, r := range sent(b.github) {
		if strings.HasPrefix(r, "POST ") {
			writes++
		}
	}

	require.Equal(t, 4, writes, "one tree, one commit, one branch and one pull request in all")
}

func TestExecuteRacesWithAnotherAttempt(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.expectNoBranch()
	b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})
	b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, `{"sha":"`+testTreeSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, `{"sha":"`+testCommitSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusUnprocessableEntity, `{"message":"Reference already exists"}`))
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusUnprocessableEntity, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:`+testBranch+`."}]}`))
	b.handlePulls(map[string]string{"acme:" + testBranch: "[" + pullBody(30, true) + "]"})

	res, err := b.code.Execute(t.Context(), invocation(validParams()))
	require.NoError(t, err)
	require.EqualValues(t, 30, res.PullRequest.Number)
}

func TestExecuteClosesAPullRequestThatIsNotADraft(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.expectNoBranch()
	b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(40, false)))
	b.github.Handle(http.MethodPatch, repoPath("pulls/40"), reply(`{"number":40,"state":"closed","draft":false}`))
	b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakeNoContent())

	_, err := b.code.Execute(t.Context(), invocation(validParams()))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "did not open acme/app#40 as a draft")

	require.Equal(t, "closed", bodyOf(t, b.github, http.MethodPatch, repoPath("pulls/40"))["state"])

	sentList := sent(b.github)
	require.Equal(t, "PATCH "+repoPath("pulls/40"), sentList[len(sentList)-2])
	require.Equal(t, "DELETE "+repoPath("git/refs/heads/"+testBranch), sentList[len(sentList)-1])
}

func TestExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		inv           func() action.Invocation
		setup         func(b *bed)
		wantErr       string
		wantPermanent bool
		wantLimit     bool
	}{
		{
			name: "no idempotency key",
			inv: func() action.Invocation {
				inv := invocation(validParams())
				inv.IdempotencyKey = ""

				return inv
			},
			wantErr: "no idempotency key", wantPermanent: true,
		},
		{
			name: "a key that is not a branch",
			inv: func() action.Invocation {
				inv := invocation(validParams())
				inv.IdempotencyKey = "something"

				return inv
			},
			wantErr: "no idempotency key", wantPermanent: true,
		},
		{
			name: "a repository that is not allowed",
			inv: func() action.Invocation {
				p := validParams()
				p.Repo = "acme/other"

				return invocation(p)
			},
			wantErr: "is not one of limits.repos", wantLimit: true,
		},
		{
			name: "a path that is refused",
			inv: func() action.Invocation {
				p := validParams()
				p.Files[0].Path = ".github/workflows/ci.yml"

				return invocation(p)
			},
			wantErr: "refused", wantLimit: true,
		},
		{
			name:    "a file that has the new content already",
			inv:     func() action.Invocation { return invocation(validParams()) },
			setup:   func(b *bed) { b.expectNoBranch(); b.serveBase(repoFiles{testGoPath: regular(testGoAfter)}) },
			wantErr: "already has this content", wantPermanent: true,
		},
		{
			name: "GitHub refuses the tree",
			inv:  func() action.Invocation { return invocation(validParams()) },
			setup: func(b *bed) {
				b.expectNoBranch()
				b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})
				b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusForbidden, `{"message":"Resource not accessible by integration"}`))
			},
			wantErr: "make the tree", wantPermanent: true,
		},
		{
			name: "GitHub is down while the tree is made",
			inv:  func() action.Invocation { return invocation(validParams()) },
			setup: func(b *bed) {
				b.expectNoBranch()
				b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})
				b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusBadGateway, `{"message":"bad gateway"}`))
			},
			wantErr: "make the tree",
		},
		{
			name: "GitHub refuses the pull request",
			inv:  func() action.Invocation { return invocation(validParams()) },
			setup: func(b *bed) {
				b.expectNoBranch()
				b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})
				b.expectPush()
				b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusUnprocessableEntity, `{"message":"Validation Failed","errors":[{"message":"No commits between main and the branch"}]}`))
			},
			wantErr: "open the pull request", wantPermanent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			if tt.setup != nil {
				tt.setup(b)
			}

			_, err := b.code.Execute(t.Context(), tt.inv())
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.Equal(t, tt.wantLimit, isLimit(err))
		})
	}
}
