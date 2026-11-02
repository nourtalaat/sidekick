package githubpr

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

func TestExecuteOpensPullRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limits    func(l *Limits)
		wantDraft bool
		wantWord  string
	}{
		{name: "a draft by default", wantDraft: true, wantWord: "draft pull request"},
		{name: "a pull request ready for review", limits: func(l *Limits) { l.Draft = new(false) }, wantWord: "pull request acme/monitors#12"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			b.expectNoBranch()
			b.expectBase()
			b.expectFile(testRulesPath, testRulesBefore)
			b.validatorPasses(1)
			b.expectPush()
			b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(12, tt.wantDraft)))
			b.github.ExpectAllCalled()
			b.validator.ExpectAllCalled()

			params := rulesParams()
			params.Body = "The alert fires on short spikes. cc @octocat, fixes #9"

			res, err := b.pr.Execute(t.Context(), invocation(params))
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.wantWord)
			require.Equal(t, []action.Link{{Title: "acme/monitors#12", URL: "https://github.com/acme/monitors/pull/12"}}, res.Links)

			require.Equal(t, []string{
				"GET /repos/acme/monitors/git/ref/heads/" + testBranch,
				"GET /repos/acme/monitors/git/ref/heads/main",
				"GET /repos/acme/monitors/git/commits/" + testBaseSHA,
				"GET /repos/acme/monitors/contents/" + testRulesPath,
				"POST /repos/acme/monitors/git/trees",
				"POST /repos/acme/monitors/git/commits",
				"POST /repos/acme/monitors/git/refs",
				"POST /repos/acme/monitors/pulls",
			}, sent(b.github))

			require.Equal(t, validateclient.Request{
				Format: validateclient.FormatPrometheusRules,
				Files:  []validateclient.File{{Path: testRulesPath, Content: testRulesAfter}},
			}, validatorRequest(t, b.validator, 0))

			tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
			require.Equal(t, testBaseTree, tree["base_tree"])
			require.Equal(t, []any{map[string]any{"path": testRulesPath, "mode": "100644", "type": "blob", "content": testRulesAfter}}, tree["tree"])

			commit := bodyOf(t, b.github, http.MethodPost, repoPath("git/commits"))
			require.Equal(t, testTreeSHA, commit["tree"])
			require.Equal(t, []any{testBaseSHA}, commit["parents"])
			require.Equal(t, "Raise the checkout latency threshold\n\nSidekick proposal "+testProposalID+"\n", commit["message"])

			ref := bodyOf(t, b.github, http.MethodPost, repoPath("git/refs"))
			require.Equal(t, map[string]any{"ref": "refs/heads/" + testBranch, "sha": testCommitSHA}, ref)

			pull := bodyOf(t, b.github, http.MethodPost, repoPath("pulls"))
			require.Equal(t, testBranch, pull["head"])
			require.Equal(t, "main", pull["base"])
			require.Equal(t, "Raise the checkout latency threshold", pull["title"])
			require.Equal(t, tt.wantDraft, pull["draft"])

			body, ok := pull["body"].(string)
			require.True(t, ok)
			require.Contains(t, body, "cc `@octocat`, fixes `#9`")
			require.Contains(t, body, "Opened by Sidekick for proposal `"+testProposalID+"`.")
			require.Contains(t, body, "https://sidekick.example.com")
		})
	}
}

func TestExecuteDeletesAndCreatesFiles(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile("prometheus/rules/old.yaml", "groups: []\n")
	b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/new.yaml"), notFound)
	b.validatorPasses(1)
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(3, true)))
	b.github.ExpectAllCalled()

	params := validParams()
	params.Files = []FileChange{
		{Path: "prometheus/rules/old.yaml", Delete: true},
		{Path: "prometheus/rules/new.yaml", Content: text("groups:\n- name: a\n  rules: []\n")},
	}

	_, err := b.pr.Execute(t.Context(), invocation(params))
	require.NoError(t, err)

	tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, []any{
		map[string]any{"path": "prometheus/rules/old.yaml", "sha": nil},
		map[string]any{"path": "prometheus/rules/new.yaml", "mode": "100644", "type": "blob", "content": "groups:\n- name: a\n  rules: []\n"},
	}, tree["tree"])

	require.Equal(t, validateclient.Request{
		Format: validateclient.FormatPrometheusRules,
		Files:  []validateclient.File{{Path: "prometheus/rules/new.yaml", Content: "groups:\n- name: a\n  rules: []\n"}},
	}, validatorRequest(t, b.validator, 0), "a deleted file is not sent to the validator")
}

