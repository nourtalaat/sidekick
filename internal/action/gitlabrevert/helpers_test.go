package gitlabrevert

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Test values.
const (
	testProject    = "acme/platform/deploy"
	testProposalID = "0192d3a4-0000-7000-8000-0000000000a1"
	testUndoID     = "0192d3a4-0000-7000-8000-0000000000a2"
	testInvestID   = "0192d3a4-0000-7000-8000-0000000000c1"
	testHead       = "aaaa000000000000000000000000000000000001"
	testParent     = "bbbb000000000000000000000000000000000002"
	testCommit     = "cccc000000000000000000000000000000000003"
	testRevertSHA  = "dddd000000000000000000000000000000000004"
	testTokenValue = "glpat-test-token"
	testPath       = "deploy/values.yaml"
	testBefore     = "replicas: 2\nimage: api:1\n"
	testAfter      = "replicas: 8\nimage: api:2\n"
	testWebURL     = "https://gitlab.example.com/acme/platform/deploy/-/merge_requests/"
	firstNewIID    = 100
)

var (
	// testNow is the time the action believes it is.
	testNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	// testBranch and testUndoBranch are the branches the tests expect.
	testBranch     = "sidekick/" + testProposalID
	testUndoBranch = "sidekick/" + testUndoID

	jsonHeaders = map[string]string{"Content-Type": "application/json"}
)

// fakeMR is a merge request the fake GitLab holds.
type fakeMR struct {
	IID         int
	State       string
	Draft       bool
	Source      string
	Description string
}

// world is a fake GitLab with one project: the repository, the branches of the
// action, and the merge requests it makes. Its handlers run on the server's
// goroutines, so its state is behind a mutex.
type world struct {
	t   *testing.T
	srv *fakehttp.Server

	mu    sync.Mutex
	files map[string]map[string]string
	// branches are the branches of the proposals that exist.
	branches map[string]bool
	// ahead is how many commits the branch of the proposal has that the base
	// branch lacks: one once the revert call succeeded.
	ahead int
	// revertMessage, when set, makes the revert call answer 400 with it.
	revertMessage string
	// notDraft makes the fake ignore the draft title, as a GitLab that never
	// makes drafts would.
	notDraft bool
	mrs      []*fakeMR
	nextIID  int
}

// bed is a test action with the fake GitLab it talks to.
type bed struct {
	rv *Revert
	w  *world
}

// newBed returns an action that talks to a fresh fake GitLab, with the limits
// of validLimits changed by mutate. The fake serves the standard commit.
func newBed(t *testing.T, mutate func(l *Limits)) *bed {
	t.Helper()

	return newBedWithDB(t, mutate, nil)
}

// newBedWithDB is newBed for an action that records merge request states in
// dbtx, which may be nil.
func newBedWithDB(t *testing.T, mutate func(l *Limits), dbtx db.DBTX) *bed {
	t.Helper()

	srv := fakehttp.New(t)

	limits := validLimits()
	if mutate != nil {
		mutate(&limits)
	}

	secrets := registry.NewMockSecrets(gomock.NewController(t))
	secrets.EXPECT().Resolve(gomock.Any(), testToken).Return([]byte(testTokenValue+"\n"), nil).AnyTimes()

	rv, err := newRevert("revert", Config{BaseURL: srv.URL(), TokenSecret: testToken, Limits: limits}, registry.Deps{
		Secrets:   secrets,
		HTTP:      srv.Client(),
		PublicURL: "https://sidekick.example.com",
		DB:        dbtx,
	})
	require.NoError(t, err)

	rv.now = func() time.Time { return testNow }

	w := &world{t: t, srv: srv, files: map[string]map[string]string{}, branches: map[string]bool{}, nextIID: firstNewIID}
	w.serve()

	return &bed{rv: rv, w: w}
}

// serve registers the routes that depend on the state of the world.
func (w *world) serve() {
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/tree"), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, "[]")
	})

	w.srv.HandleFunc(http.MethodGet, apiPath("repository/branches/main"), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, branchBody(testHead))
	})

	for _, branch := range []string{testBranch, testUndoBranch} {
		w.serveBranch(branch)
	}

	w.srv.HandleFunc(http.MethodPost, apiPath("repository/branches"), w.createBranch)
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/compare"), w.compare)
	w.srv.HandleFunc(http.MethodGet, apiPath("merge_requests"), w.listMergeRequests)
	w.srv.HandleFunc(http.MethodPost, apiPath("merge_requests"), w.createMergeRequest)
	w.srv.HandleFunc(http.MethodPost, apiPath("repository/commits"), w.commitFiles)
	w.srv.HandleFunc(http.MethodPost, apiPath("repository/commits/"+testCommit+"/revert"), w.revert)

	for iid := firstNewIID; iid < firstNewIID+3; iid++ {
		w.srv.HandleFunc(http.MethodPut, apiPath(fmt.Sprintf("merge_requests/%d", iid)), w.closeMergeRequest(iid))
	}
}

