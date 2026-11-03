package githubcode

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// testKey returns the RSA key the tests sign app JWTs with, made once per
// test binary because making one is slow.
var testKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, testKeyBits)
})

// secretsWithKey returns a resolver that serves the test private key.
func secretsWithKey(t *testing.T) registry.Secrets {
	t.Helper()

	key, err := testKey()
	require.NoError(t, err)

	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testApp.PrivateKeySecret).Return(pemKey, nil).AnyTimes()

	return s
}

// bed is a test action with the fake GitHub it talks to.
type bed struct {
	code   *Code
	github *fakehttp.Server
}

// newBed returns an action that talks to a fresh fake GitHub, with the limits
// of validLimits changed by mutate. The fake already answers the token
// exchange.
func newBed(t *testing.T, mutate func(l *Limits)) *bed {
	t.Helper()

	return newBedWithDB(t, mutate, nil)
}

// newBedWithDB is newBed for an action that records pull request states in
// dbtx, which may be nil.
func newBedWithDB(t *testing.T, mutate func(l *Limits), dbtx db.DBTX) *bed {
	t.Helper()

	github := fakehttp.New(t)
	github.Handle(http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", testApp.InstallationID), fakehttp.Response{
		Status:  http.StatusCreated,
		Headers: jsonHeaders,
		Body:    fmt.Appendf(nil, `{"token":"ghs_test","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)),
	})

	limits := validLimits()
	if mutate != nil {
		mutate(&limits)
	}

	code, err := newCode("code-change", Config{App: testApp, BaseURL: github.URL(), Limits: limits}, registry.Deps{
		Secrets:   secretsWithKey(t),
		HTTP:      github.Client(),
		PublicURL: "https://sidekick.example.com",
		DB:        dbtx,
	})
	require.NoError(t, err)

	return &bed{code: code, github: github}
}

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=utf-8"}

// notFound is GitHub's answer for something that does not exist.
var notFound = fakehttp.Response{Status: http.StatusNotFound, Headers: jsonHeaders, Body: []byte(`{"message":"Not Found"}`)}

// reply returns a JSON answer with the given body and status 200.
func reply(body string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(body)}
}

// replyStatus returns a JSON answer with a status.
func replyStatus(status int, body string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: jsonHeaders, Body: []byte(body)}
}

// repoPath returns the path of a route below the test repository.
func repoPath(rest string) string {
	return "/repos/" + testRepo + "/" + rest
}

// refBody is the answer to reading a reference.
func refBody(ref, sha string) string {
	return fmt.Sprintf(`{"ref":%q,"object":{"sha":%q,"type":"commit"}}`, ref, sha)
}

// pullBody is a pull request as GitHub describes it.
func pullBody(number int, draft bool) string {
	return fmt.Sprintf(`{"number":%d,"draft":%t,"state":"open","html_url":"https://github.com/%s/pull/%d","head":{"sha":%q}}`,
		number, draft, testRepo, number, testCommitSHA)
}

// file is an entry of the fake repository: a regular file, an executable, a
// symlink or a submodule.
type file struct {
	mode    string
	typ     string
	content string
}

// repoFiles are the files of a fake commit, by path.
type repoFiles map[string]file

// regular returns a regular file with the content.
func regular(content string) file {
	return file{mode: fileMode, typ: typeBlob, content: content}
}

// executable returns an executable file with the content.
func executable(content string) file {
	return file{mode: modeExecutable, typ: typeBlob, content: content}
}

// treeSHA is the SHA the fake gives the tree of a directory of a commit. The
// root is the directory "".
func treeSHA(commit, dir string) string {
	return gitchange.BlobSHA("tree\n" + commit + "\n" + dir)
}

// blobOf is the SHA of a file's blob.
func blobOf(f file) string {
	if f.typ == typeCommit {
		return gitchange.BlobSHA("commit\n" + f.content)
	}

	return gitchange.BlobSHA(f.content)
}

// serveCommit makes the fake GitHub serve a commit with the files: the commit,
// the tree of each directory and the blob of each file. A directory named in
// truncated is listed as cut short.
func (b *bed) serveCommit(commit string, files repoFiles, truncated ...string) {
	b.github.HandleFunc(http.MethodGet, repoPath("git/commits/"+commit), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, commit, treeSHA(commit, "")))
	})

	dirs := map[string][]map[string]any{"": nil}
	seen := map[string]bool{}

	for _, p := range slices.Sorted(maps.Keys(files)) {
		f := files[p]
		dir, name := path.Split(p)
		dir = strings.TrimSuffix(dir, "/")

		for d := dir; d != "" && d != "." && !seen[d]; d = path.Dir(d) {
			seen[d] = true

			parent := path.Dir(d)
			if parent == "." {
				parent = ""
			}

			dirs[parent] = append(dirs[parent], map[string]any{
				"path": path.Base(d), "mode": "040000", "type": typeTree, "sha": treeSHA(commit, d),
			})

			if _, ok := dirs[d]; !ok {
				dirs[d] = nil
			}
		}

		dirs[dir] = append(dirs[dir], map[string]any{
			"path": name, "mode": f.mode, "type": f.typ, "sha": blobOf(f), "size": len(f.content),
		})

		if f.typ == typeBlob {
			content := f.content

			b.github.HandleFunc(http.MethodGet, repoPath("git/blobs/"+blobOf(f)), func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(content))
			})
		}
	}

	for dir, entries := range dirs {
		body, err := json.Marshal(map[string]any{"sha": treeSHA(commit, dir), "tree": entries, "truncated": slices.Contains(truncated, dir)})
		if err != nil {
			panic(err)
		}

		b.github.HandleFunc(http.MethodGet, repoPath("git/trees/"+treeSHA(commit, dir)), func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, string(body))
		})
	}
}

// serveBase makes the fake GitHub serve the base branch main at the commit
// testBaseSHA with the files. Calling it again changes what the branch holds.
func (b *bed) serveBase(files repoFiles, truncated ...string) {
	b.github.HandleFunc(http.MethodGet, repoPath("git/ref/heads/main"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, refBody("refs/heads/main", testBaseSHA))
	})

	b.serveCommit(testBaseSHA, files, truncated...)
}

// expectNoBranch makes the fake GitHub say that the branch of the proposal
// does not exist yet.
func (b *bed) expectNoBranch() {
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testBranch), notFound)
}

// expectPush makes the fake GitHub accept the tree, the commit and the branch.
func (b *bed) expectPush() {
	b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"sha":%q}`, testTreeSHA)))
	b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"sha":%q}`, testCommitSHA)))
	b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusCreated, refBody("refs/heads/"+testBranch, testCommitSHA)))
}

// handlePulls makes the fake GitHub answer the list of pull requests by the
// head it asks for; a head that is not in pulls has none.
func (b *bed) handlePulls(pulls map[string]string) {
	b.github.HandleFunc(http.MethodGet, repoPath("pulls"), func(w http.ResponseWriter, r *http.Request) {
		body, ok := pulls[r.URL.Query().Get("head")]
		if !ok {
			body = "[]"
		}

		writeJSON(w, body)
	})
}

// writeJSON writes a JSON answer.
func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
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

// rawParams returns the JSON of the parameters.
func rawParams(t *testing.T, params Params) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(params)
	require.NoError(t, err)

	return raw
}

// sent returns the method and path of every request the fake received, in
// order, without the token exchange.
func sent(s *fakehttp.Server) []string {
	var out []string

	for _, r := range s.Requests() {
		if strings.HasPrefix(r.Path, "/app/installations/") {
			continue
		}

		out = append(out, r.Method+" "+r.Path)
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

// fakeNoContent is GitHub's answer to deleting a reference.
func fakeNoContent() fakehttp.Response {
	return fakehttp.Response{Status: http.StatusNoContent}
}

// isPermanent reports whether a repeat cannot help, as the job runner sees it.
func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent)
}

// isLimit reports whether err wraps action.ErrLimit.
func isLimit(err error) bool {
	return errors.Is(err, action.ErrLimit)
}
