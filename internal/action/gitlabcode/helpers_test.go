package gitlabcode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Test values.
const (
	testProject    = "acme/platform/api"
	testProposalID = "0192d3a4-0000-7000-8000-0000000000a1"
	testUndoID     = "0192d3a4-0000-7000-8000-0000000000a2"
	testInvestID   = "0192d3a4-0000-7000-8000-0000000000c1"
	testBaseSHA    = "aaaa000000000000000000000000000000000001"
	testCommitSHA  = "cccc000000000000000000000000000000000003"
	testTokenValue = "glpat-test-token"
	testWebURL     = "https://gitlab.example.com/acme/platform/api/-/merge_requests/"

	testPath   = "src/billing/invoice.go"
	testBefore = "package billing\n\nfunc Total(a, b int) int {\n\treturn a - b\n}\n"
	testAfter  = "package billing\n\nfunc Total(a, b int) int {\n\treturn a + b\n}\n"
)

// testBranch and testUndoBranch are the branches the action makes for the
// proposal and for the undo.
var (
	testBranch     = "sidekick/" + testProposalID
	testUndoBranch = "sidekick/" + testUndoID
)

// testToken is the secret of the token in the tests.
var testToken = registry.SecretRef{Name: "code-change", Key: "token"}

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// notFound is GitLab's answer for something that does not exist.
var notFound = failed(http.StatusNotFound, "404 Not Found")

// bed is a test action with the fake GitLab it talks to.
type bed struct {
	code   *Code
	gitlab *fakehttp.Server
}

// newBed returns an action that talks to a fresh fake GitLab, with the limits
// of validLimits changed by mutate.
func newBed(t *testing.T, mutate func(l *Limits)) *bed {
	t.Helper()

	return newBedWithDB(t, mutate, nil)
}

// newBedWithDB is newBed for an action that records merge request states in
// dbtx, which may be nil.
func newBedWithDB(t *testing.T, mutate func(l *Limits), dbtx db.DBTX) *bed {
	t.Helper()

	gitlab := fakehttp.New(t)

	limits := validLimits()
	if mutate != nil {
		mutate(&limits)
	}

	secrets := registry.NewMockSecrets(gomock.NewController(t))
	secrets.EXPECT().Resolve(gomock.Any(), testToken).Return([]byte(testTokenValue+"\n"), nil).AnyTimes()

	code, err := newCode("code-mr", Config{BaseURL: gitlab.URL(), TokenSecret: testToken, Limits: limits}, registry.Deps{
		Secrets:   secrets,
		HTTP:      gitlab.Client(),
		PublicURL: "https://sidekick.example.com",
		DB:        dbtx,
	})
	require.NoError(t, err)

	return &bed{code: code, gitlab: gitlab}
}

// validLimits returns limits that pass the config check.
func validLimits() Limits {
	return Limits{
		Projects:     []string{testProject},
		Paths:        []string{"src/**", "cmd/**", "go.mod", ".github/**", "CODEOWNERS"},
		RefusedPaths: []string{"src/legacy/**"},
		Bounds:       gitchange.Bounds{MaxFiles: 3, MaxChangedLines: 40, BaseBranch: "main"},
		AllowDelete:  true,
	}
}

// node is an entry of the fake repository: a regular file (the default), a
// symlink or a submodule.
type node struct {
	text string
	// mode is the mode of the entry; empty means 100644.
	mode string
	// kind is the type of the entry; empty means blob.
	kind string
}

// repo is the fake repository: each key is the path of a file, a symlink or a
// submodule, and the directories above them exist.
type repo map[string]node

// regular returns a regular file with the text.
func regular(text string) node {
	return node{text: text}
}

// serveRepo makes the fake GitLab serve the repository at the base commit: the
// head of main, the listing of every directory and each regular file.
func (b *bed) serveRepo(r repo) {
	b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/branches/main"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, branchBody(testBaseSHA))
	})

	b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/tree"), func(w http.ResponseWriter, req *http.Request) {
		dir := req.URL.Query().Get("path")
		if dir == "" {
			dir = "."
		}

		entries := r.listing(dir)
		if entries == nil && dir != "." {
			writeJSON(w, http.StatusNotFound, `{"message":"404 Tree Not Found"}`)

			return
		}

		writeJSON(w, http.StatusOK, "["+strings.Join(entries, ",")+"]")
	})

	for p, n := range r {
		if n.kind != "" || n.mode == gitlabSymlink {
			continue
		}

		b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/files/"+p), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, fileBody(p, n.text))
		})
	}
}

