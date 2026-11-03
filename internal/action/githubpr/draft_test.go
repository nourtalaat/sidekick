package githubpr

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const (
	notDraftOpen   = `{"number":12,"draft":false,"state":"open","html_url":"https://github.com/acme/monitors/pull/12"}`
	notDraftClosed = `{"number":12,"draft":false,"state":"closed","html_url":"https://github.com/acme/monitors/pull/12"}`
	notDraftMerged = `{"number":12,"draft":false,"state":"closed","html_url":"https://github.com/acme/monitors/pull/12","merged_at":"2026-11-03T10:00:00Z"}`
)

// writes returns the calls that change something, in order.
func writes(s *fakehttp.Server) []string {
	var out []string

	for _, call := range sent(s) {
		if !strings.HasPrefix(call, "GET ") {
			out = append(out, call)
		}
	}

	return out
}

func TestExecuteWithDraftChecksEveryPath(t *testing.T) {
	t.Parallel()

	closeCall := "PATCH /repos/acme/monitors/pulls/12"
	deleteCall := "DELETE /repos/acme/monitors/git/refs/heads/" + testBranch
	pushCalls := []string{"POST /repos/acme/monitors/git/trees", "POST /repos/acme/monitors/git/commits", "POST /repos/acme/monitors/git/refs"}

	foundOnRetry := func(prs string, extra func(b *bed)) func(b *bed) {
		return func(b *bed) {
			b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testBranch), reply(refBody("refs/heads/"+testBranch, testCommitSHA)))
			b.github.Handle(http.MethodGet, repoPath("pulls"), reply("["+prs+"]"))

			if extra != nil {
				extra(b)
			}
		}
	}

	closes := func(b *bed) {
		b.github.Handle(http.MethodPatch, repoPath("pulls/12"), reply(notDraftClosed))
		b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})
	}

	tests := []struct {
		name       string
		limits     func(l *Limits)
		setup      func(b *bed)
		wantWrites []string
		// wantErr is empty when Execute succeeds.
		wantErr string
	}{
		{
			name:       "found on a retry: open and not a draft",
			setup:      foundOnRetry(notDraftOpen, closes),
			wantWrites: []string{closeCall, deleteCall},
			wantErr:    "did not open acme/monitors#12 as a draft",
		},
		{
			name: "found on a retry: closed and not a draft, branch still there",
			setup: foundOnRetry(notDraftClosed, func(b *bed) {
				b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})
			}),
			wantWrites: []string{deleteCall},
			wantErr:    "did not open acme/monitors#12 as a draft",
		},
		{
			name: "found on a retry: closed and not a draft, branch deleted",
			setup: foundOnRetry(notDraftClosed, func(b *bed) {
				b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), replyStatus(http.StatusUnprocessableEntity, `{"message":"Reference does not exist"}`))
			}),
			wantWrites: []string{deleteCall},
			wantErr:    "did not open acme/monitors#12 as a draft",
		},
		{
			name:       "found on a retry: merged by a person",
			setup:      foundOnRetry(notDraftMerged, nil),
			wantWrites: nil,
		},
		{
			name:       "found on a retry: a draft",
			setup:      foundOnRetry(pullBody(12, true), nil),
			wantWrites: nil,
		},
		{
			name: "found on a retry: GitHub refuses to close it",
			setup: foundOnRetry(notDraftOpen, func(b *bed) {
				b.github.Handle(http.MethodPatch, repoPath("pulls/12"), replyStatus(http.StatusForbidden, `{"message":"Resource not accessible by integration"}`))
			}),
			wantWrites: []string{closeCall},
			wantErr:    "close the pull request acme/monitors#12 that is not a draft",
		},
		{
			name: "newly opened and not a draft",
			setup: func(b *bed) {
				b.expectNoBranch()
				b.expectBase()
				b.expectFile(testRulesPath, testRulesBefore)
				b.validatorPasses(1)
				b.expectPush()
				b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, notDraftOpen))
				closes(b)
			},
			wantWrites: append(slices.Clone(pushCalls), "POST /repos/acme/monitors/pulls", closeCall, deleteCall),
			wantErr:    "did not open acme/monitors#12 as a draft",
		},
		{
			name: "found after a 409 and not a draft",
			setup: func(b *bed) {
				b.expectNoBranch()
				b.expectBase()
				b.expectFile(testRulesPath, testRulesBefore)
				b.validatorPasses(1)
				b.expectPush()
				b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusUnprocessableEntity,
					`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for acme:`+testBranch+`."}]}`))
				b.github.Handle(http.MethodGet, repoPath("pulls"), reply("["+notDraftOpen+"]"))
				closes(b)
			},
			wantWrites: append(slices.Clone(pushCalls), "POST /repos/acme/monitors/pulls", closeCall, deleteCall),
			wantErr:    "did not open acme/monitors#12 as a draft",
		},
		{
			name: "newly opened as a draft",
			setup: func(b *bed) {
				b.expectNoBranch()
				b.expectBase()
				b.expectFile(testRulesPath, testRulesBefore)
				b.validatorPasses(1)
				b.expectPush()
				b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(12, true)))
			},
			wantWrites: append(slices.Clone(pushCalls), "POST /repos/acme/monitors/pulls"),
		},
		{
			name:       "draft is false: a pull request found on a retry is left as it is",
			limits:     func(l *Limits) { l.Draft = new(false) },
			setup:      foundOnRetry(notDraftOpen, nil),
			wantWrites: nil,
		},
		{
			name:   "draft is false: a pull request that is opened is left as it is",
			limits: func(l *Limits) { l.Draft = new(false) },
			setup: func(b *bed) {
				b.expectNoBranch()
				b.expectBase()
				b.expectFile(testRulesPath, testRulesBefore)
				b.validatorPasses(1)
				b.expectPush()
				b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, notDraftOpen))
			},
			wantWrites: append(slices.Clone(pushCalls), "POST /repos/acme/monitors/pulls"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			tt.setup(b)

			_, err := b.pr.Execute(t.Context(), invocation(rulesParams()))
			require.Equal(t, tt.wantWrites, writes(b.github))

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent, "a repeat never ends in success for a pull request that is not a draft")
		})
	}
}