// serveBranch registers the read and the delete of a branch of a proposal.
func (w *world) serveBranch(branch string) {
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/branches/"+branch), func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()

		if !w.branches[branch] {
			writeJSON(rw, http.StatusNotFound, `{"message":"404 Branch Not Found"}`)

			return
		}

		writeJSON(rw, http.StatusOK, branchBody(testHead))
	})

	w.srv.HandleFunc(http.MethodDelete, apiPath("repository/branches/"+branch), func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()

		if !w.branches[branch] {
			writeJSON(rw, http.StatusNotFound, `{"message":"404 Branch Not Found"}`)

			return
		}

		delete(w.branches, branch)
		rw.WriteHeader(http.StatusNoContent)
	})
}

// createBranch answers the call that makes a branch.
func (w *world) createBranch(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
		Ref    string `json:"ref"`
	}

	w.decode(r, &req)

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.branches[req.Branch] {
		writeJSON(rw, http.StatusBadRequest, `{"message":"Branch already exists"}`)

		return
	}

	w.branches[req.Branch] = true
	writeJSON(rw, http.StatusCreated, branchBody(req.Ref))
}

// revert answers the revert call.
func (w *world) revert(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
	}

	w.decode(r, &req)

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.revertMessage != "" {
		writeJSON(rw, http.StatusBadRequest, fmt.Sprintf(`{"message":%q,"error_code":"conflict"}`, w.revertMessage))

		return
	}

	w.ahead = 1
	writeJSON(rw, http.StatusCreated, fmt.Sprintf(`{"id":%q}`, testRevertSHA))
}

// commitFiles answers the call that makes a commit, which also makes its
// branch.
func (w *world) commitFiles(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
	}

	w.decode(r, &req)

	w.mu.Lock()
	defer w.mu.Unlock()

	w.branches[req.Branch] = true
	writeJSON(rw, http.StatusCreated, fmt.Sprintf(`{"id":%q}`, testRevertSHA))
}

// compare answers the comparison of the base branch with the proposal's
// branch: the revert commit, once it is made.
func (w *world) compare(rw http.ResponseWriter, _ *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()

	commits := make([]string, w.ahead)
	for i := range commits {
		commits[i] = fmt.Sprintf(`{"id":%q}`, testRevertSHA)
	}

	writeJSON(rw, http.StatusOK, `{"commits":[`+strings.Join(commits, ",")+`]}`)
}

// listMergeRequests answers the list of merge requests by source branch.
func (w *world) listMergeRequests(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var out []string

	for _, mr := range w.mrs {
		if mr.Source == r.URL.Query().Get("source_branch") {
			out = append(out, mr.body())
		}
	}

	writeJSON(rw, http.StatusOK, "["+strings.Join(out, ",")+"]")
}

// createMergeRequest answers the call that opens a merge request.
func (w *world) createMergeRequest(rw http.ResponseWriter, r *http.Request) {
	var req gitlabgit.CreateRequest

	w.decode(r, &req)

	w.mu.Lock()
	defer w.mu.Unlock()

	mr := &fakeMR{
		IID:         w.nextIID,
		State:       "opened",
		Draft:       strings.HasPrefix(strings.ToLower(req.Title), "draft:") && !w.notDraft,
		Source:      req.SourceBranch,
		Description: req.Description,
	}
	w.nextIID++
	w.mrs = append(w.mrs, mr)

	writeJSON(rw, http.StatusCreated, mr.body())
}

// closeMergeRequest answers the call that closes the merge request iid.
func (w *world) closeMergeRequest(iid int) http.HandlerFunc {
	return func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()

		for _, mr := range w.mrs {
			if mr.IID == iid {
				mr.State = "closed"
				writeJSON(rw, http.StatusOK, mr.body())

				return
			}
		}

		writeJSON(rw, http.StatusNotFound, `{"message":"404 Not found"}`)
	}
}

