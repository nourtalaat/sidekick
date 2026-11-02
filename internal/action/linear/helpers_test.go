package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	testAPIKey          = "lin_api_secret" //nolint:gosec // G101: a fake token for the tests.
	opsTeamID           = "0192d3a4-0000-7000-8000-0000000000a1"
	engTeamID           = "0192d3a4-0000-7000-8000-0000000000a2"
)

var keyRef = registry.SecretRef{Name: "linear", Key: "apiKey"}

// fakeSecrets resolves every reference to the same value.
type fakeSecrets struct{ value string }

func (f fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(f.value + "\n"), nil
}

// fakeLinear is a small Linear that keeps its issues and comments, and
// answers the GraphQL documents the client sends.
type fakeLinear struct {
	mu sync.Mutex

	teams    []teamRef
	issues   map[string]issueRef // by ID
	comments map[string]string   // comment ID to the body
	labels   []labelRef

	// descriptions and inputs record what was created.
	descriptions map[string]string
	inputs       []issueInput
	commentBodys []string

	// ignoreClientID makes issueCreate choose its own ID, as a Linear that
	// does not accept one would.
	ignoreClientID bool
	// loseAnswer makes the next create store the item and then answer with
	// an error.
	loseAnswer bool
	// errorCode makes every request fail with a GraphQL error of that code,
	// and errorMessage is its message.
	errorCode, errorMessage string
	// status, when set, answers every request with it and no body.
	status int

	creates int
	calls   int
}

func newFakeLinear() *fakeLinear {
	return &fakeLinear{
		teams:        []teamRef{{ID: opsTeamID, Key: "OPS"}, {ID: engTeamID, Key: "ENG"}},
		issues:       map[string]issueRef{},
		comments:     map[string]string{},
		descriptions: map[string]string{},
		labels: []labelRef{
			{ID: "l-incident", Name: "incident"},
			{ID: "l-ops-bug", Name: "bug", Team: &labelTeam{ID: opsTeamID}},
			{ID: "l-ws-bug", Name: "bug"},
		},
	}
}

// addIssue puts an issue in the fake.
func (f *fakeLinear) addIssue(id, identifier, teamKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.issues[id] = f.issueOf(id, identifier, teamKey)
}

func (f *fakeLinear) issueOf(id, identifier, teamKey string) issueRef {
	for _, t := range f.teams {
		if t.Key == teamKey {
			return issueRef{ID: id, Identifier: identifier, URL: "https://linear.app/acme/issue/" + identifier, Team: t}
		}
	}

	return issueRef{ID: id, Identifier: identifier}
}

// serve answers one request.
func (f *fakeLinear) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.status != 0 {
		w.WriteHeader(f.status)

		return
	}

	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	if f.errorCode != "" {
		reply(w, nil, f.errorCode, f.errorMessage)

		return
	}

	data, code, msg := f.answer(req.Query, req.Variables)
	reply(w, data, code, msg)
}

// answer returns the data for a document, or an error code and message.
func (f *fakeLinear) answer(query string, vars map[string]any) (data any, code, msg string) {
	str := func(k string) string {
		s, _ := vars[k].(string)

		return s
	}

	switch {
	case strings.Contains(query, "query SidekickIssue("):
		return f.lookupIssue(str("id"))
	case strings.Contains(query, "query SidekickComment("):
		if _, ok := f.comments[str("id")]; !ok {
			return nil, "INVALID_INPUT", "Entity not found: Comment"
		}

		return map[string]any{"comment": commentRef{ID: str("id"), URL: "https://linear.app/acme/comment/" + str("id")}}, "", ""
	case strings.Contains(query, "mutation SidekickCommentCreate"):
		return f.createComment(vars["input"].(map[string]any))
	case strings.Contains(query, "query SidekickTeam("):
		for _, t := range f.teams {
			if t.ID == str("id") {
				return map[string]any{"team": t}, "", ""
			}
		}

		return nil, "INVALID_INPUT", "Entity not found: Team"
	case strings.Contains(query, "query SidekickTeamByKey"):
		nodes := []teamRef{}

		for _, t := range f.teams {
			if t.Key == str("key") {
				nodes = append(nodes, t)
			}
		}

		return map[string]any{"teams": map[string]any{"nodes": nodes}}, "", ""
	case strings.Contains(query, "query SidekickLabels"):
		return map[string]any{"issueLabels": map[string]any{"nodes": f.labels}}, "", ""
	case strings.Contains(query, "query SidekickIssueByMarker"):
		nodes := []issueRef{}

		for id, d := range f.descriptions {
			if strings.Contains(d, str("marker")) {
				nodes = append(nodes, f.issues[id])
			}
		}

		return map[string]any{"issues": map[string]any{"nodes": nodes}}, "", ""
	case strings.Contains(query, "mutation SidekickIssueCreate"):
		return f.createIssue(vars["input"])
	default:
		return nil, "INVALID_INPUT", "unknown document"
	}
}

