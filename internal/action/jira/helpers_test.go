package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/daily"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const (
	testProposalID      = "0192d3a4-0000-7000-8000-0000000000f1"
	testInvestigationID = "0192d3a4-0000-7000-8000-0000000000e2"
	testKey             = "sidekick/" + testProposalID
	testPublicURL       = "https://sidekick.example.com"
	testEmail           = "sidekick@example.com"
	testToken           = "api-token-secret" //nolint:gosec // G101: a fake token for the tests.
	apiBase             = "/rest/api/3"
	footerText          = "Posted by Sidekick after an approver approved it."
)

var tokenRef = registry.SecretRef{Name: "jira", Key: "apiToken"}

// fakeSecrets resolves every reference to the same value.
type fakeSecrets struct{ value string }

func (f fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(f.value + "\n"), nil
}

// fakeIssue is an issue of the fake.
type fakeIssue struct {
	ID, Key, Project string
	Labels           []string
	Fields           json.RawMessage
	Comments         []fakeComment
}

// fakeComment is a comment of the fake: its ID and the body as it was sent.
type fakeComment struct {
	ID   string          `json:"id"`
	Body json.RawMessage `json:"body"`
}

// fakeJira is a small Jira Cloud that keeps issues and comments.
type fakeJira struct {
	mu sync.Mutex

	issues []*fakeIssue
	nextID int

	// loseAnswer makes the next create store the item and then answer 502.
	loseAnswer bool
	// status, when set, answers every request with it and no body.
	status int

	calls   int
	creates int
}

func newFakeJira() *fakeJira {
	return &fakeJira{nextID: 10000}
}

// addIssue puts an issue in the fake.
func (f *fakeJira) addIssue(key, project string) *fakeIssue {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nextID++
	i := &fakeIssue{ID: strconv.Itoa(f.nextID), Key: key, Project: project}
	f.issues = append(f.issues, i)

	return i
}

// start registers the routes for the issues the fake has so far, and
// the ones that do not depend on an issue, on api.
func (f *fakeJira) start(api *fakehttp.Server) {
	api.HandleFunc(http.MethodPost, apiBase+"/issue", f.handle(f.createIssue))
	api.HandleFunc(http.MethodGet, apiBase+"/search/jql", f.handle(f.search))

	for _, i := range f.issues {
		api.HandleFunc(http.MethodGet, apiBase+"/issue/"+i.Key, f.handle(f.getIssue(i)))
		api.HandleFunc(http.MethodGet, apiBase+"/issue/"+i.ID, f.handle(f.getIssue(i)))
		api.HandleFunc(http.MethodGet, apiBase+"/issue/"+i.ID+"/comment", f.handle(f.listComments(i)))
		api.HandleFunc(http.MethodPost, apiBase+"/issue/"+i.ID+"/comment", f.handle(f.addComment(i)))
	}
}

// handle wraps a handler with the counting and the forced status.
func (f *fakeJira) handle(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		f.calls++

		if f.status != 0 {
			w.WriteHeader(f.status)

			return
		}

		h(w, r)
	}
}

func (f *fakeJira) getIssue(i *fakeIssue) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, map[string]any{"id": i.ID, "key": i.Key, "fields": map[string]any{"project": map[string]any{"key": i.Project}}})
	}
}

func (f *fakeJira) listComments(i *fakeIssue) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		comments := make([]fakeComment, 0, len(i.Comments))
		for n := len(i.Comments) - 1; n >= 0; n-- {
			comments = append(comments, i.Comments[n])
		}

		reply(w, http.StatusOK, map[string]any{"comments": comments})
	}
}

func (f *fakeJira) addComment(i *fakeIssue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Body json.RawMessage `json:"body"`
		}

		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		f.creates++

		c := fakeComment{ID: strconv.Itoa(500 + len(i.Comments)), Body: in.Body}
		i.Comments = append(i.Comments, c)

		if f.loseAnswer {
			f.loseAnswer = false
			w.WriteHeader(http.StatusBadGateway)

			return
		}

		reply(w, http.StatusCreated, map[string]any{"id": c.ID})
	}
}

var labelInJQL = regexp.MustCompile(`project = "([^"]+)" AND labels = "([^"]+)"`)

