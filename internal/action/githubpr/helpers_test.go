package githubpr

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
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
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Test values.
const (
	testKeyBits     = 2048
	testRepo        = "acme/monitors"
	testProposalID  = "0192d3a4-0000-7000-8000-0000000000a1"
	testUndoID      = "0192d3a4-0000-7000-8000-0000000000a2"
	testInvestID    = "0192d3a4-0000-7000-8000-0000000000c1"
	testBaseSHA     = "aaaa000000000000000000000000000000000001"
	testBaseTree    = "bbbb000000000000000000000000000000000002"
	testCommitSHA   = "cccc000000000000000000000000000000000003"
	testTreeSHA     = "dddd000000000000000000000000000000000004"
	testRulesPath   = "prometheus/rules/checkout.yaml"
	testRulesBefore = "groups:\n- name: checkout\n  rules:\n  - alert: Slow\n    expr: rate(x[1m]) > 5\n    for: 1m\n"
	testRulesAfter  = "groups:\n- name: checkout\n  rules:\n  - alert: Slow\n    expr: rate(x[1m]) > 5\n    for: 10m\n"
)

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

// bed is a test action with the fake GitHub and the fake validator it talks to.
type bed struct {
	pr        *PR
	github    *fakehttp.Server
	validator *fakehttp.Server
}

// newBed returns an action that talks to fresh fake servers, with the
// limits of validLimits changed by mutate. The fake GitHub already answers
// the token exchange.
func newBed(t *testing.T, mutate func(l *Limits)) *bed {
	t.Helper()

	return newBedWithDB(t, mutate, nil)
}

// newBedWithDB is newBed for an action that records pull request states in
// dbtx, which may be nil.
func newBedWithDB(t *testing.T, mutate func(l *Limits), dbtx db.DBTX) *bed {
	t.Helper()

	github, validator := fakehttp.New(t), fakehttp.New(t)
	github.Handle(http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", testApp.InstallationID), fakehttp.Response{
		Status:  http.StatusCreated,
		Headers: jsonHeaders,
		Body:    fmt.Appendf(nil, `{"token":"ghs_test","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)),
	})

	limits := validLimits()
	if mutate != nil {
		mutate(&limits)
	}

	pr, err := newPR("monitor-pr", Config{App: testApp, BaseURL: github.URL(), Limits: limits}, registry.Deps{
		Secrets:      secretsWithKey(t),
		HTTP:         github.Client(),
		ValidatorURL: validator.URL(),
		PublicURL:    "https://sidekick.example.com",
		DB:           dbtx,
	})
	require.NoError(t, err)

	return &bed{pr: pr, github: github, validator: validator}
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

// fileBody is the answer to reading a file with the contents API.
func fileBody(path, text string) string {
	return fmt.Sprintf(`{"type":"file","name":%q,"path":%q,"sha":"blob-%s","size":%d,"encoding":"base64","content":%q}`,
		path[strings.LastIndex(path, "/")+1:], path, path, len(text), base64.StdEncoding.EncodeToString([]byte(text)))
}

// fileBodyWithSHA is fileBody for a file with a blob SHA of the test's choice.
func fileBodyWithSHA(path, text, sha string) string {
	return fmt.Sprintf(`{"type":"file","name":%q,"path":%q,"sha":%q,"size":%d,"encoding":"base64","content":%q}`,
		path[strings.LastIndex(path, "/")+1:], path, sha, len(text), base64.StdEncoding.EncodeToString([]byte(text)))
}

// handlePulls makes the fake GitHub answer the list of pull requests by the
// head it asks for; a head that is not in pulls has none.
func (b *bed) handlePulls(pulls map[string]string) {
	b.github.HandleFunc(http.MethodGet, repoPath("pulls"), func(w http.ResponseWriter, r *http.Request) {
		body, ok := pulls[r.URL.Query().Get("head")]
		if !ok {
			body = "[]"
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// listing is the answer to listing a directory; each entry is name and size.
func listing(dir string, files map[string]string) string {
	var entries []string

	for name, text := range files {
		full := dir + "/" + name
		if dir == "." {
			full = name
		}

		entries = append(entries, fmt.Sprintf(`{"type":"file","name":%q,"path":%q,"sha":"blob-%s","size":%d}`, name, full, full, len(text)))
	}

	return "[" + strings.Join(entries, ",") + "]"
}

// expectBase makes the fake GitHub answer the reads of the base branch.
func (b *bed) expectBase() {
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/main"), reply(refBody("refs/heads/main", testBaseSHA)))
	b.github.Handle(http.MethodGet, repoPath("git/commits/"+testBaseSHA), reply(fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, testBaseSHA, testBaseTree)))
}

// expectFile makes the fake GitHub serve a file of the base branch.
func (b *bed) expectFile(path, text string) {
	b.github.Handle(http.MethodGet, repoPath("contents/"+path), reply(fileBody(path, text)))
}

// expectPush makes the fake GitHub accept the tree, the commit and the branch.
func (b *bed) expectPush() {
	b.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"sha":%q}`, testTreeSHA)))
	b.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"sha":%q}`, testCommitSHA)))
	b.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusCreated, refBody("refs/heads/"+testBranch, testCommitSHA)))
}

// pullBody is a pull request as GitHub describes it.
func pullBody(number int, draft bool) string {
	return fmt.Sprintf(`{"number":%d,"draft":%t,"state":"open","html_url":"https://github.com/%s/pull/%d"}`, number, draft, testRepo, number)
}

// expectNoBranch makes the fake GitHub say that the branch of the proposal
// does not exist yet.
func (b *bed) expectNoBranch() {
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testBranch), notFound)
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
			var out map[string]any

			require.NoError(t, json.Unmarshal(r.Body, &out))

			return out
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
