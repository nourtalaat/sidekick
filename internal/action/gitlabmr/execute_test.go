package gitlabmr

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

func TestExecuteOpensMergeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limits    func(l *Limits)
		title     string
		wantDraft bool
		wantTitle string
		wantWord  string
	}{
		{name: "a draft by default", title: "Raise the checkout latency threshold", wantDraft: true, wantTitle: "Draft: Raise the checkout latency threshold", wantWord: "draft merge request"},
		{
			name: "a merge request ready for review", limits: func(l *Limits) { l.Draft = new(false) },
			title: "Raise the checkout latency threshold", wantTitle: "Raise the checkout latency threshold", wantWord: "merge request acme/platform/monitors!12",
		},
		{
			name: "a draft prefix of the model is removed when not a draft", limits: func(l *Limits) { l.Draft = new(false) },
			title: "[Draft] Raise the threshold", wantTitle: "Raise the threshold", wantWord: "merge request acme/platform/monitors!12",
		},
		{name: "a draft prefix of the model is not doubled", title: "Draft: Raise the threshold", wantDraft: true, wantTitle: "Draft: Raise the threshold", wantWord: "draft merge request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			b.handleMergeRequests(nil)
			b.expectNoBranch()
			b.expectBase()
			b.expectFile(testRulesPath, testRulesBefore)
			b.validatorPasses(1)
			b.expectCommit()
			b.expectOpen(12, tt.wantDraft)
			b.gitlab.ExpectAllCalled()
			b.validator.ExpectAllCalled()

			params := rulesParams()
			params.Title = tt.title
			params.Body = "The alert fires on short spikes. cc @alex, fixes group/project#9"

			res, err := b.mr.Execute(t.Context(), invocation(params))
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.wantWord)
			require.Equal(t, []action.Link{{Title: "acme/platform/monitors!12", URL: testWebURL + "12"}}, res.Links)

			require.Equal(t, []string{
				"GET merge_requests",
				"GET repository/branches/" + testBranch,
				"GET repository/branches/main",
				"GET repository/files/" + testRulesPath,
				"POST repository/commits",
				"POST merge_requests",
			}, sent(b.gitlab))

			require.Equal(t, validateclient.Request{
				Format: validateclient.FormatPrometheusRules,
				Files:  []validateclient.File{{Path: testRulesPath, Content: testRulesAfter}},
			}, validatorRequest(t, b.validator, 0))

			list := queryOf(t, b.gitlab, http.MethodGet, apiPath("merge_requests"))
			require.Equal(t, testBranch, list.Get("source_branch"))
			require.Equal(t, "all", list.Get("state"))

			commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))
			require.Equal(t, []string{"actions", "branch", "commit_message", "start_sha"}, keysOf(commit), "the commit has only what the action sets")
			require.Equal(t, testBranch, commit["branch"])
			require.Equal(t, testBaseSHA, commit["start_sha"], "the branch is made from the commit the files were read at")
			require.Equal(t, commitTitle(tt.title)+"\n\nSidekick proposal "+testProposalID+"\n", commit["commit_message"])

			actions, err := json.Marshal(commit["actions"])
			require.NoError(t, err)
			require.JSONEq(t, `[{"action":"update","file_path":"`+testRulesPath+`","content":`+jsonString(t, testRulesAfter)+`}]`, string(actions))

			mr := bodyOf(t, b.gitlab, http.MethodPost, apiPath("merge_requests"))
			require.Equal(t, []string{"description", "source_branch", "target_branch", "title"}, keysOf(mr),
				"the merge request sets no assignee, reviewer, label, milestone, squash or auto-merge")
			require.Equal(t, testBranch, mr["source_branch"])
			require.Equal(t, "main", mr["target_branch"])
			require.Equal(t, tt.wantTitle, mr["title"])

			body, ok := mr["description"].(string)
			require.True(t, ok)
			require.Contains(t, body, "cc `@alex`, fixes `group/project#9`")
			require.Contains(t, body, "\n\n---\nOpened by Sidekick for proposal `"+testProposalID+"`.")
			require.Contains(t, body, "[The investigation](https://sidekick.example.com/investigations/"+testInvestID+")")

			for _, r := range b.gitlab.Requests() {
				require.Equal(t, testTokenValue, r.Header.Get("PRIVATE-TOKEN"))
			}
		})
	}
}

