package gitlabgit

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Test values.
const (
	testProject    = "acme/platform/api"
	testProposalID = "0192d3a4-0000-7000-8000-0000000000a1"
	testBaseSHA    = "aaaa000000000000000000000000000000000001"
	testHeadSHA    = "cccc000000000000000000000000000000000003"
	testWebURL     = "https://gitlab.example.com/acme/platform/api/-/merge_requests/"
)

// testBranch is the branch an action makes for the test proposal.
var testBranch = "sidekick/" + testProposalID

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// notFound is GitLab's answer for something that does not exist.
var notFound = failed(http.StatusNotFound, "404 Not Found")

// bed is a client of a fake GitLab.
type bed struct {
	gitlab *fakehttp.Server
	client *gitlabapi.Client
}

// newBed returns a client that talks to a fresh fake GitLab.
func newBed(t *testing.T) *bed {
	t.Helper()

	gitlab := fakehttp.New(t)
	token := registry.SecretRef{Name: "gitlab", Key: "token"}

	secrets := registry.NewMockSecrets(gomock.NewController(t))
	secrets.EXPECT().Resolve(gomock.Any(), token).Return([]byte("glpat-test-token\n"), nil).AnyTimes()

	client, err := gitlabapi.New(gitlabapi.Options{BaseURL: gitlab.URL(), Token: token, Secrets: secrets, HTTP: gitlab.Client()})
	require.NoError(t, err)

	return &bed{gitlab: gitlab, client: client}
}

// reply returns a JSON answer with the given body and status 200.
func reply(body string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(body)}
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

// sent returns each request the fake GitLab got as a method and the path
// below the project.
func sent(s *fakehttp.Server) []string {
	var out []string

	for _, r := range s.Requests() {
		out = append(out, r.Method+" "+strings.TrimPrefix(r.Path, "/api/v4/projects/"+testProject+"/"))
	}

	return out
}

// mergeRequestBody is a merge request as GitLab describes it.
func mergeRequestBody(iid int, state string, draft bool) string {
	merged, closed := "null", "null"

	switch state {
	case gitlabMerged:
		merged = `"2026-11-03T10:00:00Z"`
	case gitlabClosed:
		closed = `"2026-11-03T10:00:00Z"`
	}

	return fmt.Sprintf(`{"iid":%d,"state":%q,"draft":%t,"web_url":"%s%d","sha":%q,"source_branch":%q,"target_branch":"main","merged_at":%s,"closed_at":%s}`,
		iid, state, draft, testWebURL, iid, testHeadSHA, testBranch, merged, closed)
}

// branchBody is the answer to reading a branch.
func branchBody(sha string) string {
	return fmt.Sprintf(`{"name":"x","commit":{"id":%q}}`, sha)
}

// blobBody is the answer to reading a blob.
func blobBody(sha, text string) string {
	return fmt.Sprintf(`{"size":%d,"encoding":"base64","content":%q,"sha":%q}`, len(text), base64.StdEncoding.EncodeToString([]byte(text)), sha)
}

// fileBody is the answer to reading a file, with the blob ID of its content.
func fileBody(path, text string) string {
	return fmt.Sprintf(`{"file_name":%q,"file_path":%q,"size":%d,"encoding":"base64","content":%q,"blob_id":%q,"commit_id":%q}`,
		path[strings.LastIndex(path, "/")+1:], path, len(text), base64.StdEncoding.EncodeToString([]byte(text)), gitchange.BlobSHA(text), testBaseSHA)
}
