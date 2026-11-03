package gitlabmr

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// allowedCalls are the calls the action makes to GitLab, each as a method and
// the path below the project with what changes from call to call left as a
// name. The action never assigns, asks for a review, approves, sets
// auto-merge or squash, or merges: a call of another kind fails
// TestCallsAreOnTheList until the list is changed on purpose, with a reason.
var allowedCalls = []string{
	"DELETE repository/branches/:branch",
	"GET merge_requests",
	"GET repository/blobs/:sha",
	"GET repository/branches/:branch",
	"GET repository/files/:path",
	"GET repository/tree",
	"POST merge_requests",
	"POST repository/commits",
	"PUT merge_requests/:iid",
}

// allowedBodyKeys are the keys of the JSON body the action may send with a
// call that writes.
var allowedBodyKeys = map[string][]string{
	"POST merge_requests":                {"description", "source_branch", "target_branch", "title"},
	"POST repository/commits":            {"actions", "branch", "commit_message", "start_sha"},
	"PUT merge_requests/:iid":            {"state_event"},
	"DELETE repository/branches/:branch": nil,
}

// callKind returns the kind of a request to the fake GitLab, as in
// allowedCalls.
func callKind(r fakehttp.Request) string {
	rest := strings.TrimPrefix(r.Path, "/api/v4/projects/"+testProject+"/")

	switch {
	case strings.HasPrefix(rest, "repository/branches/"):
		rest = "repository/branches/:branch"
	case strings.HasPrefix(rest, "repository/files/"):
		rest = "repository/files/:path"
	case strings.HasPrefix(rest, "repository/blobs/"):
		rest = "repository/blobs/:sha"
	case strings.HasPrefix(rest, "merge_requests/"):
		rest = "merge_requests/:iid"
	}

	return r.Method + " " + rest
}

func TestCallsAreOnTheList(t *testing.T) {
	t.Parallel()

	scenarios := map[string]func(t *testing.T) *bed{
		"propose": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.expectBase()
			b.expectFile(testRulesPath, testRulesBefore)
			b.expectBase()
			b.expectFile(testRulesPath, testRulesBefore)
			b.validatorPasses(1)

			require.NoError(t, b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params))

			_, err := b.mr.Snapshot(t.Context(), invocation(rulesParams()).Params)
			require.NoError(t, err)

			return b
		},
		"execute a change of every kind": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.handleMergeRequests(nil)
			b.expectNoBranch()
			b.expectBase()
			b.expectFile("prometheus/rules/old.yaml", "groups: []\n")
			b.expectNoFile("prometheus/rules/new.yaml")
			b.expectFile(testRulesPath, testRulesBefore)
			b.validatorPasses(1)
			b.expectCommit()
			b.expectOpen(3, true)

			params := validParams()
			params.Files = []gitchange.FileChange{
				{Path: "prometheus/rules/old.yaml", Delete: true},
				{Path: "prometheus/rules/new.yaml", Content: text("groups: []\nx: 1\n")},
				{Path: testRulesPath, Content: text(testRulesAfter)},
			}

			_, err := b.mr.Execute(t.Context(), invocation(params))
			require.NoError(t, err)

			return b
		},
		"execute again": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.handleMergeRequests(map[string]string{testBranch: openList(12)})

			_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
			require.NoError(t, err)

			return b
		},
		"execute a branch with no merge request": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.handleMergeRequests(nil)
			b.expectBranch()
			b.expectOpen(13, true)

			_, err := b.mr.Execute(t.Context(), invocation(rulesParams()))
			require.NoError(t, err)

			return b
		},
		"undo an open merge request": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.handleMergeRequests(map[string]string{testBranch: openList(12)})
			b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(mergeRequestBody(12, "closed", true)))
			b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})

			_, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
			require.NoError(t, err)

			return b
		},
		"undo a merged merge request": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.handleMergeRequests(map[string]string{testBranch: mergedList(12)})
			b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
			b.expectBase()
			b.expectMergedFiles()
			b.expectBeforeBlobs()
			b.expectCommit()
			b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(30, "opened", true, testUndoBranch)))

			_, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
			require.NoError(t, err)

			return b
		},
		"the daily check": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.handleMergeRequests(map[string]string{testBranch: mergedList(12)})

			_, _, err := b.mr.PullRequestState(t.Context(), json.RawMessage(`{"project":"`+testProject+`"}`), testBranch)
			require.NoError(t, err)

			return b
		},
		"read a blob for the line diff": func(t *testing.T) *bed {
			t.Helper()

			b := newBed(t, nil)
			b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+testBlobSHA), reply(blobBody(testBlobSHA, "hello\n")))

			raw, err := json.Marshal(validParams())
			require.NoError(t, err)

			_, err = b.mr.ReadBlob(t.Context(), raw, testBlobSHA)
			require.NoError(t, err)

			return b
		},
	}

	var (
		mu   sync.Mutex
		seen = map[string]bool{}
	)

	// The group is not parallel on purpose: t.Run returns when its parallel
	// subtests are done, and the check below needs what they saw.
	t.Run("each scenario", func(t *testing.T) { //nolint:paralleltest // see the comment above.
		for name, run := range scenarios {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				b := run(t)

				for _, r := range b.gitlab.Requests() {
					kind := callKind(r)

					require.Contains(t, allowedCalls, kind, "the action made a call that is not on the list: %s %s", r.Method, r.Path)

					if keys, written := allowedBodyKeys[kind]; written && len(r.Body) > 0 {
						var body map[string]any

						require.NoError(t, json.Unmarshal(r.Body, &body))

						for key := range body {
							require.Contains(t, keys, key, "%s sent a key that is not on the list", kind)
						}
					}

					mu.Lock()
					seen[kind] = true
					mu.Unlock()
				}
			})
		}
	})

	mu.Lock()
	defer mu.Unlock()

	used := make([]string, 0, len(seen))
	for kind := range seen {
		used = append(used, kind)
	}

	slices.Sort(used)
	require.Equal(t, allowedCalls, used, "every call on the list is made by some scenario, and no other")
}
