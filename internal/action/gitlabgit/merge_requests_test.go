package gitlabgit

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestExisting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(b *bed)
		wantIID    int
		wantBranch bool
		wantCalls  []string
		wantErr    string
	}{
		{
			name: "a merge request, so the branch is not read",
			setup: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("["+mergeRequestBody(7, "merged", true)+"]"))
			},
			wantIID:   7,
			wantCalls: []string{"GET merge_requests"},
		},
		{
			name: "a branch with no merge request",
			setup: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("[]"))
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), reply(branchBody(testHeadSHA)))
			},
			wantBranch: true,
			wantCalls:  []string{"GET merge_requests", "GET repository/branches/" + testBranch},
		},
		{
			name: "nothing",
			setup: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("[]"))
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), notFound)
			},
			wantCalls: []string{"GET merge_requests", "GET repository/branches/" + testBranch},
		},
		{
			name: "the list fails",
			setup: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), failed(http.StatusInternalServerError, "boom"))
			},
			wantCalls: []string{"GET merge_requests"},
			wantErr:   "look for the merge request of an earlier attempt",
		},
		{
			name: "the branch read fails",
			setup: func(b *bed) {
				b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("[]"))
				b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), failed(http.StatusInternalServerError, "boom"))
			},
			wantCalls: []string{"GET merge_requests", "GET repository/branches/" + testBranch},
			wantErr:   "look for the branch of an earlier attempt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t)
			tt.setup(b)

			mr, branchExists, err := Existing(t.Context(), b.client, testProject, testBranch)
			require.Equal(t, tt.wantCalls, sent(b.gitlab))

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantBranch, branchExists)

			if tt.wantIID == 0 {
				require.Nil(t, mr)

				return
			}

			require.Equal(t, tt.wantIID, mr.IID)
		})
	}
}

func TestMergeRequestFromPrefersOpenThenMerged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		list  string
		wantI int
	}{
		{name: "an open one over newer closed ones", list: "[" + mergeRequestBody(3, "closed", true) + "," + mergeRequestBody(2, "opened", true) + "]", wantI: 2},
		{name: "a locked one counts as open", list: "[" + mergeRequestBody(3, "merged", true) + "," + mergeRequestBody(2, "locked", true) + "]", wantI: 2},
		{name: "a merged one over a closed one", list: "[" + mergeRequestBody(3, "closed", true) + "," + mergeRequestBody(2, "merged", true) + "]", wantI: 2},
		{name: "the newest of equals", list: "[" + mergeRequestBody(3, "closed", true) + "," + mergeRequestBody(2, "closed", true) + "]", wantI: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t)
			b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply(tt.list))

			mr, err := MergeRequestFrom(t.Context(), b.client, testProject, testBranch)
			require.NoError(t, err)
			require.Equal(t, tt.wantI, mr.IID)

			q := b.gitlab.Requests()[0].Query
			require.Equal(t, testBranch, q.Get("source_branch"))
			require.Equal(t, "all", q.Get("state"))
		})
	}
}

func TestCommitTo(t *testing.T) {
	t.Parallel()

	req := CommitRequest{Branch: testBranch, StartSHA: testBaseSHA, CommitMessage: "m", Actions: []CommitAction{{Action: CommitDelete, FilePath: "a.go"}}}

	tests := []struct {
		name         string
		commit       []fakehttp.Response
		branchExists bool
		want         string
		wantErr      error
	}{
		{name: "the commit is made", commit: []fakehttp.Response{reply(`{"id":"` + testHeadSHA + `"}`)}, want: testHeadSHA},
		{name: "GitLab refuses but the branch is there, made by another attempt", commit: []fakehttp.Response{failed(http.StatusBadRequest, "A branch called x already exists")}, branchExists: true},
		{name: "GitLab refuses and there is no branch", commit: []fakehttp.Response{failed(http.StatusBadRequest, "A file with this name doesn't exist")}, wantErr: action.ErrPermanent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t)
			b.gitlab.Handle(http.MethodPost, apiPath("repository/commits"), tt.commit...)

			if tt.want == "" {
				if tt.branchExists {
					b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), reply(branchBody(testHeadSHA)))
				} else {
					b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), notFound)
				}
			}

			got, err := CommitTo(t.Context(), b.client, testProject, req)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOpenMergeRequest(t *testing.T) {
	t.Parallel()

	req := CreateRequest{SourceBranch: testBranch, TargetBranch: "main", Title: "Draft: x", Description: "d"}

	t.Run("a merge request is made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), reply(mergeRequestBody(5, "opened", true)))

		mr, err := OpenMergeRequest(t.Context(), b.client, testProject, req)
		require.NoError(t, err)
		require.Equal(t, 5, mr.IID)
		require.Equal(t, testBranch, mr.SourceBranch)
		require.Equal(t, "main", mr.TargetBranch)
	})

	t.Run("another attempt made it a moment ago", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "Another open merge request already exists"))
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("["+mergeRequestBody(6, "opened", true)+"]"))

		mr, err := OpenMergeRequest(t.Context(), b.client, testProject, req)
		require.NoError(t, err)
		require.Equal(t, 6, mr.IID)
	})

	t.Run("a conflict and no merge request", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), failed(http.StatusConflict, "x"))
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("[]"))

		_, err := OpenMergeRequest(t.Context(), b.client, testProject, req)
		require.ErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("GitLab does not say which merge request it made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), reply(`{}`))

		_, err := OpenMergeRequest(t.Context(), b.client, testProject, req)
		require.ErrorIs(t, err, ErrNoIID)
	})
}