func (f *fakeJira) search(w http.ResponseWriter, r *http.Request) {
	m := labelInJQL.FindStringSubmatch(r.URL.Query().Get("jql"))
	if m == nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	found := []map[string]any{}

	for _, i := range f.issues {
		if i.Project == m[1] && containsLabel(i.Labels, m[2]) {
			found = append(found, map[string]any{"id": i.ID, "key": i.Key})
		}
	}

	reply(w, http.StatusOK, map[string]any{"issues": found})
}

func containsLabel(labels []string, label string) bool {
	for _, l := range labels {
		if l == label {
			return true
		}
	}

	return false
}

func (f *fakeJira) createIssue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fields json.RawMessage `json:"fields"`
	}

	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	var fields struct {
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
		Labels []string `json:"labels"`
	}

	if err := json.Unmarshal(in.Fields, &fields); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	f.creates++
	f.nextID++

	i := &fakeIssue{ID: strconv.Itoa(f.nextID), Key: fields.Project.Key + "-" + strconv.Itoa(f.creates), Project: fields.Project.Key, Labels: fields.Labels, Fields: in.Fields}
	f.issues = append(f.issues, i)

	if f.loseAnswer {
		f.loseAnswer = false
		w.WriteHeader(http.StatusBadGateway)

		return
	}

	reply(w, http.StatusCreated, map[string]any{"id": i.ID, "key": i.Key})
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(body)
}

// newServer starts a fake Jira with the issues given as key:project pairs.
func newServer(t *testing.T, issues ...[2]string) (*fakeJira, *fakehttp.Server) {
	t.Helper()

	f := newFakeJira()
	for _, i := range issues {
		f.addIssue(i[0], i[1])
	}

	api := fakehttp.New(t)
	f.start(api)

	return f, api
}

func connection(api *fakehttp.Server) Connection {
	return Connection{URL: api.URL(), Email: testEmail, APITokenSecret: tokenRef}
}

func testDeps(api *fakehttp.Server) registry.Deps {
	return registry.Deps{Secrets: fakeSecrets{value: testToken}, HTTP: api.Client(), PublicURL: testPublicURL}
}

// newComment builds a jira.comment action that talks to api.
func newComment(t *testing.T, api *fakehttp.Server, projects ...string) action.Action {
	t.Helper()

	a, err := NewComment(t.Context(), "ops-comment", CommentConfig{Connection: connection(api), Limits: CommentLimits{Projects: projects}}, testDeps(api))
	require.NoError(t, err)

	return a
}

// newIssue builds a jira.issue action that talks to api, with the daily
// limit check given.
func newIssue(t *testing.T, api *fakehttp.Server, limits IssueLimits, limiter daily.Limiter) action.Action {
	t.Helper()

	a, err := NewIssue(t.Context(), "ops-issue", IssueConfig{Connection: connection(api), Limits: limits}, testDeps(api))
	require.NoError(t, err)

	a.(*Issue).counter = limiter

	return a
}

// allowDaily returns a limiter that lets everything through.
func allowDaily(t *testing.T) daily.Limiter {
	t.Helper()

	l := daily.NewMockLimiter(gomock.NewController(t))
	l.EXPECT().Check(gomock.Any(), "ops-issue", testProposalID, gomock.Any()).Return(nil).AnyTimes()

	return l
}

// invocation is an approved request with the parameters given.
func invocation(params string) action.Invocation {
	return action.Invocation{
		ProposalID:      testProposalID,
		InvestigationID: testInvestigationID,
		Params:          json.RawMessage(params),
		IdempotencyKey:  testKey,
		ApprovedBy:      "u1",
	}
}

// isPermanent reports whether err ends the proposal at once.
func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent) || errors.Is(err, action.ErrLimit)
}

// bodyText returns all the text of an ADF document, joined by "|".
func bodyText(t *testing.T, raw json.RawMessage) string {
	t.Helper()

	var doc node
	require.NoError(t, json.Unmarshal(raw, &doc))

	var parts []string

	var walk func(n node)

	walk = func(n node) {
		if n.Type == "text" {
			parts = append(parts, n.Text)
		}

		for _, c := range n.Content {
			walk(c)
		}
	}

	walk(doc)

	return strings.Join(parts, "|")
}