// decode reads the JSON body of a request into out.
func (w *world) decode(r *http.Request, out any) {
	w.t.Helper()

	data, err := io.ReadAll(r.Body)
	require.NoError(w.t, err)
	require.NoError(w.t, json.Unmarshal(data, out))
}

// json returns the merge request as GitLab describes it.
func (m *fakeMR) body() string {
	return mergeRequestBody(m.IID, m.State, m.Draft, m.Source, m.Description, "", "")
}

// addMergeRequest puts a merge request that exists already in the world.
func (w *world) addMergeRequest(mr *fakeMR) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.mrs = append(w.mrs, mr)
}

// setBranch says whether the branch of a proposal exists.
func (w *world) setBranch(branch string, exists bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.branches[branch] = exists
}

// setAhead sets how many commits the branch of the proposal has that the base
// branch lacks.
func (w *world) setAhead(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ahead = n
}

// failRevert makes the revert call answer 400 with the message.
func (w *world) failRevert(message string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.revertMessage = message
}

// ignoreDraft makes the fake open merge requests that are not drafts.
func (w *world) ignoreDraft() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.notDraft = true
}

// state returns the merge request with the iid, or nil.
func (w *world) state(iid int) *fakeMR {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, mr := range w.mrs {
		if mr.IID == iid {
			copied := *mr

			return &copied
		}
	}

	return nil
}

// hasBranch reports whether the branch exists.
func (w *world) hasBranch(branch string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.branches[branch]
}

// file puts a file in the repository at the commit ref and serves it.
func (w *world) file(ref, path, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.files[ref] == nil {
		w.files[ref] = map[string]string{}
	}

	w.files[ref][path] = text

	w.srv.HandleFunc(http.MethodGet, apiPath("repository/files/"+path), w.serveFile(path))
}

// serveFile answers the read of a file at the ref in the query.
func (w *world) serveFile(path string) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		text, ok := w.files[r.URL.Query().Get("ref")][path]
		w.mu.Unlock()

		if !ok {
			writeJSON(rw, http.StatusNotFound, `{"message":"404 File Not Found"}`)

			return
		}

		writeJSON(rw, http.StatusOK, fileBody(path, text))
	}
}

// commit serves a commit with its parents and its date.
func (w *world) commit(sha string, parents []string, committed time.Time) {
	quoted, err := json.Marshal(parents)
	require.NoError(w.t, err)

	body := fmt.Sprintf(`{"id":%q,"parent_ids":%s,"committed_date":%q}`, sha, quoted, committed.Format(time.RFC3339))

	w.srv.HandleFunc(http.MethodGet, apiPath("repository/commits/"+sha), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, body)
	})
}

// missing makes GitLab answer 404 for a commit.
func (w *world) missing(sha string) {
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/commits/"+sha), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusNotFound, `{"message":"404 Commit Not Found"}`)
	})
}

// diff serves the diff of a commit.
func (w *world) diff(sha string, files ...string) {
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/commits/"+sha+"/diff"), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, "["+strings.Join(files, ",")+"]")
	})
}

// mergeBase serves the common ancestor of the commit and the base branch:
// the commit itself when onBase is true, and none when it is false.
func (w *world) mergeBase(onBase bool) {
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/merge_base"), func(rw http.ResponseWriter, r *http.Request) {
		if !onBase {
			writeJSON(rw, http.StatusNotFound, `{"message":"404 Merge Base Not Found"}`)

			return
		}

		writeJSON(rw, http.StatusOK, fmt.Sprintf(`{"id":%q}`, r.URL.Query()["refs[]"][0]))
	})
}

// mergeRequestsOf serves the merge requests that hold a commit.
func (w *world) mergeRequestsOf(sha string, mrs ...string) {
	w.srv.HandleFunc(http.MethodGet, apiPath("repository/commits/"+sha+"/merge_requests"), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, "["+strings.Join(mrs, ",")+"]")
	})
}

// mergeRequest serves a merge request by its IID.
func (w *world) mergeRequest(iid int, body string) {
	w.srv.HandleFunc(http.MethodGet, apiPath(fmt.Sprintf("merge_requests/%d", iid)), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, body)
	})
}

// mergeRequestCommits serves the commits of a merge request.
func (w *world) mergeRequestCommits(iid int, shas ...string) {
	refs := make([]string, len(shas))
	for i, sha := range shas {
		refs[i] = fmt.Sprintf(`{"id":%q}`, sha)
	}

	w.srv.HandleFunc(http.MethodGet, apiPath(fmt.Sprintf("merge_requests/%d/commits", iid)), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, "["+strings.Join(refs, ",")+"]")
	})
}