func TestExecuteRetryAfterCrash(t *testing.T) {
	t.Parallel()

	branchRef := reply(refBody("refs/heads/"+testBranch, testCommitSHA))

	tests := []struct {
		name     string
		prs      string
		prStatus []fakehttp.Response
		writes   func(b *bed)
		want     []string
		wantText string
	}{
		{
			name:     "the branch and its pull request exist",
			prs:      "[" + pullBody(12, true) + "]",
			wantText: "Found the already opened draft pull request acme/monitors#12",
			want: []string{
				"GET /repos/acme/monitors/git/ref/heads/" + testBranch,
				"GET /repos/acme/monitors/pulls",
			},
		},
		{
			name:     "the pull request was closed meanwhile",
			prs:      `[{"number":12,"draft":false,"state":"closed","html_url":"https://github.com/acme/monitors/pull/12"}]`,
			wantText: "Found the already opened pull request acme/monitors#12",
			want: []string{
				"GET /repos/acme/monitors/git/ref/heads/" + testBranch,
				"GET /repos/acme/monitors/pulls",
			},
		},
		{
			name: "the branch exists and has no pull request",
			prs:  "[]",
			writes: func(b *bed) {
				b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(13, true)))
			},
			wantText: "Opened draft pull request acme/monitors#13",
			want: []string{
				"GET /repos/acme/monitors/git/ref/heads/" + testBranch,
				"GET /repos/acme/monitors/pulls",
				"POST /repos/acme/monitors/pulls",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testBranch), branchRef)
			b.github.Handle(http.MethodGet, repoPath("pulls"), reply(tt.prs))

			if tt.writes != nil {
				tt.writes(b)
			}

			b.github.ExpectAllCalled()

			res, err := b.pr.Execute(t.Context(), invocation(rulesParams()))
			require.NoError(t, err)
			require.Equal(t, tt.wantText, res.Summary)
			require.Equal(t, tt.want, sent(b.github))

			require.Empty(t, b.validator.Requests(), "nothing is validated or written again")

			for _, r := range b.github.Requests() {
				if r.Path == repoPath("pulls") && r.Method == http.MethodGet {
					require.Equal(t, "acme:"+testBranch, r.Query.Get("head"))
					require.Equal(t, "all", r.Query.Get("state"))
				}
			}
		})
	}
}

func TestExecuteRacesWithAnotherAttempt(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile(testRulesPath, testRulesBefore)
	b.validatorPasses(1)
	b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, `{"sha":"`+testTreeSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, `{"sha":"`+testCommitSHA+`"}`))
	b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusUnprocessableEntity, `{"message":"Reference already exists"}`))
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusUnprocessableEntity,
		`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for acme:`+testBranch+`."}]}`))
	b.github.Handle(http.MethodGet, repoPath("pulls"), reply("["+pullBody(14, true)+"]"))
	b.github.ExpectAllCalled()

	res, err := b.pr.Execute(t.Context(), invocation(rulesParams()))
	require.NoError(t, err)
	require.Equal(t, []action.Link{{Title: "acme/monitors#14", URL: "https://github.com/acme/monitors/pull/14"}}, res.Links)
}

