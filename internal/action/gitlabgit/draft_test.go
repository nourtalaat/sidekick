package gitlabgit

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestRequireDraft(t *testing.T) {
	t.Parallel()

	closeCall := "PUT merge_requests/4"
	deleteCall := "DELETE repository/branches/" + testBranch

	tests := []struct {
		name      string
		mr        MergeRequest
		wantCalls []string
		wantErr   bool
	}{
		{name: "an open draft", mr: MergeRequest{IID: 4, State: "opened", Draft: true}},
		{name: "a closed draft", mr: MergeRequest{IID: 4, State: "closed", Draft: true}},
		{name: "a merged merge request that is not a draft is left alone", mr: MergeRequest{IID: 4, State: "merged"}},
		{name: "an open merge request that is not a draft is closed and its branch deleted", mr: MergeRequest{IID: 4, State: "opened"}, wantCalls: []string{closeCall, deleteCall}, wantErr: true},
		{name: "a closed merge request that is not a draft loses its branch", mr: MergeRequest{IID: 4, State: "closed"}, wantCalls: []string{deleteCall}, wantErr: true},
		{name: "a merge request that GitLab is merging is not touched", mr: MergeRequest{IID: 4, State: "locked"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t)
			b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/4"), reply(mergeRequestBody(4, "closed", false)))
			b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), reply(`{}`))

			err := RequireDraft(t.Context(), b.client, testProject, testBranch, &tt.mr)
			require.Equal(t, tt.wantCalls, sent(b.gitlab))

			if !tt.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}

func TestRequireDraftStopsWhenTheCloseFails(t *testing.T) {
	t.Parallel()

	b := newBed(t)
	b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/4"), failed(http.StatusInternalServerError, "boom"))

	err := RequireDraft(t.Context(), b.client, testProject, testBranch, &MergeRequest{IID: 4, State: "opened"})
	require.ErrorContains(t, err, "close the merge request")
	require.NotErrorIs(t, err, action.ErrPermanent, "a failed close is tried again")
	require.Equal(t, []string{"PUT merge_requests/4"}, sent(b.gitlab), "the branch of an open merge request stays")
}

func TestRequireDraftTreatsAGoneBranchAsDeleted(t *testing.T) {
	t.Parallel()

	b := newBed(t)
	b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), notFound)

	err := RequireDraft(t.Context(), b.client, testProject, testBranch, &MergeRequest{IID: 4, State: "closed"})
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestOpenDraft(t *testing.T) {
	t.Parallel()

	req := CreateRequest{SourceBranch: testBranch, TargetBranch: "main", Title: "Draft: x", Description: "d"}

	t.Run("a draft is returned", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), reply(mergeRequestBody(4, "opened", true)))

		mr, err := OpenDraft(t.Context(), b.client, testProject, testBranch, req)
		require.NoError(t, err)
		require.Equal(t, 4, mr.IID)
		require.Equal(t, []string{"POST merge_requests"}, sent(b.gitlab))
	})

	t.Run("a merge request GitLab made without the draft state is closed", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), reply(mergeRequestBody(4, "opened", false)))
		b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/4"), reply(mergeRequestBody(4, "closed", false)))
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), reply(`{}`))

		_, err := OpenDraft(t.Context(), b.client, testProject, testBranch, req)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.Equal(t, []string{"POST merge_requests", "PUT merge_requests/4", "DELETE repository/branches/" + testBranch}, sent(b.gitlab))
	})

	t.Run("one found after a conflict is checked too", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "Another open merge request already exists"))
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("["+mergeRequestBody(4, "opened", false)+"]"))
		b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/4"), reply(mergeRequestBody(4, "closed", false)))
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), reply(`{}`))

		_, err := OpenDraft(t.Context(), b.client, testProject, testBranch, req)
		require.ErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("a failed open makes no check", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusForbidden, "no"))

		_, err := OpenDraft(t.Context(), b.client, testProject, testBranch, req)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.Equal(t, []string{"POST merge_requests"}, sent(b.gitlab))
	})
}