// listing returns the JSON entries of a directory, sorted by path, or nil when
// the directory does not exist.
func (r repo) listing(dir string) []string {
	seen := map[string]string{}

	for p, n := range r {
		for cur := p; cur != "." && cur != "/"; cur = path.Dir(cur) {
			if path.Dir(cur) != dir {
				continue
			}

			switch {
			case cur != p:
				seen[cur] = treeEntry(cur, "tree", "040000")
			case n.kind != "":
				seen[cur] = treeEntry(cur, n.kind, n.mode)
			case n.mode != "":
				seen[cur] = treeEntry(cur, "blob", n.mode)
			default:
				seen[cur] = treeEntry(cur, "blob", "100644")
			}
		}
	}

	if len(seen) == 0 {
		return nil
	}

	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, seen[p])
	}

	return out
}

// gitlabSymlink is the mode GitLab gives a symbolic link.
const gitlabSymlink = "120000"

// writeJSON writes a JSON answer.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// reply returns a JSON answer with the given body and status 200.
func reply(body string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(body)}
}

// replyStatus returns a JSON answer with a status.
func replyStatus(status int, body string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: jsonHeaders, Body: []byte(body)}
}

// failed returns a GitLab error answer.
func failed(status int, message string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: jsonHeaders, Body: fmt.Appendf(nil, `{"message":%q}`, message)}
}

// apiPath returns the decoded path of a route below the test project, as the
// fake server matches it.
func apiPath(rest string) string {
	return "/api/v4/projects/" + testProject + "/" + rest
}

// branchBody is the answer to reading a branch.
func branchBody(sha string) string {
	return fmt.Sprintf(`{"name":"x","commit":{"id":%q}}`, sha)
}

// fileBody is the answer to reading a file, with the blob ID of its content.
func fileBody(p, text string) string {
	return fmt.Sprintf(`{"file_name":%q,"file_path":%q,"size":%d,"encoding":"base64","content":%q,"blob_id":%q,"commit_id":%q}`,
		path.Base(p), p, len(text), base64.StdEncoding.EncodeToString([]byte(text)), gitchange.BlobSHA(text), testBaseSHA)
}

// blobBody is the answer to reading a blob.
func blobBody(sha, text string) string {
	return fmt.Sprintf(`{"size":%d,"encoding":"base64","content":%q,"sha":%q}`, len(text), base64.StdEncoding.EncodeToString([]byte(text)), sha)
}

// treeEntry is one entry of a directory listing.
func treeEntry(p, kind, mode string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"type":%q,"path":%q,"mode":%q}`, gitchange.BlobSHA(p), path.Base(p), kind, p, mode)
}

// mergeRequestBody is a merge request as GitLab describes it.
func mergeRequestBody(iid int, state string, draft bool) string {
	return mergeRequestBodyFrom(iid, state, draft, testBranch)
}

// mergeRequestBodyFrom is mergeRequestBody for a merge request from a branch.
func mergeRequestBodyFrom(iid int, state string, draft bool, branch string) string {
	merged, closed := "null", "null"

	switch state {
	case "merged":
		merged = `"2026-11-03T10:00:00Z"`
	case "closed":
		closed = `"2026-11-03T10:00:00Z"`
	}

	return fmt.Sprintf(`{"iid":%d,"state":%q,"draft":%t,"web_url":"%s%d","sha":%q,"source_branch":%q,"target_branch":"main","merged_at":%s,"closed_at":%s}`,
		iid, state, draft, testWebURL, iid, testCommitSHA, branch, merged, closed)
}

// listOf is the list of merge requests with the merge requests given.
func listOf(bodies ...string) string {
	return "[" + strings.Join(bodies, ",") + "]"
}

// handleMergeRequests makes the fake GitLab answer the list of merge requests
// by the source branch it asks for; a branch that is not in lists has none.
func (b *bed) handleMergeRequests(lists map[string]string) {
	b.gitlab.HandleFunc(http.MethodGet, apiPath("merge_requests"), func(w http.ResponseWriter, r *http.Request) {
		body, ok := lists[r.URL.Query().Get("source_branch")]
		if !ok {
			body = "[]"
		}

		writeJSON(w, http.StatusOK, body)
	})
}

// expectNoBranch makes the fake GitLab say that the branch of the proposal
// does not exist yet.
func (b *bed) expectNoBranch() {
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), notFound)
}

// expectBranch makes the fake GitLab say that the branch of the proposal
// exists.
func (b *bed) expectBranch() {
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testBranch), reply(branchBody(testCommitSHA)))
}

// expectCommit makes the fake GitLab accept the commit that makes the branch.
func (b *bed) expectCommit() {
	b.gitlab.Handle(http.MethodPost, apiPath("repository/commits"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"id":%q}`, testCommitSHA)))
}