func TestExecuteFailedValidationOpensNothing(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile(testRulesPath, testRulesBefore)
	b.validator.Handle(http.MethodPost, "/validate", reply(`{"ok":false,"errors":[{"path":"`+testRulesPath+`","line":6,"message":"bad duration \"10x\""}]}`))
	b.github.ExpectAllCalled()
	b.validator.ExpectAllCalled()

	_, err := b.pr.Execute(t.Context(), invocation(rulesParams()))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, testRulesPath+":6: bad duration")

	for _, call := range sent(b.github) {
		require.NotContains(t, call, "POST", "no tree, commit, branch or pull request is made")
	}
}

func TestExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(b *bed)
		change        func(p *Params)
		invocation    func(inv *action.Invocation)
		wantPermanent bool
		wantLimit     bool
		wantErr       string
		wantErrIs     error
		// skipsGitHub is set for a case that fails before it calls GitHub.
		skipsGitHub bool
	}{
		{
			name: "the validator refuses the request",
			setup: func(b *bed) {
				b.validator.Handle(http.MethodPost, "/validate", replyStatus(http.StatusBadRequest, `{"error":"bad request: path"}`))
			},
			wantPermanent: true,
			wantErrIs:     validateclient.ErrBadRequest,
		},
		{
			name: "the validator is busy",
			setup: func(b *bed) {
				b.validator.Handle(http.MethodPost, "/validate", replyStatus(http.StatusServiceUnavailable, `{"error":"busy"}`))
			},
			wantErrIs: validateclient.ErrBusy,
		},
		{
			name: "the validator times out",
			setup: func(b *bed) {
				b.validator.Handle(http.MethodPost, "/validate", replyStatus(http.StatusGatewayTimeout, `{"error":"timeout"}`))
			},
			wantErrIs: validateclient.ErrTimeout,
		},
		{
			name: "the changes touch too many lines",
			change: func(p *Params) {
				p.Files[0].Content = text(strings.Repeat("x\n", 60))
			},
			wantPermanent: true,
			wantLimit:     true,
			wantErr:       "more than the 40 lines",
		},
		{
			name:          "the file already has the content",
			change:        func(p *Params) { p.Files[0].Content = text(testRulesBefore) },
			wantPermanent: true,
			wantErr:       "already has this content",
		},
		{
			name:        "the idempotency key is not a branch name",
			invocation:  func(inv *action.Invocation) { inv.IdempotencyKey = "sidekick/../main" },
			skipsGitHub: true,
			wantErrIs:   action.ErrPermanent,
			wantErr:     "no idempotency key",
		},
		{
			name:          "the repository is outside the limits",
			change:        func(p *Params) { p.Repo = "acme/other" },
			skipsGitHub:   true,
			wantPermanent: true,
			wantLimit:     true,
			wantErr:       "limits.repos",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			if !tt.skipsGitHub {
				b.expectNoBranch()
				b.expectBase()
				b.expectFile(testRulesPath, testRulesBefore)
			}

			if tt.setup != nil {
				tt.setup(b)
			}

			params := rulesParams()
			if tt.change != nil {
				tt.change(&params)
			}

			inv := invocation(params)
			if tt.invocation != nil {
				tt.invocation(&inv)
			}

			_, err := b.pr.Execute(t.Context(), inv)
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, hasPermanent(err))
			require.Equal(t, tt.wantLimit, hasLimit(err))

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			}

			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
			}

			for _, call := range sent(b.github) {
				require.NotContains(t, call, "POST", "nothing is written when a check fails")
			}
		})
	}
}

