package githubrevert

import (
	"net/http"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// callKinds are the kinds of call the action makes to GitHub, by method and
// the shape of the path. A new kind, such as merging, requesting a review,
// assigning, turning on auto-merge or calling GraphQL, fails the test below
// until someone adds it here on purpose.
var callKinds = []struct {
	method  string
	pattern *regexp.Regexp
	name    string
}{
	{http.MethodPost, regexp.MustCompile(`^/app/installations/\d+/access_tokens$`), "make an installation token"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/git/ref/heads/.+$`), "read a branch"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/git/commits/[0-9a-f]{40}$`), "read a commit"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/git/trees/[0-9a-f]{40}$`), "read a tree"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/git/blobs/[0-9a-f]{40}$`), "read a blob"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/compare/[^/]+$`), "compare two commits"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/pulls/\d+$`), "read a pull request"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/pulls/\d+/files$`), "list the files of a pull request"},
	{http.MethodGet, regexp.MustCompile(`^/repos/acme/api/pulls$`), "list pull requests from a branch"},
	{http.MethodPost, regexp.MustCompile(`^/repos/acme/api/git/trees$`), "write a tree"},
	{http.MethodPost, regexp.MustCompile(`^/repos/acme/api/git/commits$`), "write a commit"},
	{http.MethodPost, regexp.MustCompile(`^/repos/acme/api/git/refs$`), "make a branch"},
	{http.MethodPost, regexp.MustCompile(`^/repos/acme/api/pulls$`), "open a pull request"},
	{http.MethodPatch, regexp.MustCompile(`^/repos/acme/api/pulls/\d+$`), "close a pull request"},
	{http.MethodDelete, regexp.MustCompile(`^/repos/acme/api/git/refs/heads/.+$`), "delete a branch"},
}

// kindOf returns the name of the kind of call, or "" for one the action must
// not make.
func kindOf(method, path string) string {
	for _, k := range callKinds {
		if k.method == method && k.pattern.MatchString(path) {
			return k.name
		}
	}

	return ""
}

// assertKnownCalls fails the test for a request of a kind the action does not
// make, and returns the kinds it saw.
func assertKnownCalls(t *testing.T, s *fakehttp.Server) []string {
	t.Helper()

	var seen []string

	for _, r := range s.Requests() {
		kind := kindOf(r.Method, r.Path)
		require.NotEmpty(t, kind, "the action made a kind of call to GitHub that is not on the list: %s %s", r.Method, r.Path)

		if !slices.Contains(seen, kind) {
			seen = append(seen, kind)
		}
	}

	return seen
}

func TestTheActionMakesOnlyTheCallsOnTheList(t *testing.T) {
	t.Parallel()

	t.Run("a revert of a commit, a repeat, and an undo that closes", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		h := b.standard()
		b.onMain(h.bad)
		b.w.serve()

		params := byCommit(h.bad)
		snap := b.snapshotOf(t, params)
		inv := invocation(t, params, &snap)

		_, err := b.rev.Execute(t.Context(), inv)
		require.NoError(t, err)

		_, err = b.rev.Execute(t.Context(), inv)
		require.NoError(t, err)

		closePull(b, createdNumber)
		b.w.gh.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})

		_, err = b.rev.Undo(t.Context(), undoInvocation(t, params), snap, false)
		require.NoError(t, err)

		seen := assertKnownCalls(t, b.w.gh)
		require.Contains(t, seen, "open a pull request")
		require.Contains(t, seen, "close a pull request")
		require.NotContains(t, seen, "list the files of a pull request")

		pull := bodyOf(t, b.w.gh, http.MethodPost, repoPath("pulls"))
		requireDraft(t, pull)
	})

	t.Run("a revert of a squashed pull request and an undo that puts the files back", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		h := b.standard()
		b.w.setBranch("main", h.bad)
		b.w.pull(merged(h.bad, 3, readmePath, configPath, newFilePath))
		b.w.compare(h.first, prHead, "ahead", h.first)
		b.w.serve()

		params := byPullRequest(7)
		snap := b.snapshotOf(t, params)

		_, err := b.rev.Execute(t.Context(), invocation(t, params, &snap))
		require.NoError(t, err)

		head := b.w.commit([]string{h.bad}, "Merge the revert", testNow, map[string]wfile{
			readmePath: txt(readmeText), configPath: txt(configBefore), limitsPath: txt(limitsText),
		})
		b.w.setBranch("main", head)
		b.w.setOwnState(testBranch, "closed", true)
		b.w.serve()

		_, err = b.rev.Undo(t.Context(), undoInvocation(t, params), snap, false)
		require.NoError(t, err)

		seen := assertKnownCalls(t, b.w.gh)
		require.Contains(t, seen, "list the files of a pull request")
		require.Contains(t, seen, "compare two commits")
	})
}

func TestKindOfRefusesWhatTheActionMustNotDo(t *testing.T) {
	t.Parallel()

	for _, call := range []struct{ method, path string }{
		{http.MethodPut, "/repos/acme/api/pulls/7/merge"},
		{http.MethodPost, "/repos/acme/api/pulls/7/requested_reviewers"},
		{http.MethodPost, "/repos/acme/api/issues/7/assignees"},
		{http.MethodPost, "/graphql"},
		{http.MethodPatch, "/repos/acme/api/git/refs/heads/main"},
		{http.MethodPost, "/repos/acme/api/issues/7/comments"},
		{http.MethodDelete, "/repos/acme/api/pulls/7"},
	} {
		require.Empty(t, kindOf(call.method, call.path), "%s %s", call.method, call.path)
	}
}
