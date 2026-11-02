package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const (
	testProposalID      = "0192d3a4-0000-7000-8000-0000000000f1"
	testInvestigationID = "0192d3a4-0000-7000-8000-0000000000e2"
	testKey             = "sidekick/" + testProposalID
	testPublicURL       = "https://sidekick.example.com"
	testToken           = "xoxb-secret-token" //nolint:gosec // G101: a fake token for the tests.
	testChannel         = "C0123456789"
	testThread          = "1700000000.000100"
	botUser             = "UBOT"
)

var tokenRef = registry.SecretRef{Name: "slack", Key: "botToken"}

// fakeSecrets resolves every reference to the same value.
type fakeSecrets struct{ value string }

func (f fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(f.value + "\n"), nil
}

// fakeMessage is a message of the fake thread.
type fakeMessage struct {
	TS       string         `json:"ts"`
	User     string         `json:"user"`
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// fakeSlack is a small Slack that keeps one thread and its reactions.
type fakeSlack struct {
	mu sync.Mutex

	thread    []fakeMessage
	reactions map[string]bool
	posts     []postedMessage
	pageSize  int

	// errorCode makes every method answer {"ok": false, "error": code}.
	errorCode string
	// status, when set, answers every method with it and no body.
	status int
	// noPermalink makes chat.getPermalink fail.
	noPermalink bool

	calls         map[string]int
	metadataReads int
}

func newFakeSlack() *fakeSlack {
	return &fakeSlack{
		thread:    []fakeMessage{{TS: testThread, User: "UALERT", Text: "alert"}},
		reactions: map[string]bool{},
		calls:     map[string]int{},
		pageSize:  threadPageSize,
	}
}

// serve returns the handler of one method.
func (f *fakeSlack) serve(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		f.calls[method]++

		if f.status != 0 {
			w.WriteHeader(f.status)

			return
		}

		if f.errorCode != "" {
			write(w, map[string]any{"ok": false, "error": f.errorCode})

			return
		}

		switch method {
		case methodAuthTest:
			write(w, map[string]any{"ok": true, "user_id": botUser})
		case methodReplies:
			f.replies(w, r)
		case methodPostMessage:
			f.post(w, r)
		case methodGetPermalink:
			if f.noPermalink {
				write(w, map[string]any{"ok": false, "error": "message_not_found"})

				return
			}

			write(w, map[string]any{"ok": true, "permalink": "https://acme.slack.com/archives/" + r.URL.Query().Get("channel") + "/p" + r.URL.Query().Get("message_ts")})
		case methodReactionAdd:
			f.react(w, r)
		}
	}
}

func (f *fakeSlack) replies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	withMeta := q.Get("include_all_metadata") == "true"

	var matching []fakeMessage

	for _, m := range f.thread {
		if q.Get("oldest") != "" && m.TS != q.Get("oldest") {
			continue
		}

		if !withMeta {
			m.Metadata = nil
		}

		matching = append(matching, m)
	}

	if withMeta {
		f.metadataReads++
	}

	start := 0
	if c := q.Get("cursor"); c != "" {
		start, _ = strconv.Atoi(c)
	}

	end := min(start+f.pageSize, len(matching))
	next := ""

	if end < len(matching) {
		next = strconv.Itoa(end)
	}

	write(w, map[string]any{
		"ok": true, "messages": matching[start:end], "has_more": next != "",
		"response_metadata": map[string]any{"next_cursor": next},
	})
}

func (f *fakeSlack) post(w http.ResponseWriter, r *http.Request) {
	var m postedMessage
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	f.posts = append(f.posts, m)

	ts := "1700000100." + strconv.Itoa(100000+len(f.posts))
	msg := fakeMessage{TS: ts, User: botUser, Text: m.Text}

	if m.Metadata != nil {
		msg.Metadata = map[string]any{"event_type": m.Metadata.EventType, "event_payload": m.Metadata.EventPayload}
	}

	f.thread = append(f.thread, msg)

	write(w, map[string]any{"ok": true, "ts": ts})
}

func (f *fakeSlack) react(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	key := in["channel"] + "/" + in["timestamp"] + "/" + in["name"]
	if f.reactions[key] {
		write(w, map[string]any{"ok": false, "error": "already_reacted"})

		return
	}

	f.reactions[key] = true

	write(w, map[string]any{"ok": true})
}

func write(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(body)
}

// newServer starts the fake and returns it with its server.
func newServer(t *testing.T) (*fakeSlack, *fakehttp.Server) {
	t.Helper()

	f := newFakeSlack()
	api := fakehttp.New(t)

	for _, m := range []string{methodAuthTest, methodReplies, methodPostMessage, methodGetPermalink, methodReactionAdd} {
		verb := http.MethodPost
		if m == methodReplies || m == methodGetPermalink {
			verb = http.MethodGet
		}

		api.HandleFunc(verb, "/api/"+m, f.serve(m))
	}

	return f, api
}

func testDeps(api *fakehttp.Server) registry.Deps {
	return registry.Deps{Secrets: fakeSecrets{value: testToken}, HTTP: api.Client(), SlackBotToken: tokenRef, PublicURL: testPublicURL}
}

func connection(api *fakehttp.Server) Connection {
	return Connection{APIURL: api.URL() + "/api/"}
}

// targetsAt returns Targets that find the thread and the alert at the
// given channel and timestamp.
func targetsAt(t *testing.T, channel string) *MockTargets {
	t.Helper()

	m := NewMockTargets(gomock.NewController(t))
	m.EXPECT().Thread(gomock.Any(), testInvestigationID).Return(Target{Channel: channel, TS: testThread}, nil).AnyTimes()
	m.EXPECT().Alert(gomock.Any(), testInvestigationID).Return(Target{Channel: channel, TS: testThread}, nil).AnyTimes()

	return m
}

// newReply builds a slack.reply action that talks to api and finds its
// thread through targets.
func newReply(t *testing.T, api *fakehttp.Server, targets Targets, channels ...string) action.Action {
	t.Helper()

	a, err := NewReply(t.Context(), "reply-in-thread", ReplyConfig{Connection: connection(api), Limits: ReplyLimits{Channels: channels}}, testDeps(api))
	require.NoError(t, err)

	a.(*Reply).targets = targets

	return a
}

// newReaction builds a slack.reaction action.
func newReaction(t *testing.T, api *fakehttp.Server, targets Targets, emoji []string, channels ...string) action.Action {
	t.Helper()

	a, err := NewReaction(t.Context(), "react", ReactionConfig{Connection: connection(api), Limits: ReactionLimits{Channels: channels, Emoji: emoji}}, testDeps(api))
	require.NoError(t, err)

	a.(*Reaction).targets = targets

	return a
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