// standard makes the world hold the commit most tests revert: one file that the
// commit changed from testBefore to testAfter, which the head still has as the
// commit wrote it. The commit landed an hour ago.
func (w *world) standard() {
	w.commit(testCommit, []string{testParent}, testNow.Add(-time.Hour))
	w.mergeBase(true)
	w.mergeRequestsOf(testCommit)
	w.diff(testCommit, diffEntry(testPath, testPath, "100644", "100644"))
	w.file(testHead, testPath, testAfter)
	w.file(testCommit, testPath, testAfter)
	w.file(testParent, testPath, testBefore)
}

// apiPath returns the decoded path of a route below the test project, as the
// fake server matches it.
func apiPath(rest string) string {
	return "/api/v4/projects/" + testProject + "/" + rest
}

// writeJSON answers with a JSON body.
func writeJSON(rw http.ResponseWriter, status int, body string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_, _ = rw.Write([]byte(body))
}

// branchBody is the answer to reading a branch.
func branchBody(sha string) string {
	return fmt.Sprintf(`{"name":"x","commit":{"id":%q}}`, sha)
}

// fileBody is the answer to reading a file, with the blob ID of its content.
func fileBody(path, text string) string {
	return fmt.Sprintf(`{"file_name":%q,"file_path":%q,"size":%d,"encoding":"base64","content":%q,"blob_id":%q}`,
		path[strings.LastIndex(path, "/")+1:], path, len(text), base64.StdEncoding.EncodeToString([]byte(text)), gitchange.BlobSHA(text))
}

// blobBody is the answer to reading a blob.
func blobBody(text string) string {
	return fmt.Sprintf(`{"size":%d,"encoding":"base64","content":%q}`, len(text), base64.StdEncoding.EncodeToString([]byte(text)))
}

// diffEntry is one file of the diff of a commit.
func diffEntry(oldPath, newPath, aMode, bMode string) string {
	return fmt.Sprintf(`{"old_path":%q,"new_path":%q,"a_mode":%q,"b_mode":%q,"new_file":false,"deleted_file":false,"collapsed":false,"too_large":false}`,
		oldPath, newPath, aMode, bMode)
}

// mergeRequestBody is a merge request as GitLab describes it.
func mergeRequestBody(iid int, state string, draft bool, source, description, mergeSHA, squashSHA string) string {
	merged, closed := "null", "null"

	switch state {
	case "merged":
		merged = `"` + testNow.Add(-time.Hour).Format(time.RFC3339) + `"`
	case "closed":
		closed = `"` + testNow.Format(time.RFC3339) + `"`
	}

	return fmt.Sprintf(`{"iid":%d,"state":%q,"draft":%t,"web_url":"%s%d","sha":%q,"source_branch":%q,"target_branch":"main","description":%q,"merge_commit_sha":%q,"squash_commit_sha":%q,"merged_at":%s,"closed_at":%s}`,
		iid, state, draft, testWebURL, iid, testCommit, source, description, mergeSHA, squashSHA, merged, closed)
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

// commitParams returns parameters that revert the standard commit.
func commitParams() Params {
	return Params{Project: testProject, BaseBranch: "main", Commit: testCommit, Title: "Revert the new replica count", Body: "It overloads the database."}
}

// sent returns the method and path of every request the fake received, in
// order, below the project.
func sent(s *fakehttp.Server) []string {
	var out []string

	for _, r := range s.Requests() {
		out = append(out, r.Method+" "+strings.TrimPrefix(r.Path, "/api/v4/projects/"+testProject+"/"))
	}

	return out
}

// count returns how many requests of the fake were sent to method and path.
func count(s *fakehttp.Server, method, path string) int {
	n := 0

	for _, r := range s.Requests() {
		if r.Method == method && r.Path == apiPath(path) {
			n++
		}
	}

	return n
}

// bodyOf returns the JSON body of the first request to method and path.
func bodyOf(t *testing.T, s *fakehttp.Server, method, path string) map[string]any {
	t.Helper()

	for _, r := range s.Requests() {
		if r.Method == method && r.Path == apiPath(path) {
			var out map[string]any

			require.NoError(t, json.Unmarshal(r.Body, &out))

			return out
		}
	}

	require.Failf(t, "no such request", "%s %s", method, path)

	return nil
}