func TestCloseMergeRequestAndDeleteBranch(t *testing.T) {
	t.Parallel()

	t.Run("a merge request is closed with nothing else in the body", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/9"), reply(mergeRequestBody(9, "closed", true)))

		mr, err := CloseMergeRequest(t.Context(), b.client, testProject, 9)
		require.NoError(t, err)
		require.Equal(t, gitlabClosed, mr.State)
		require.JSONEq(t, `{"state_event":"close"}`, string(b.gitlab.Requests()[0].Body))
	})

	t.Run("a branch that is gone is not an error", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), notFound)

		require.NoError(t, DeleteBranch(t.Context(), b.client, testProject, testBranch))
	})

	t.Run("a server error is tried again later", func(t *testing.T) {
		t.Parallel()

		b := newBed(t)
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), failed(http.StatusInternalServerError, "boom"))

		err := DeleteBranch(t.Context(), b.client, testProject, testBranch)
		require.Error(t, err)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})
}

func TestStatusOf(t *testing.T) {
	t.Parallel()

	b := newBed(t)
	b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), reply("["+mergeRequestBody(4, "merged", false)+"]"), reply("[]"))

	st, err := Status(t.Context(), b.client, testProject, testBranch)
	require.NoError(t, err)
	require.Equal(t, 4, st.Number)
	require.Equal(t, gitchange.StateMerged, st.State)
	require.NotNil(t, st.MergedAt)
	require.Nil(t, st.ClosedAt)

	none, err := Status(t.Context(), b.client, testProject, testBranch)
	require.NoError(t, err)
	require.Nil(t, none)
}

func TestStateOf(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"opened": gitchange.StateOpen,
		"locked": gitchange.StateOpen,
		"merged": gitchange.StateMerged,
		"closed": gitchange.StateClosed,
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, want, StateOf(&MergeRequest{State: in}))
		})
	}
}

func TestProjectOfParams(t *testing.T) {
	t.Parallel()

	got, err := ProjectOfParams([]byte(`{"project":"acme/platform/api","files":[]}`))
	require.NoError(t, err)
	require.Equal(t, testProject, got)

	for _, bad := range []string{`not json`, `{}`, `{"project":"../x"}`, `{"project":"a"}`} {
		_, err := ProjectOfParams([]byte(bad))
		require.ErrorIs(t, err, action.ErrPermanent, bad)
	}
}

func TestResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mr      MergeRequest
		how     string
		pushed  string
		summary string
		head    string
	}{
		{name: "opened", mr: MergeRequest{IID: 3, State: "opened", Draft: true, SHA: "s1"}, how: ResultOpened, summary: "Opened draft merge request " + testProject + "!3", head: "s1"},
		{name: "found open", mr: MergeRequest{IID: 3, State: "opened"}, how: ResultExisting, pushed: "p1", summary: "Found the already opened merge request " + testProject + "!3", head: "p1"},
		{name: "found merged", mr: MergeRequest{IID: 3, State: "merged", SHA: "s1"}, how: ResultExisting, summary: "Found merge request " + testProject + "!3, which has been merged since it was opened", head: "s1"},
		{name: "found closed", mr: MergeRequest{IID: 3, State: "closed", SHA: "s1"}, how: ResultExisting, summary: "Found merge request " + testProject + "!3, which has been closed since it was opened", head: "s1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Result(testProject, &tt.mr, tt.how, tt.pushed)
			require.Equal(t, tt.summary, got.Summary)
			require.Equal(t, int64(3), got.PullRequest.Number)
			require.Equal(t, tt.head, got.PullRequest.HeadSHA)
		})
	}
}

func TestNewMetricsKeepsTheNames(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	m, err := NewMetrics(reg, "gitlabx")
	require.NoError(t, err)

	m.MergeRequest(ResultOpened)
	m.StatusCheck("open")

	families, err := reg.Gather()
	require.NoError(t, err)

	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}

	require.ElementsMatch(t, []string{"sidekick_gitlabx_merge_requests_total", "sidekick_gitlabx_status_checks_total"}, names)

	_, err = NewMetrics(reg, "gitlabx")
	require.NoError(t, err, "a second instance shares the collectors")
}

func TestUndoBranch(t *testing.T) {
	t.Parallel()

	undone := "0192d3a4-0000-7000-8000-0000000000a2"

	tests := []struct {
		name    string
		inv     action.Invocation
		want    string
		wantErr error
	}{
		{name: "a valid undo", inv: action.Invocation{UndoOf: testProposalID, IdempotencyKey: "sidekick/" + undone}, want: "sidekick/" + testProposalID},
		{name: "no proposal to undo", inv: action.Invocation{IdempotencyKey: "sidekick/" + undone}, wantErr: ErrNoUndoOf},
		{name: "no key", inv: action.Invocation{UndoOf: testProposalID}, wantErr: ErrNoBranch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := UndoBranch(tt.inv)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrPermanent)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