func TestExecuteGitHubErrors(t *testing.T) {
	t.Parallel()

	rateLimited := fakehttp.Response{
		Status: http.StatusForbidden,
		Headers: map[string]string{
			"Content-Type":          "application/json",
			"X-RateLimit-Limit":     "5000",
			"X-RateLimit-Remaining": "0",
			"X-RateLimit-Reset":     "4102444800",
		},
		Body: []byte(`{"message":"API rate limit exceeded"}`),
	}

	tests := []struct {
		name          string
		base          fakehttp.Response
		wantPermanent bool
	}{
		{name: "the base branch does not exist", base: notFound, wantPermanent: true},
		{name: "the app may not read the repository", base: replyStatus(http.StatusForbidden, `{"message":"Resource not accessible by integration"}`), wantPermanent: true},
		{name: "GitHub fails", base: replyStatus(http.StatusBadGateway, `{"message":"Server Error"}`)},
		{name: "the rate limit is used up", base: rateLimited},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.expectNoBranch()
			b.github.Handle(http.MethodGet, repoPath("git/ref/heads/main"), tt.base)

			_, err := b.pr.Execute(t.Context(), invocation(rulesParams()))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, hasPermanent(err))
		})
	}
}

func TestExecuteTerraformModule(t *testing.T) {
	t.Parallel()

	const (
		mainTF   = "module \"alert\" {\n  source = \"../modules/alert\"\n}\n"
		mainNew  = "module \"alert\" {\n  source = \"../modules/alert\"\n  threshold = 10\n}\n"
		oldTF    = "resource \"datadog_monitor\" \"old\" {}\n"
		moduleTF = "variable \"threshold\" {}\n"
		readme   = "# notes\n"
	)

	b := newBed(t, nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile("datadog/prod/main.tf", mainTF)
	b.expectFile("datadog/prod/old.tf", oldTF)
	b.github.Handle(http.MethodGet, repoPath("contents/datadog/prod"), reply(listing("datadog/prod", map[string]string{"main.tf": mainTF, "old.tf": oldTF, "README.md": readme})))
	b.github.Handle(http.MethodGet, repoPath("contents/datadog/modules/alert"), reply(listing("datadog/modules/alert", map[string]string{"main.tf": moduleTF})))
	b.github.Handle(http.MethodGet, repoPath("git/blobs/blob-datadog/modules/alert/main.tf"), fakehttp.Response{Body: []byte(moduleTF)})
	b.validatorPasses(1)
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(5, true)))
	b.github.ExpectAllCalled()
	b.validator.ExpectAllCalled()

	params := validParams()
	params.Files = []FileChange{
		{Path: "datadog/prod/main.tf", Content: text(mainNew)},
		{Path: "datadog/prod/old.tf", Delete: true},
	}

	_, err := b.pr.Execute(t.Context(), invocation(params))
	require.NoError(t, err)

	require.Equal(t, validateclient.Request{
		Format: validateclient.FormatTerraform,
		Dir:    "datadog/prod",
		Files: []validateclient.File{
			{Path: "datadog/modules/alert/main.tf", Content: moduleTF},
			{Path: "datadog/prod/main.tf", Content: mainNew},
		},
	}, validatorRequest(t, b.validator, 0), "the whole module with the change applied, the delete gone and no README")
}

func TestExecuteSeveralFormats(t *testing.T) {
	t.Parallel()

	const grafanaFile = "grafana/alerting/g.yaml"

	b := newBed(t, nil)
	b.expectNoBranch()
	b.expectBase()
	b.expectFile(testRulesPath, testRulesBefore)
	b.expectFile(grafanaFile, "apiVersion: 1\n")
	b.validatorPasses(2)
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(6, true)))

	params := validParams()
	params.Files = []FileChange{
		{Path: testRulesPath, Content: text(testRulesAfter)},
		{Path: grafanaFile, Content: text("apiVersion: 1\ngroups: []\n")},
	}

	_, err := b.pr.Execute(t.Context(), invocation(params))
	require.NoError(t, err)

	require.Equal(t, validateclient.FormatPrometheusRules, validatorRequest(t, b.validator, 0).Format)
	require.Equal(t, validateclient.FormatGrafanaAlertFile, validatorRequest(t, b.validator, 1).Format)
}

// hasPermanent reports whether err tells Sidekick that a repeat cannot help.
func hasPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent) || errors.Is(err, action.ErrLimit)
}

// hasLimit reports whether err is a limit violation.
func hasLimit(err error) bool {
	return errors.Is(err, action.ErrLimit)
}
