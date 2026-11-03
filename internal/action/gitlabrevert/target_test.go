package gitlabrevert

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

const testOtherParent = "eeee000000000000000000000000000000000005"

func TestResolveCommit(t *testing.T) {
	t.Parallel()

	const trailer = "Why.\n\nSidekick-Revert: 0192d3a4-0000-7000-8000-0000000000ff"

	tests := []struct {
		name       string
		setup      func(w *world)
		wantErr    string
		wantParent string
	}{
		{name: "a commit on the base branch", wantParent: testParent},
		{name: "a merge commit is reverted against its first parent", setup: func(w *world) {
			w.commit(testCommit, []string{testParent, testOtherParent}, testNow.Add(-time.Hour))
		}, wantParent: testParent},
		{name: "a commit GitLab does not know", setup: func(w *world) { w.missing(testCommit) }, wantErr: "was not found"},
		{name: "a commit that is not on the base branch", setup: func(w *world) { w.mergeBase(false) }, wantErr: "is not on the base branch"},
		{name: "a commit at the age limit", setup: func(w *world) {
			w.commit(testCommit, []string{testParent}, testNow.Add(-72*time.Hour))
		}, wantParent: testParent},
		{name: "a commit just over the age limit", setup: func(w *world) {
			w.commit(testCommit, []string{testParent}, testNow.Add(-72*time.Hour-time.Minute))
		}, wantErr: "longer than limits.maxAge"},
		{name: "a commit with no date", setup: func(w *world) {
			w.commit(testCommit, []string{testParent}, time.Time{})
		}, wantErr: "gave no date"},
		{name: "a commit with no parent", setup: func(w *world) { w.commit(testCommit, nil, testNow) }, wantErr: "no parent commit"},
		{name: "a commit with three parents", setup: func(w *world) {
			w.commit(testCommit, []string{testParent, testOtherParent, testHead}, testNow)
		}, wantErr: "3 parents"},
		{name: "a commit held by a revert of this action", setup: func(w *world) {
			w.mergeRequestsOf(testCommit, mergeRequestBody(7, "merged", false, testBranch, trailer, testCommit, ""))
		}, wantErr: "revert made by this action"},
		{name: "a commit held by a merge request from another branch that quotes the trailer", setup: func(w *world) {
			w.mergeRequestsOf(testCommit, mergeRequestBody(7, "merged", false, "feature/x", trailer, testCommit, ""))
		}, wantParent: testParent},
		{name: "a commit held by a branch of this action without the trailer", setup: func(w *world) {
			w.mergeRequestsOf(testCommit, mergeRequestBody(7, "merged", false, testBranch, "Opened by someone.", testCommit, ""))
		}, wantParent: testParent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.w.standard()

			if tt.setup != nil {
				tt.setup(b.w)
			}

			got, err := b.rv.resolve(t.Context(), commitParams())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testParent, got.parent)
			require.Equal(t, testCommit, got.sha)
			require.Equal(t, "commit "+testCommit[:shortSHA], got.what)
		})
	}
}

func TestResolveMergeRequest(t *testing.T) {
	t.Parallel()

	const trailer = "Why.\n\nSidekick-Revert: 0192d3a4-0000-7000-8000-0000000000ff"

	merged := func(source, description, mergeSHA, squashSHA string) func(w *world) {
		return func(w *world) { w.mergeRequest(12, mergeRequestBody(12, "merged", false, source, description, mergeSHA, squashSHA)) }
	}

	tests := []struct {
		name    string
		setup   func(w *world)
		change  func(b *bed, p *Params)
		wantErr string
	}{
		{name: "a merge commit", setup: merged("feature/x", "", testCommit, "")},
		{name: "a squash commit", setup: merged("feature/x", "", "", testCommit)},
		{name: "a merge commit before a squash commit", setup: merged("feature/x", "", testCommit, testOtherParent)},
		{name: "a fast-forward merge of one commit", setup: func(w *world) {
			merged("feature/x", "", "", "")(w)
			w.mergeRequestCommits(12, testCommit)
		}},
		{name: "a fast-forward merge of several commits", setup: func(w *world) {
			merged("feature/x", "", "", "")(w)
			w.mergeRequestCommits(12, testCommit, testHead)
		}, wantErr: "fast-forward with several commits"},
		{name: "a fast-forward merge with no commit", setup: func(w *world) {
			merged("feature/x", "", "", "")(w)
			w.mergeRequestCommits(12)
		}, wantErr: "gives no commit"},
		{name: "a merge request that is open", setup: func(w *world) {
			w.mergeRequest(12, mergeRequestBody(12, "opened", false, "feature/x", "", "", ""))
		}, wantErr: "is not merged"},
		{name: "a merge request that was closed", setup: func(w *world) {
			w.mergeRequest(12, mergeRequestBody(12, "closed", false, "feature/x", "", "", ""))
		}, wantErr: "is not merged"},
		{name: "a merge request merged into another branch", setup: merged("feature/x", "", testCommit, ""), change: func(_ *bed, p *Params) {
			p.BaseBranch = "release"
		}, wantErr: `was merged into "main", not into the base branch "release"`},
		{name: "a merge request GitLab does not know", setup: func(w *world) {
			w.srv.HandleFunc(http.MethodGet, apiPath("merge_requests/12"), func(rw http.ResponseWriter, _ *http.Request) {
				writeJSON(rw, http.StatusNotFound, `{"message":"404 Not found"}`)
			})
		}, wantErr: "was not found"},
		{name: "a revert made by this action", setup: merged(testBranch, trailer, testCommit, ""), wantErr: "revert made by this action"},
		{name: "a merge request merged just over the age limit", setup: merged("feature/x", "", testCommit, ""), change: func(b *bed, _ *Params) {
			b.rv.now = func() time.Time { return testNow.Add(72*time.Hour + time.Minute) }
		}, wantErr: "longer than limits.maxAge"},
		{name: "a merge request merged at the age limit", setup: merged("feature/x", "", testCommit, ""), change: func(b *bed, _ *Params) {
			b.rv.now = func() time.Time { return testNow.Add(71 * time.Hour) }
		}},
		{name: "a merge commit with no parent", setup: func(w *world) {
			merged("feature/x", "", testCommit, "")(w)
			w.commit(testCommit, nil, testNow)
		}, wantErr: "no parent commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.w.standard()
			tt.setup(b.w)

			p := commitParams()
			p.Commit, p.MergeRequest = "", 12

			if tt.change != nil {
				tt.change(b, &p)
			}

			got, err := b.rv.resolve(t.Context(), p)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testCommit, got.sha)
			require.Equal(t, testParent, got.parent)
			require.Equal(t, "merge request !12", got.what)
		})
	}
}
