package githubrevert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // the fake GitHub names commits and trees like Git does, with SHA-1.
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
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
)

// Test values.
const (
	testKeyBits    = 2048
	testRepo       = "acme/api"
	testProposalID = "0192d3a4-0000-7000-8000-0000000000a1"
	testUndoID     = "0192d3a4-0000-7000-8000-0000000000a2"
	testInvestID   = "0192d3a4-0000-7000-8000-0000000000c1"
	testTreeSHA    = "dddd000000000000000000000000000000000004"
	testCommitSHA  = "cccc000000000000000000000000000000000003"
	createdNumber  = 100
)

// testNow is the time of the tests.
var testNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// testBranch is the branch the tests expect the action to make.
var testBranch = "sidekick/" + testProposalID

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

// wfile is a file of the fake repository. An empty mode means a regular
// file; kind "commit" makes a submodule.
type wfile struct {
	content string
	mode    string
	kind    string
}

// txt is a regular file with the content.
func txt(content string) wfile {
	return wfile{content: content}
}

// wentry is an entry of a tree of the fake repository.
type wentry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

// wcommit is a commit of the fake repository.
type wcommit struct {
	sha     string
	parents []string
	tree    string
	message string
	date    time.Time
}

// wpull is a pull request of the fake repository that was merged.
type wpull struct {
	number   int
	merged   bool
	mergedAt time.Time
	base     string
	mergeSHA string
	commits  int
	headSHA  string
	title    string
	body     string
	files    []wprFile
}

// wprFile is a file of a pull request.
type wprFile struct {
	name     string
	previous string
}

// own is a pull request made by a test or by the action, from a branch.
type own struct {
	number  int
	branch  string
	state   string
	merged  bool
	headSHA string
}

// world is a fake GitHub repository, served by a fakehttp server. It holds
// commits, trees, blobs, branches and pull requests, and records what the
// action writes.
type world struct {
	gh *fakehttp.Server

	mu        sync.Mutex
	commits   map[string]*wcommit
	trees     map[string][]wentry
	blobs     map[string]string
	truncated map[string]bool
	branches  map[string]string
	owns      []*own
}

