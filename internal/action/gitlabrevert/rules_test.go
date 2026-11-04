package gitlabrevert

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

func TestRefusalMatchesLimitAndPermanent(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("check: %w", refuse("%q changed", "a.yaml"))

	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrConflict)
	require.ErrorContains(t, err, `"a.yaml" changed`)
}

func TestCheckAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		landed  time.Time
		wantErr bool
	}{
		{name: "just landed", landed: now},
		{name: "in the future counts as now", landed: now.Add(time.Hour)},
		{name: "at the limit", landed: now.Add(-time.Hour)},
		{name: "just over the limit", landed: now.Add(-time.Hour - time.Second), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkAge("commit abc", tt.landed, now, time.Hour)
			if tt.wantErr {
				require.ErrorIs(t, err, action.ErrLimit)
				require.ErrorContains(t, err, "commit abc landed on the base branch")

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestIsSidekickRevert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mr   gitlabgit.MergeRequest
		want bool
	}{
		{name: "the branch and the trailer", mr: gitlabgit.MergeRequest{SourceBranch: testBranch, Description: "Why.\n\nSidekick-Revert: " + testProposalID}, want: true},
		{name: "the trailer with spaces before it", mr: gitlabgit.MergeRequest{SourceBranch: testBranch, Description: "Why.\n  Sidekick-Revert: x"}, want: true},
		{name: "the branch without the trailer", mr: gitlabgit.MergeRequest{SourceBranch: testBranch, Description: "Opened by Sidekick."}},
		{name: "the trailer on another branch", mr: gitlabgit.MergeRequest{SourceBranch: "feature/x", Description: "Sidekick-Revert: x"}},
		{name: "the trailer in the middle of a line", mr: gitlabgit.MergeRequest{SourceBranch: testBranch, Description: "see Sidekick-Revert: x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, isSidekickRevert(&tt.mr))
		})
	}
}

func TestFileFailure(t *testing.T) {
	t.Parallel()

	for _, sentinel := range []error{gitlabgit.ErrNotFile, gitlabgit.ErrTooBig, gitlabgit.ErrNotText, gitlabgit.ErrUnsupportedHash, gitlabgit.ErrNotBase64} {
		err := fileFailure("a.bin", fmt.Errorf("read: a.bin %w", sentinel))

		require.ErrorIs(t, err, action.ErrLimit)
		require.ErrorIs(t, err, sentinel)
		require.ErrorContains(t, err, "a.bin")
	}

	other := errors.New("network down")
	err := fileFailure("a.yaml", other)

	require.ErrorIs(t, err, other)
	require.NotErrorIs(t, err, action.ErrLimit)
}

func TestApiFailure(t *testing.T) {
	t.Parallel()

	err := apiFailure("read the commit", "commit abc", &gitlabapi.StatusError{Code: http.StatusNotFound})
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "commit abc was not found in the project")

	err = apiFailure("read the commit", "commit abc", &gitlabapi.StatusError{Code: http.StatusInternalServerError})
	require.NotErrorIs(t, err, action.ErrLimit)
	require.NotErrorIs(t, err, action.ErrPermanent)

	err = apiFailure("read the commit", "commit abc", &gitlabapi.StatusError{Code: http.StatusForbidden})
	require.ErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrLimit)
}