// expectOpen makes the fake GitLab accept the merge request.
func (b *bed) expectOpen(iid int, draft bool) {
	b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBody(iid, "opened", draft)))
}

// text returns a pointer to s.
func text(s string) *string {
	return &s
}

// validParams returns parameters that pass the limits, with no files.
func validParams() Params {
	return Params{Project: testProject, Edit: gitchange.Edit{
		BaseBranch: "main",
		Title:      "Fix the total of an invoice",
		Body:       "The total subtracted the second amount.",
	}}
}

// codeParams returns parameters that change the test file.
func codeParams() Params {
	p := validParams()
	p.Files = []gitchange.FileChange{{Path: testPath, Content: text(testAfter)}}

	return p
}

// invocation returns the invocation of a proposal with params.
func invocation(params Params) action.Invocation {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}

	return action.Invocation{
		ProposalID:      testProposalID,
		InvestigationID: testInvestID,
		Params:          raw,
		IdempotencyKey:  action.IdempotencyKey(testProposalID),
		ApprovedBy:      "user-1",
	}
}

// rawParams returns the parameters as JSON.
func rawParams(t *testing.T, p Params) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	return raw
}

// sent returns the method and decoded path of every request the fake
// received, in order.
func sent(s *fakehttp.Server) []string {
	var out []string

	for _, r := range s.Requests() {
		out = append(out, r.Method+" "+strings.TrimPrefix(r.Path, "/api/v4/projects/"+testProject+"/"))
	}

	return out
}

// writes returns the requests the fake received that are not reads.
func writes(s *fakehttp.Server) []string {
	var out []string

	for _, call := range sent(s) {
		if !strings.HasPrefix(call, "GET ") {
			out = append(out, call)
		}
	}

	return out
}

// bodyOf returns the JSON body of the first request to method and path.
func bodyOf(t *testing.T, s *fakehttp.Server, method, p string) map[string]any {
	t.Helper()

	for _, r := range s.Requests() {
		if r.Method == method && r.Path == p {
			var out map[string]any

			require.NoError(t, json.Unmarshal(r.Body, &out))

			return out
		}
	}

	require.Failf(t, "no such request", "%s %s", method, p)

	return nil
}

// expectMissing makes the fake GitLab answer 404 to the read of each path as a
// file, as it does for a path that is no file.
func (b *bed) expectMissing(paths ...string) {
	for _, p := range paths {
		b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/files/"+p), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, `{"message":"404 File Not Found"}`)
		})
	}
}

// expectBlob makes the fake GitLab serve the blob of the text.
func (b *bed) expectBlob(text string) {
	sha := gitchange.BlobSHA(text)

	b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/blobs/"+sha), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, blobBody(sha, text))
	})
}

// noContent is GitLab's answer to a delete that worked.
var noContent = fakehttp.Response{Status: http.StatusNoContent}

// blobOf returns the blob ID of text.
func blobOf(text string) string {
	return gitchange.BlobSHA(text)
}

// limits changes the limits of the action under test.
func (b *bed) limits(mutate func(l *Limits)) {
	mutate(&b.code.limits)
}
