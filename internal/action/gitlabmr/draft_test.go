package gitlabmr

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
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

	closeCall := "PUT merge_requests/12"
	deleteCall := "DELETE repository/branches/" + testBranch
	commitThenOpen := []string{"POST repository/commits", "POST merge_requests"}

	foundOnRetry := func(list string, extra func(b *bed)) func(b *bed) {
		return func(b *bed) {
			b.handleMergeRequests(map[string]string{testBranch: list})

			if extra != nil {
				extra(b)
			}
		}
	}

	closes := func(b *bed) {
		b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(mergeRequestBody(12, "closed", false)))
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})
	}

	deletesBranch := func(b *bed) {
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})
	}

	newBranch := func(b *bed) {
		b.handleMergeRequests(nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), notFound)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validatorPasses(1)
		b.expectCommit()
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
			setup:      foundOnRetry("["+mergeRequestBody(12, "opened", false)+"]", closes),
			wantWrites: []string{closeCall, deleteCall},
			wantErr:    "acme/platform/monitors!12 was not a draft",
		},
		{
			name:       "found on a retry: closed and not a draft",
			setup:      foundOnRetry("["+mergeRequestBody(12, "closed", false)+"]", deletesBranch),
			wantWrites: []string{deleteCall},
			wantErr:    "acme/platform/monitors!12 was not a draft",
		},
		{
			name: "found on a retry: closed and not a draft, branch deleted",
			setup: foundOnRetry("["+mergeRequestBody(12, "closed", false)+"]", func(b *bed) {
				b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), notFound)
			}),
			wantWrites: []string{deleteCall},
			wantErr:    "acme/platform/monitors!12 was not a draft",
		},
		{
			name:       "found on a retry: being merged and not a draft",
			setup:      foundOnRetry("["+mergeRequestBody(12, "locked", false)+"]", nil),
			wantWrites: nil,
			wantErr:    "GitLab is merging it",
		},
		{
			name:       "found on a retry: merged by a person",
			setup:      foundOnRetry(mergedList(12), nil),
			wantWrites: nil,
		},
		{
			name:       "found on a retry: a draft",
			setup:      foundOnRetry(openList(12), nil),
			wantWrites: nil,
		},
		{
			name: "found on a retry: GitLab refuses to close it",
			setup: foundOnRetry("["+mergeRequestBody(12, "opened", false)+"]", func(b *bed) {
				b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), failed(http.StatusForbidden, "403 Forbidden"))
			}),
			wantWrites: []string{closeCall},
			wantErr:    "close the merge request acme/platform/monitors!12 that is not a draft",
		},
		{
			name: "newly opened and not a draft",
			setup: func(b *bed) {
				newBranch(b)
				b.expectOpen(12, false)
				closes(b)
			},
			wantWrites: append(slices.Clone(commitThenOpen), closeCall, deleteCall),
			wantErr:    "acme/platform/monitors!12 was not a draft",
		},
		{
			name: "found after a 409 and not a draft",
			setup: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("[]"), reply("["+mergeRequestBody(12, "opened", false)+"]"))
				b.expectBranch()
				b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "Another open merge request already exists for this source branch: !12"))
				closes(b)
			},
			wantWrites: []string{"POST merge_requests", closeCall, deleteCall},
			wantErr:    "acme/platform/monitors!12 was not a draft",
		},
		{
			name: "newly opened as a draft",
			setup: func(b *bed) {
				newBranch(b)
				b.expectOpen(12, true)
			},
			wantWrites: commitThenOpen,
		},
		{
			name:       "draft is false: a merge request found on a retry is left as it is",
			limits:     func(l *Limits) { l.Draft = new(false) },
			setup:      foundOnRetry("["+mergeRequestBody(12, "opened", false)+"]", nil),
			wantWrites: nil,
		},
		{
			name:   "draft is false: a merge request that is opened is left as it is",
			limits: func(l *Limits) { l.Draft = new(false) },
			setup: func(b *bed) {
				newBranch(b)
				b.expectOpen(12, false)
			},
			wantWrites: commitThenOpen,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			tt.setup(b)

			_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
			require.Equal(t, tt.wantWrites, writes(b.gitlab))

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent, "a repeat never ends in success for a merge request that is not a draft")
		})
	}
}

func TestUndoRevertWithDraftChecksEveryPath(t *testing.T) {
	t.Parallel()

	revertList := func(state string, draft bool) string {
		return "[" + mergeRequestBodyFrom(30, state, draft, testUndoBranch) + "]"
	}
	closeCall := "PUT merge_requests/30"
	deleteCall := "DELETE repository/branches/" + testUndoBranch
	commitThenOpen := []string{"POST repository/commits", "POST merge_requests"}

	closes := func(b *bed) {
		b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/30"), reply(mergeRequestBodyFrom(30, "closed", false, testUndoBranch)))
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testUndoBranch), fakehttp.Response{Status: http.StatusNoContent})
	}

	foundOnRetry := func(list string, extra func(b *bed)) func(b *bed) {
		return func(b *bed) {
			b.handleMergeRequests(map[string]string{testBranch: mergedList(12), testUndoBranch: list})

			if extra != nil {
				extra(b)
			}
		}
	}

	opens := func(draft bool, extra func(b *bed)) func(b *bed) {
		return func(b *bed) {
			b.handleMergeRequests(map[string]string{testBranch: mergedList(12)})
			b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
			b.expectBase()
			b.expectMergedFiles()
			b.expectBeforeBlobs()
			b.expectCommit()
			b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(30, "opened", draft, testUndoBranch)))

			if extra != nil {
				extra(b)
			}
		}
	}

	tests := []struct {
		name       string
		limits     func(l *Limits)
		setup      func(b *bed)
		wantWrites []string
		wantErr    string
	}{
		{
			name:       "found on a retry: open and not a draft",
			setup:      foundOnRetry(revertList("opened", false), closes),
			wantWrites: []string{closeCall, deleteCall},
			wantErr:    "acme/platform/monitors!30 was not a draft",
		},
		{
			name: "found on a retry: closed and not a draft",
			setup: foundOnRetry(revertList("closed", false), func(b *bed) {
				b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testUndoBranch), fakehttp.Response{Status: http.StatusNoContent})
			}),
			wantWrites: []string{deleteCall},
			wantErr:    "acme/platform/monitors!30 was not a draft",
		},
		{
			name:       "found on a retry: a draft",
			setup:      foundOnRetry(revertList("opened", true), nil),
			wantWrites: nil,
		},
		{
			name:       "newly opened and not a draft",
			setup:      opens(false, closes),
			wantWrites: append(slices.Clone(commitThenOpen), closeCall, deleteCall),
			wantErr:    "acme/platform/monitors!30 was not a draft",
		},
		{
			name:       "newly opened as a draft",
			setup:      opens(true, nil),
			wantWrites: commitThenOpen,
		},
		{
			name:       "draft is false: a revert found on a retry is left as it is",
			limits:     func(l *Limits) { l.Draft = new(false) },
			setup:      foundOnRetry(revertList("opened", false), nil),
			wantWrites: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			tt.setup(b)

			_, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
			require.Equal(t, tt.wantWrites, writes(b.gitlab))

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}