// newWorld returns an empty repository that already answers the token
// exchange.
func newWorld(t *testing.T) *world {
	t.Helper()

	w := &world{
		gh:        fakehttp.New(t),
		commits:   map[string]*wcommit{},
		trees:     map[string][]wentry{},
		blobs:     map[string]string{},
		truncated: map[string]bool{},
		branches:  map[string]string{},
	}

	w.gh.Handle(http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", testApp.InstallationID), fakehttp.Response{
		Status:  http.StatusCreated,
		Headers: jsonHeaders,
		Body:    fmt.Appendf(nil, `{"token":"ghs_test","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)),
	})

	return w
}

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=utf-8"}

// repoPath returns the path of a route below the test repository.
func repoPath(rest string) string {
	return "/repos/" + testRepo + "/" + rest
}

// commit adds a commit with the files, which are all the files it has, and
// returns its SHA.
func (w *world) commit(parents []string, message string, date time.Time, files map[string]wfile) string {
	w.mu.Lock()
	defer w.mu.Unlock()

	tree := w.buildTree(files)
	sha := hash("commit", tree, strings.Join(parents, ","), message, date.String())

	w.commits[sha] = &wcommit{sha: sha, parents: parents, tree: tree, message: message, date: date}

	return sha
}

// buildTree stores the tree of the files, and the trees below it, and
// returns its SHA. The caller holds w.mu.
func (w *world) buildTree(files map[string]wfile) string {
	dirs := map[string]map[string]wfile{}
	var entries []wentry

	for path, f := range files {
		head, rest, nested := strings.Cut(path, "/")
		if nested {
			if dirs[head] == nil {
				dirs[head] = map[string]wfile{}
			}

			dirs[head][rest] = f

			continue
		}

		entries = append(entries, w.leaf(head, f))
	}

	for name, sub := range dirs {
		entries = append(entries, wentry{Path: name, Mode: "040000", Type: "tree", SHA: w.buildTree(sub)})
	}

	slices.SortFunc(entries, func(a, b wentry) int { return strings.Compare(a.Path, b.Path) })

	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = e.Mode + " " + e.Path + " " + e.SHA
	}

	sha := hash("tree", strings.Join(lines, "\n"))
	w.trees[sha] = entries

	return sha
}

// leaf stores the blob of a file and returns its tree entry.
func (w *world) leaf(name string, f wfile) wentry {
	mode, typ := f.mode, "blob"
	if mode == "" {
		mode = modeFile
	}

	if f.kind == "commit" {
		return wentry{Path: name, Mode: modeSubmodule, Type: "commit", SHA: hash("submodule", f.content)}
	}

	sha := gitchange.BlobSHA(f.content)
	w.blobs[sha] = f.content

	return wentry{Path: name, Mode: mode, Type: typ, SHA: sha, Size: len(f.content)}
}

// hash returns a 40 character hex SHA-1 of the parts.
func hash(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00"))) //nolint:gosec // see the import.

	return hex.EncodeToString(sum[:])
}

// blobOf returns the SHA of a file's content.
func blobOf(content string) string {
	return gitchange.BlobSHA(content)
}

// setBranch points a branch at a commit.
func (w *world) setBranch(branch, sha string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.branches[branch] = sha
}

// ownPull records a pull request made from a branch, as GitHub lists it.
func (w *world) ownPull(number int, branch, state string, merged bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.owns = append(w.owns, &own{number: number, branch: branch, state: state, merged: merged, headSHA: testCommitSHA})
}

// setOwnState changes the state of the pull request made from a branch, for
// example when it is merged.
func (w *world) setOwnState(branch, state string, merged bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, o := range w.owns {
		if o.branch == branch {
			o.state, o.merged = state, merged
		}
	}
}

// serve makes the fake GitHub answer the reads of every commit, tree, blob
// and branch added so far, the lists of pull requests made from a branch, and
// the writes of the action. Call it after the repository is built.
func (w *world) serve() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for sha, c := range w.commits {
		w.gh.HandleFunc(http.MethodGet, repoPath("git/commits/"+sha), func(rw http.ResponseWriter, _ *http.Request) {
			write(rw, http.StatusOK, commitJSON(c))
		})
	}

	for sha, entries := range w.trees {
		w.gh.HandleFunc(http.MethodGet, repoPath("git/trees/"+sha), func(rw http.ResponseWriter, _ *http.Request) {
			w.mu.Lock()
			cut := w.truncated[sha]
			w.mu.Unlock()

			write(rw, http.StatusOK, map[string]any{"sha": sha, "tree": entries, "truncated": cut})
		})
	}

	for sha, content := range w.blobs {
		w.gh.HandleFunc(http.MethodGet, repoPath("git/blobs/"+sha), func(rw http.ResponseWriter, _ *http.Request) {
			_, _ = rw.Write([]byte(content))
		})
	}

	w.serveBranches()
	w.serveWrites()
}

// serveBranches answers the reads of branches and the list of pull requests
// made from a branch.
func (w *world) serveBranches() {
	for _, branch := range []string{"main", testBranch, "sidekick/" + testUndoID} {
		w.gh.HandleFunc(http.MethodGet, repoPath("git/ref/heads/"+branch), func(rw http.ResponseWriter, _ *http.Request) {
			w.mu.Lock()
			sha, ok := w.branches[branch]
			w.mu.Unlock()

			if !ok {
				write(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})

				return
			}

			write(rw, http.StatusOK, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"sha": sha, "type": "commit"}})
		})
	}

	w.gh.HandleFunc(http.MethodGet, repoPath("pulls"), func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()

		head := strings.TrimPrefix(r.URL.Query().Get("head"), "acme:")
		list := []any{}

		for _, o := range w.owns {
			if o.branch == head {
				list = append(list, ownJSON(o))
			}
		}

		write(rw, http.StatusOK, list)
	})
}

// serveWrites answers the writes of the action and keeps what they make.
func (w *world) serveWrites() {
	w.gh.HandleFunc(http.MethodPost, repoPath("git/trees"), func(rw http.ResponseWriter, _ *http.Request) {
		write(rw, http.StatusCreated, map[string]string{"sha": testTreeSHA})
	})

	w.gh.HandleFunc(http.MethodPost, repoPath("git/commits"), func(rw http.ResponseWriter, _ *http.Request) {
		write(rw, http.StatusCreated, map[string]string{"sha": testCommitSHA})
	})

	w.gh.HandleFunc(http.MethodPost, repoPath("git/refs"), func(rw http.ResponseWriter, r *http.Request) {
		var req struct{ Ref, SHA string }

		_ = json.NewDecoder(r.Body).Decode(&req)

		w.mu.Lock()
		defer w.mu.Unlock()

		branch := strings.TrimPrefix(req.Ref, "refs/heads/")
		if _, ok := w.branches[branch]; ok {
			write(rw, http.StatusUnprocessableEntity, map[string]string{"message": "Reference already exists"})

			return
		}

		w.branches[branch] = req.SHA

		write(rw, http.StatusCreated, map[string]any{"ref": req.Ref, "object": map[string]string{"sha": req.SHA}})
	})

	w.gh.HandleFunc(http.MethodPost, repoPath("pulls"), func(rw http.ResponseWriter, r *http.Request) {
		var req struct{ Head string }

		_ = json.NewDecoder(r.Body).Decode(&req)

		w.mu.Lock()
		defer w.mu.Unlock()

		created := &own{number: createdNumber, branch: req.Head, state: "open", headSHA: testCommitSHA}
		w.owns = append(w.owns, created)

		write(rw, http.StatusCreated, ownJSON(created))
	})
}

// pull adds a merged pull request and answers its reads.
func (w *world) pull(p wpull) {
	w.gh.HandleFunc(http.MethodGet, repoPath(fmt.Sprintf("pulls/%d", p.number)), func(rw http.ResponseWriter, _ *http.Request) {
		body := map[string]any{
			"number": p.number, "state": "closed", "merged": p.merged, "merge_commit_sha": p.mergeSHA, "commits": p.commits,
			"title": p.title, "body": p.body, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", testRepo, p.number),
			"base": map[string]string{"ref": p.base}, "head": map[string]string{"sha": p.headSHA},
		}

		if p.merged {
			body["merged_at"] = p.mergedAt.Format(time.RFC3339)
		}

		write(rw, http.StatusOK, body)
	})

	w.gh.HandleFunc(http.MethodGet, repoPath(fmt.Sprintf("pulls/%d/files", p.number)), func(rw http.ResponseWriter, _ *http.Request) {
		files := []map[string]string{}

		for _, f := range p.files {
			e := map[string]string{"filename": f.name, "status": "modified"}
			if f.previous != "" {
				e["previous_filename"], e["status"] = f.previous, "renamed"
			}

			files = append(files, e)
		}

		write(rw, http.StatusOK, files)
	})
}

// compare answers the comparison of base with head.
func (w *world) compare(base, head, status, mergeBase string) {
	w.gh.HandleFunc(http.MethodGet, repoPath("compare/"+base+"..."+head), func(rw http.ResponseWriter, _ *http.Request) {
		write(rw, http.StatusOK, map[string]any{"status": status, "merge_base_commit": map[string]string{"sha": mergeBase}})
	})
}

// commitJSON is a commit as the Git data API describes it.
func commitJSON(c *wcommit) map[string]any {
	parents := make([]map[string]string, len(c.parents))
	for i, p := range c.parents {
		parents[i] = map[string]string{"sha": p}
	}

	who := map[string]string{"name": "Dev", "email": "dev@example.com", "date": c.date.Format(time.RFC3339)}

	return map[string]any{
		"sha": c.sha, "message": c.message, "tree": map[string]string{"sha": c.tree}, "parents": parents,
		"author": who, "committer": who, "html_url": "https://github.com/" + testRepo + "/commit/" + c.sha,
	}
}

// ownJSON is a pull request made from a branch, as GitHub describes it.
func ownJSON(o *own) map[string]any {
	body := map[string]any{
		"number": o.number, "state": o.state, "draft": true, "head": map[string]string{"sha": o.headSHA},
		"html_url": fmt.Sprintf("https://github.com/%s/pull/%d", testRepo, o.number),
	}

	if o.merged {
		body["state"], body["merged_at"] = "closed", testNow.Format(time.RFC3339)
	}

	return body
}

// write answers with a JSON body.
func write(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)

	_ = json.NewEncoder(rw).Encode(body)
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
func bodyOf(t *testing.T, s *fakehttp.Server, method, path string) map[string]any {
	t.Helper()

	for _, r := range s.Requests() {
		if r.Method == method && r.Path == path {
			var body map[string]any

			require.NoError(t, json.Unmarshal(r.Body, &body))

			return body
		}
	}

	require.FailNow(t, "no request", "%s %s was not sent", method, path)

	return nil
}

// invocation returns the invocation of a proposal with params and snapshot.
func invocation(t *testing.T, params Params, snap *action.Snapshot) action.Invocation {
	t.Helper()

	raw, err := json.Marshal(params)
	require.NoError(t, err)

	return action.Invocation{
		ProposalID:      testProposalID,
		InvestigationID: testInvestID,
		Params:          raw,
		IdempotencyKey:  action.IdempotencyKey(testProposalID),
		ApprovedBy:      "user-1",
		Snapshot:        snap,
	}
}

// requireDraft fails the test unless the body of a pull request request makes
// a draft.
func requireDraft(t *testing.T, pull map[string]any) {
	t.Helper()

	draft, ok := pull["draft"].(bool)
	require.True(t, ok, "the request has a draft key")
	require.True(t, draft, "the pull request is a draft")
}