func TestUndoRevertWithDraftChecksEveryPath(t *testing.T) {
	t.Parallel()

	revertPull := func(draft bool, state string) string {
		return `{"number":30,"draft":` + boolText(draft) + `,"state":"` + state + `","html_url":"https://github.com/acme/monitors/pull/30"}`
	}
	closeCall := "PATCH /repos/acme/monitors/pulls/30"
	deleteCall := "DELETE /repos/acme/monitors/git/refs/heads/" + testUndoBranch

	closes := func(b *bed) {
		b.github.Handle(http.MethodPatch, repoPath("pulls/30"), reply(revertPull(false, "closed")))
		b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testUndoBranch), fakehttp.Response{Status: http.StatusNoContent})
	}

	foundOnRetry := func(pull string, extra func(b *bed)) func(b *bed) {
		return func(b *bed) {
			b.handlePulls(map[string]string{"acme:" + testBranch: mergedPull(12), "acme:" + testUndoBranch: "[" + pull + "]"})
			b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testUndoBranch), reply(refBody("refs/heads/"+testUndoBranch, testCommitSHA)))

			if extra != nil {
				extra(b)
			}
		}
	}

	opens := func(pull string, extra func(b *bed)) func(b *bed) {
		return func(b *bed) {
			b.handlePulls(map[string]string{"acme:" + testBranch: mergedPull(12)})
			b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testUndoBranch), notFound)
			b.expectBase()
			b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesAfter, blobSHA(testRulesAfter))))
			b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/new.yaml"), reply(fileBodyWithSHA("prometheus/rules/new.yaml", "x: 1\n", blobSHA("x: 1\n"))))
			b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/old.yaml"), notFound)
			b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, `{"sha":"`+testTreeSHA+`"}`))
			b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, `{"sha":"`+testCommitSHA+`"}`))
			b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusCreated, refBody("refs/heads/"+testUndoBranch, testCommitSHA)))
			b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pull))

			if extra != nil {
				extra(b)
			}
		}
	}

	pushCalls := []string{"POST /repos/acme/monitors/git/trees", "POST /repos/acme/monitors/git/commits", "POST /repos/acme/monitors/git/refs"}

	tests := []struct {
		name       string
		limits     func(l *Limits)
		setup      func(b *bed)
		wantWrites []string
		wantErr    string
	}{
		{
			name:       "found on a retry: open and not a draft",
			setup:      foundOnRetry(revertPull(false, "open"), closes),
			wantWrites: []string{closeCall, deleteCall},
			wantErr:    "did not open acme/monitors#30 as a draft",
		},
		{
			name: "found on a retry: closed and not a draft",
			setup: foundOnRetry(revertPull(false, "closed"), func(b *bed) {
				b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testUndoBranch), fakehttp.Response{Status: http.StatusNoContent})
			}),
			wantWrites: []string{deleteCall},
			wantErr:    "did not open acme/monitors#30 as a draft",
		},
		{
			name:       "found on a retry: a draft",
			setup:      foundOnRetry(revertPull(true, "open"), nil),
			wantWrites: nil,
		},
		{
			name:       "newly opened and not a draft",
			setup:      opens(revertPull(false, "open"), closes),
			wantWrites: append(slices.Clone(pushCalls), "POST /repos/acme/monitors/pulls", closeCall, deleteCall),
			wantErr:    "did not open acme/monitors#30 as a draft",
		},
		{
			name:       "newly opened as a draft",
			setup:      opens(revertPull(true, "open"), nil),
			wantWrites: append(slices.Clone(pushCalls), "POST /repos/acme/monitors/pulls"),
		},
		{
			name:       "draft is false: a revert found on a retry is left as it is",
			limits:     func(l *Limits) { l.Draft = new(false) },
			setup:      foundOnRetry(revertPull(false, "open"), nil),
			wantWrites: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			tt.setup(b)

			_, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
			require.Equal(t, tt.wantWrites, writes(b.github))

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}

// boolText writes a JSON boolean.
func boolText(v bool) string {
	if v {
		return "true"
	}

	return "false"
}
