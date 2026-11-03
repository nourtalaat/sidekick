package gitlabmr

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Test values.
const (
	testProject     = "acme/platform/monitors"
	testProposalID  = "0192d3a4-0000-7000-8000-0000000000a1"
	testUndoID      = "0192d3a4-0000-7000-8000-0000000000a2"
	testInvestID    = "0192d3a4-0000-7000-8000-0000000000c1"
	testBaseSHA     = "aaaa000000000000000000000000000000000001"
	testCommitSHA   = "cccc000000000000000000000000000000000003"
	testTokenValue  = "glpat-test-token"
	testRulesSHA    = "1111111111111111111111111111111111111111"
	testOldSHA      = "2222222222222222222222222222222222222222"
	testRulesPath   = "prometheus/rules/checkout.yaml"
	testRulesBefore = "groups:\n- name: checkout\n  rules:\n  - alert: Slow\n    expr: rate(x[1m]) > 5\n    for: 1m\n"
	testRulesAfter  = "groups:\n- name: checkout\n  rules:\n  - alert: Slow\n    expr: rate(x[1m]) > 5\n    for: 10m\n"
	testWebURL      = "https://gitlab.example.com/acme/platform/monitors/-/merge_requests/"
)

// testBranch is the branch the tests expect the action to make.
var testBranch = "sidekick/" + testProposalID

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// notFound is GitLab's answer for something that does not exist.
var notFound = failed(http.StatusNotFound, "404 Not Found")

// bed is a test action with the fake GitLab and the fake validator it talks to.
type bed struct {
	mr        *MR
	gitlab    *fakehttp.Server
	validator *fakehttp.Server
}

// newBed returns an action that talks to fresh fake servers, with the limits
// of validLimits changed by mutate.
func newBed(t *testing.T, mutate func(l *Limits)) *bed {
	t.Helper()

	return newBedWithDB(t, mutate, nil)
}