func TestExecuteCreatesAndDeletesFiles(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile("prometheus/rules/old.yaml", "groups: []\n")
	b.expectNoFile("prometheus/rules/new.yaml")
	b.validatorPasses(1)
	b.expectCommit()
	b.expectOpen(3, true)
	b.gitlab.ExpectAllCalled()

	params := validParams()
	params.Files = []gitchange.FileChange{
		{Path: "prometheus/rules/old.yaml", Delete: true},
		{Path: "prometheus/rules/new.yaml", Content: text("groups:\n- name: a\n  rules: []\n")},
	}

	_, err := b.mr.Execute(t.Context(), invocation(params))
	require.NoError(t, err)

	commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))

	actions, err := json.Marshal(commit["actions"])
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"action":"delete","file_path":"prometheus/rules/old.yaml"},
		{"action":"create","file_path":"prometheus/rules/new.yaml","content":"groups:\n- name: a\n  rules: []\n"}
	]`, string(actions))

	require.Equal(t, validateclient.Request{
		Format: validateclient.FormatPrometheusRules,
		Files:  []validateclient.File{{Path: "prometheus/rules/new.yaml", Content: "groups:\n- name: a\n  rules: []\n"}},
	}, validatorRequest(t, b.validator, 0), "a deleted file is not sent to the validator")
}

func TestExecuteEmptyContentIsSent(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile(testRulesPath, testRulesBefore)
	b.validatorPasses(1)
	b.expectCommit()
	b.expectOpen(4, true)

	params := validParams()
	params.Files = []gitchange.FileChange{{Path: testRulesPath, Content: text("")}}

	_, err := b.mr.Execute(t.Context(), invocation(params))
	require.NoError(t, err)

	require.Contains(t, string(b.gitlab.Requests()[4].Body), `"content":""`, "an update to an empty file still has its content key")
}

func TestExecuteQuickActionsNeverReachGitLab(t *testing.T) {
	t.Parallel()

	commands := []string{"/merge", "/assign @alex", "/label ~bug", "/approve", "/close", "/draft", "/ready", "/rebase", "/submit_review approve", "/MERGE", "  /merge", "/shrug"}

	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.handleMergeRequests(nil)
			b.expectNoBranch()
			b.expectBase()
			b.expectFile(testRulesPath, testRulesBefore)
			b.validatorPasses(1)
			b.expectCommit()
			b.expectOpen(5, true)

			params := rulesParams()
			params.Title = command
			params.Body = "Raise the threshold.\n" + command + "\nThanks.\r\n" + command + "\n\r" + command

			_, err := b.mr.Execute(t.Context(), invocation(params))
			require.NoError(t, err)

			mr := bodyOf(t, b.gitlab, http.MethodPost, apiPath("merge_requests"))

			for _, key := range []string{"title", "description"} {
				text, ok := mr[key].(string)
				require.True(t, ok)
				noSlashLines(t, text)
			}

			commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))
			message, ok := commit["commit_message"].(string)
			require.True(t, ok)
			noSlashLines(t, message)
		})
	}
}

func TestExecuteRetryAfterCrash(t *testing.T) {
	t.Parallel()

	opened := "[" + mergeRequestBody(12, "opened", true) + "]"

	tests := []struct {
		name     string
		lists    string
		branch   bool
		writes   func(b *bed)
		want     []string
		wantText string
	}{
		{
			name:     "the merge request exists",
			lists:    opened,
			wantText: "Found the already opened draft merge request acme/platform/monitors!12",
			want:     []string{"GET merge_requests"},
		},
		{
			name:     "the merge request was closed meanwhile",
			lists:    "[" + mergeRequestBody(12, "closed", false) + "]",
			wantText: "Found merge request acme/platform/monitors!12, which has been closed since it was opened",
			want:     []string{"GET merge_requests"},
		},
		{
			name:     "the merge request was merged meanwhile, and its branch is gone",
			lists:    "[" + mergeRequestBody(12, "merged", false) + "]",
			wantText: "Found merge request acme/platform/monitors!12, which has been merged since it was opened",
			want:     []string{"GET merge_requests"},
		},
		{
			name:     "the merge request is being merged",
			lists:    "[" + mergeRequestBody(12, "locked", false) + "]",
			wantText: "Found the already opened merge request acme/platform/monitors!12",
			want:     []string{"GET merge_requests"},
		},
		{
			name:     "an open one is preferred over a closed and a merged one",
			lists:    "[" + mergeRequestBody(14, "closed", false) + "," + mergeRequestBody(13, "merged", false) + "," + mergeRequestBody(12, "opened", true) + "]",
			wantText: "Found the already opened draft merge request acme/platform/monitors!12",
			want:     []string{"GET merge_requests"},
		},
		{
			name:     "a merged one is preferred over the newest closed one",
			lists:    "[" + mergeRequestBody(14, "closed", false) + "," + mergeRequestBody(13, "merged", false) + "]",
			wantText: "Found merge request acme/platform/monitors!13, which has been merged since it was opened",
			want:     []string{"GET merge_requests"},
		},
		{
			name:   "the branch exists and has no merge request",
			lists:  "[]",
			branch: true,
			writes: func(b *bed) { b.expectOpen(13, true) },
			want: []string{
				"GET merge_requests",
				"GET repository/branches/" + testBranch,
				"POST merge_requests",
			},
			wantText: "Opened draft merge request acme/platform/monitors!13",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.handleMergeRequests(map[string]string{testBranch: tt.lists})

			if tt.branch {
				b.expectBranch()
			}

			if tt.writes != nil {
				tt.writes(b)
			}

			b.gitlab.ExpectAllCalled()

			res, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
			require.NoError(t, err)
			require.Equal(t, tt.wantText, res.Summary)
			require.Equal(t, tt.want, sent(b.gitlab))
			require.Empty(t, b.validator.Requests(), "a repeat does not validate again")
		})
	}
}

func TestExecuteTwiceChangesThingsOnce(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		opened bool
	)

	b := newBed(t, nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile(testRulesPath, testRulesBefore)
	b.validatorPasses(1)
	b.expectCommit()
	b.gitlab.HandleFunc(http.MethodGet, apiPath("merge_requests"), func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if opened {
			_, _ = w.Write([]byte("[" + mergeRequestBody(12, "opened", true) + "]"))

			return
		}

		_, _ = w.Write([]byte("[]"))
	})
	b.gitlab.HandleFunc(http.MethodPost, apiPath("merge_requests"), func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		opened = true

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(mergeRequestBody(12, "opened", true)))
	})

	inv := invocation(rulesParams())

	first, err := b.mr.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := b.mr.Execute(t.Context(), inv)
	require.NoError(t, err)

	require.Equal(t, first.Links, second.Links)
	require.Contains(t, second.Summary, "Found the already opened")

	var commits, opens int

	for _, call := range sent(b.gitlab) {
		switch call {
		case "POST repository/commits":
			commits++
		case "POST merge_requests":
			opens++
		}
	}

	require.Equal(t, 1, commits, "one commit")
	require.Equal(t, 1, opens, "one merge request")
}

func TestExecuteRacingAttempts(t *testing.T) {
	t.Parallel()

	t.Run("another attempt made the branch first", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), notFound, reply(branchBody(testCommitSHA)))
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validatorPasses(1)
		b.gitlab.Handle(http.MethodPost, apiPath("repository/commits"), failed(http.StatusBadRequest, "A branch called '"+testBranch+"' already exists. Switch to that branch in order to make changes"))
		b.expectOpen(12, true)
		b.gitlab.ExpectAllCalled()

		res, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.NoError(t, err)
		require.Contains(t, res.Summary, "Opened draft merge request")
	})

	t.Run("the commit fails and there is no branch", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), notFound, notFound)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validatorPasses(1)
		b.gitlab.Handle(http.MethodPost, apiPath("repository/commits"), failed(http.StatusBadRequest, "Commit message does not follow the pattern"))

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "Commit message does not follow the pattern")

		for _, call := range sent(b.gitlab) {
			require.NotEqual(t, "POST merge_requests", call)
		}
	})

	t.Run("another attempt opened the merge request first", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("[]"), reply("["+mergeRequestBody(12, "opened", true)+"]"))
		b.expectBranch()
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "Another open merge request already exists for this source branch: !12"))
		b.gitlab.ExpectAllCalled()

		res, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.NoError(t, err)
		require.Equal(t, []action.Link{{Title: "acme/platform/monitors!12", URL: testWebURL + "12"}}, res.Links)
	})

	t.Run("a conflict that finds nothing", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("[]"), reply("[]"))
		b.expectBranch()
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "You can't use same project/branch for source and target"))

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.ErrorIs(t, err, action.ErrPermanent)
	})
}

func TestExecuteFailures(t *testing.T) {
	t.Parallel()

	t.Run("a validator that finds errors makes no branch", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(nil)
		b.expectNoBranch()
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validator.Handle(http.MethodPost, "/validate", reply(`{"ok":false,"errors":[{"path":"`+testRulesPath+`","line":6,"message":"bad duration"}]}`))

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, testRulesPath+":6: bad duration")

		for _, call := range sent(b.gitlab) {
			require.True(t, strings.HasPrefix(call, "GET "), "nothing is written: %s", call)
		}
	})

	t.Run("a validator that cannot be reached is tried again", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(nil)
		b.expectNoBranch()
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validator.Handle(http.MethodPost, "/validate", replyStatus(http.StatusServiceUnavailable, `{"error":"busy"}`))

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.ErrorIs(t, err, validateclient.ErrBusy)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("a GitLab that fails the merge request is tried again", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(nil)
		b.expectBranch()
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusBadGateway, "bad gateway"))

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.Error(t, err)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("a token without the right is not tried again", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), failed(http.StatusUnauthorized, "401 Unauthorized"))

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.ErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("no idempotency key", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		inv := invocation(rulesParams())
		inv.IdempotencyKey = "not-a-branch"

		_, err := b.mr.Execute(t.Context(), inv)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorIs(t, err, errNoBranch)
		require.Empty(t, b.gitlab.Requests())
	})

	t.Run("a merge request that GitLab does not describe", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(nil)
		b.expectBranch()
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, `{}`))

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.ErrorIs(t, err, errNoIID)
	})
}

func TestExecuteRefusesWhatTheLimitsDoNotAllow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(p *Params)
		limits func(l *Limits)
	}{
		{name: "another project", change: func(p *Params) { p.Project = "acme/platform/other" }},
		{name: "a path outside the globs", change: func(p *Params) { p.Files[0].Path = "src/main.go" }},
		{name: "another base branch", change: func(p *Params) { p.BaseBranch = "develop" }},
		{
			name: "more files than the limit", limits: func(l *Limits) { l.MaxFiles = 1 },
			change: func(p *Params) {
				p.Files = append(p.Files, gitchange.FileChange{Path: "prometheus/rules/b.yaml", Content: text("b")})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)

			p := rulesParams()
			tt.change(&p)

			_, err := b.mr.Execute(t.Context(), invocation(p))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Empty(t, b.gitlab.Requests())
		})
	}

	t.Run("more changed lines than the limit, when it runs", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, func(l *Limits) { l.MaxChangedLines = 1 })
		b.handleMergeRequests(nil)
		b.expectNoBranch()
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)

		_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
		require.ErrorIs(t, err, action.ErrLimit)

		for _, call := range sent(b.gitlab) {
			require.True(t, strings.HasPrefix(call, "GET "), "nothing is written: %s", call)
		}
	})
}

func TestExecuteComparesWithTheSnapshot(t *testing.T) {
	t.Parallel()

	afterBlob := blobSHA(testRulesAfter)

	snapshot := func(before string) *action.Snapshot {
		return &action.Snapshot{
			Target:  "gitlab:" + testProject,
			Version: testBaseSHA,
			Before:  json.RawMessage(`{"` + testRulesPath + `": {"blob": "` + before + `"}}`),
			After:   json.RawMessage(`{"` + testRulesPath + `": {"blob": "` + afterBlob + `", "status": "changed", "linesAdded": 1, "linesRemoved": 1}}`),
		}
	}

	tests := []struct {
		name       string
		live       string
		liveSHA    string
		snap       *action.Snapshot
		wantMR     bool
		wantErrIs  error
		wantErr    string
		wantResult string
	}{
		{name: "the file is as it was when the model proposed", live: testRulesBefore, liveSHA: testRulesSHA, snap: snapshot(testRulesSHA), wantMR: true},
		{name: "no snapshot to compare with", live: testRulesBefore, liveSHA: testOldSHA, wantMR: true},
		{
			name: "someone changed the file", live: "groups: []\n", liveSHA: testOldSHA, snap: snapshot(testRulesSHA),
			wantErrIs: action.ErrConflict, wantErr: `"` + testRulesPath + `" changed on the base branch after the proposal was made`,
		},
		{
			name: "the file is at the new content already", live: testRulesAfter, liveSHA: afterBlob, snap: snapshot(testRulesSHA),
			wantResult: "The change is on the base branch already, so no merge request was opened.",
		},
		{
			name: "a snapshot of other content", live: testRulesBefore, liveSHA: testRulesSHA,
			snap: &action.Snapshot{
				Before: json.RawMessage(`{"` + testRulesPath + `": {"blob": "` + testRulesSHA + `"}}`),
				After:  json.RawMessage(`{"` + testRulesPath + `": {"blob": "` + blobSHA("something else") + `"}}`),
			},
			wantErrIs: action.ErrPermanent, wantErr: "is not the content the snapshot describes",
		},
		{
			name: "a snapshot that lacks the file", live: testRulesBefore, liveSHA: testRulesSHA,
			snap:      &action.Snapshot{Before: json.RawMessage(`{}`), After: json.RawMessage(`{}`)},
			wantErrIs: action.ErrPermanent, wantErr: "has no entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.handleMergeRequests(nil)
			b.expectNoBranch()
			b.expectBase()
			b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, tt.live, tt.liveSHA)))

			if tt.wantMR {
				b.validatorPasses(1)
				b.expectCommit()
				b.expectOpen(12, true)
				b.gitlab.ExpectAllCalled()
			}

			inv := invocation(rulesParams())
			inv.Snapshot = tt.snap

			res, err := b.mr.Execute(t.Context(), inv)

			switch {
			case tt.wantErrIs != nil:
				require.ErrorIs(t, err, tt.wantErrIs)
				require.ErrorContains(t, err, tt.wantErr)
			case tt.wantResult != "":
				require.NoError(t, err)
				require.Equal(t, tt.wantResult, res.Summary)
				require.Empty(t, res.Links)
			default:
				require.NoError(t, err)
				require.Contains(t, res.Summary, "merge request")
			}

			if !tt.wantMR {
				for _, call := range sent(b.gitlab) {
					require.True(t, strings.HasPrefix(call, "GET "), "nothing is written: %s", call)
				}

				require.Empty(t, b.validator.Requests())
			}
		})
	}
}

// jsonString returns s as a JSON string.
func jsonString(t *testing.T, s string) string {
	t.Helper()

	raw, err := json.Marshal(s)
	require.NoError(t, err)

	return string(raw)
}