func (f *fakeLinear) lookupIssue(ref string) (data any, code, msg string) {
	for id, i := range f.issues {
		if id == ref || i.Identifier == ref {
			return map[string]any{"issue": i}, "", ""
		}
	}

	return nil, "INVALID_INPUT", "Entity not found: Issue"
}

func (f *fakeLinear) createComment(input map[string]any) (data any, code, msg string) {
	id, _ := input["id"].(string)
	body, _ := input["body"].(string)

	if _, ok := f.comments[id]; ok {
		return nil, "INVALID_INPUT", "comment exists"
	}

	f.creates++
	f.comments[id] = body
	f.commentBodys = append(f.commentBodys, body)

	if f.loseAnswer {
		f.loseAnswer = false

		return nil, codeInternal, "lost"
	}

	return map[string]any{"commentCreate": map[string]any{"success": true, "comment": commentRef{ID: id, URL: "https://linear.app/acme/comment/" + id}}}, "", ""
}

func (f *fakeLinear) createIssue(raw any) (data any, code, msg string) {
	var in issueInput

	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &in)

	f.creates++
	f.inputs = append(f.inputs, in)

	id := in.ID
	if f.ignoreClientID {
		id = "server-chosen-" + string(rune('a'+f.creates))
	}

	team := teamRef{}

	for _, t := range f.teams {
		if t.ID == in.TeamID {
			team = t
		}
	}

	issue := issueRef{ID: id, Identifier: team.Key + "-" + string(rune('0'+f.creates)), URL: "https://linear.app/acme/issue/new", Team: team}
	f.issues[id] = issue
	f.descriptions[id] = in.Description

	if f.loseAnswer {
		f.loseAnswer = false

		return nil, codeInternal, "lost"
	}

	return map[string]any{"issueCreate": map[string]any{"success": true, "issue": issue}}, "", ""
}

// reply writes a GraphQL response.
func reply(w http.ResponseWriter, data any, code, msg string) {
	w.Header().Set("Content-Type", "application/json")

	body := map[string]any{}
	if code != "" {
		body["errors"] = []map[string]any{{"message": msg, "extensions": map[string]any{"code": code}}}
	} else {
		body["data"] = data
	}

	_ = json.NewEncoder(w).Encode(body)
}

// newServer starts the fake and returns it with its server.
func newServer(t *testing.T) (*fakeLinear, *fakehttp.Server) {
	t.Helper()

	f := newFakeLinear()
	api := fakehttp.New(t)
	api.HandleFunc(http.MethodPost, "/graphql", f.serve)

	return f, api
}

// connection returns the connection to api.
func connection(api *fakehttp.Server) Connection {
	return Connection{APIKeySecret: keyRef, APIURL: api.URL() + "/graphql"}
}

func testDeps(api *fakehttp.Server, token string) registry.Deps {
	return registry.Deps{Secrets: fakeSecrets{value: token}, HTTP: api.Client(), PublicURL: testPublicURL}
}

// newComment builds a linear.comment action that talks to api.
func newComment(t *testing.T, api *fakehttp.Server, teams ...string) action.Action {
	t.Helper()

	a, err := NewComment(t.Context(), "ops-comment", CommentConfig{Connection: connection(api), Limits: CommentLimits{Teams: teams}}, testDeps(api, testAPIKey))
	require.NoError(t, err)

	return a
}

// newIssue builds a linear.issue action that talks to api, with the daily
// limit check given.
func newIssue(t *testing.T, api *fakehttp.Server, limits IssueLimits, limiter daily.Limiter) action.Action {
	t.Helper()

	a, err := NewIssue(t.Context(), "ops-issue", IssueConfig{Connection: connection(api), Limits: limits}, testDeps(api, testAPIKey))
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

// isLimit reports whether err wraps action.ErrLimit.
func isLimit(err error) bool {
	return errors.Is(err, action.ErrLimit)
}

// isPermanent reports whether err ends the proposal at once.
func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent) || errors.Is(err, action.ErrLimit)
}