// newBedWithDB is newBed for an action that records merge request states in
// dbtx, which may be nil.
func newBedWithDB(t *testing.T, mutate func(l *Limits), dbtx db.DBTX) *bed {
	t.Helper()

	gitlab, validator := fakehttp.New(t), fakehttp.New(t)

	limits := validLimits()
	if mutate != nil {
		mutate(&limits)
	}

	secrets := registry.NewMockSecrets(gomock.NewController(t))
	secrets.EXPECT().Resolve(gomock.Any(), testToken).Return([]byte(testTokenValue+"\n"), nil).AnyTimes()

	mr, err := newMR("monitor-mr", Config{BaseURL: gitlab.URL(), TokenSecret: testToken, Limits: limits}, registry.Deps{
		Secrets:      secrets,
		HTTP:         gitlab.Client(),
		ValidatorURL: validator.URL(),
		PublicURL:    "https://sidekick.example.com",
		DB:           dbtx,
	})
	require.NoError(t, err)

	return &bed{mr: mr, gitlab: gitlab, validator: validator}
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
func fileBody(path, text string) string {
	return fileBodyWithSHA(path, text, gitchange.BlobSHA(text))
}

// fileBodyWithSHA is fileBody for a file with a blob ID of the test's choice.
func fileBodyWithSHA(path, text, sha string) string {
	return fmt.Sprintf(`{"file_name":%q,"file_path":%q,"size":%d,"encoding":"base64","content":%q,"blob_id":%q,"commit_id":%q}`,
		path[strings.LastIndex(path, "/")+1:], path, len(text), base64.StdEncoding.EncodeToString([]byte(text)), sha, testBaseSHA)
}

// blobBody is the answer to reading a blob.
func blobBody(sha, text string) string {
	return fmt.Sprintf(`{"size":%d,"encoding":"base64","content":%q,"sha":%q}`, len(text), base64.StdEncoding.EncodeToString([]byte(text)), sha)
}

// treeEntry is one entry of a directory listing.
func treeEntry(path, kind, mode string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"type":%q,"path":%q,"mode":%q}`, gitchange.BlobSHA(path), path[strings.LastIndex(path, "/")+1:], kind, path, mode)
}

// mergeRequestBody is a merge request as GitLab describes it.
func mergeRequestBody(iid int, state string, draft bool) string {
	return fmt.Sprintf(`{"iid":%d,"state":%q,"draft":%t,"web_url":"%s%d","sha":%q,"source_branch":%q,"target_branch":"main","merged_at":%s,"closed_at":%s}`,
		iid, state, draft, testWebURL, iid, testCommitSHA, testBranch, timeOrNull(state == "merged"), timeOrNull(state == "closed"))
}

// timeOrNull returns a JSON time when set is true, and null otherwise.
func timeOrNull(set bool) string {
	if set {
		return `"2026-11-03T10:00:00Z"`
	}

	return "null"
}

// expectBase makes the fake GitLab answer the read of the base branch.
func (b *bed) expectBase() {
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/main"), reply(branchBody(testBaseSHA)))
}

// expectFile makes the fake GitLab serve a file of the base branch.
func (b *bed) expectFile(path, text string) {
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+path), reply(fileBody(path, text)))
}

// expectNoFile makes the fake GitLab say a path has no file, and its parent
// directory (which the action lists to be sure the path is no directory) have
// the entries.
func (b *bed) expectNoFile(path string, entries ...string) {
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+path), notFound)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/tree"), reply("["+strings.Join(entries, ",")+"]"))
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

// handleMergeRequests makes the fake GitLab answer the list of merge requests
// by the source branch it asks for; a branch that is not in lists has none.
func (b *bed) handleMergeRequests(lists map[string]string) {
	b.gitlab.HandleFunc(http.MethodGet, apiPath("merge_requests"), func(w http.ResponseWriter, r *http.Request) {
		body, ok := lists[r.URL.Query().Get("source_branch")]
		if !ok {
			body = "[]"
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// validatorPasses makes the fake validator accept n requests.
func (b *bed) validatorPasses(n int) {
	answers := make([]fakehttp.Response, n)
	for i := range answers {
		answers[i] = reply(`{"ok":true,"errors":[]}`)
	}

	b.validator.Handle(http.MethodPost, "/validate", answers...)
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

// rulesParams returns parameters that change the rules file.
func rulesParams() Params {
	p := validParams()
	p.Files = []gitchange.FileChange{{Path: testRulesPath, Content: text(testRulesAfter)}}

	return p
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

// bodyOf returns the JSON body of the first request to method and path.
func bodyOf(t *testing.T, s *fakehttp.Server, method, path string) map[string]any {
	t.Helper()

	for _, r := range s.Requests() {
		if r.Method == method && r.Path == path {
			var out map[string]any

			require.NoError(t, json.Unmarshal(r.Body, &out))

			return out
		}
	}

	require.Failf(t, "no such request", "%s %s", method, path)

	return nil
}

// queryOf returns the query of the first request to method and path.
func queryOf(t *testing.T, s *fakehttp.Server, method, path string) url.Values {
	t.Helper()

	for _, r := range s.Requests() {
		if r.Method == method && r.Path == path {
			return r.Query
		}
	}

	require.Failf(t, "no such request", "%s %s", method, path)

	return nil
}

// validatorRequest decodes the n-th request the fake validator received.
func validatorRequest(t *testing.T, s *fakehttp.Server, n int) validateclient.Request {
	t.Helper()

	reqs := s.Requests()
	require.Greater(t, len(reqs), n)

	var out validateclient.Request

	require.NoError(t, json.Unmarshal(reqs[n].Body, &out))

	return out
}

// expectCommit makes the fake GitLab accept the commit that makes the branch.
func (b *bed) expectCommit() {
	b.gitlab.Handle(http.MethodPost, apiPath("repository/commits"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"id":%q}`, testCommitSHA)))
}

// expectOpen makes the fake GitLab accept the merge request.
func (b *bed) expectOpen(iid int, draft bool) {
	b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBody(iid, "opened", draft)))
}

// keysOf returns the keys of a decoded JSON object, sorted.
func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// registrySecrets returns a resolver that serves the test token.
func registrySecrets(t *testing.T) registry.Secrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testToken).Return([]byte(testTokenValue), nil).AnyTimes()

	return s
}

// noSlashLines requires that no line of s, once carriage returns are gone as
// GitLab removes them, starts with a slash after spaces or tabs.
func noSlashLines(t *testing.T, s string) {
	t.Helper()

	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r", ""), "\n") {
		require.False(t, strings.HasPrefix(strings.TrimLeft(line, " \t"), "/"), "a line starts with a slash: %q", line)
	}
}
