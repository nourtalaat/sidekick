package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/actionhttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// maxAnswerBytes is the most of an answer the client reads.
const maxAnswerBytes = 1 << 20

// The Web API methods the actions call.
const (
	methodAuthTest     = "auth.test"
	methodPostMessage  = "chat.postMessage"
	methodGetPermalink = "chat.getPermalink"
	methodReplies      = "conversations.replies"
	methodReactionAdd  = "reactions.add"
)

// Slack's error codes for a call that may pass when repeated. Any other code
// ends the proposal. A code that is not shaped like a code is treated as
// the second kind, so a server cannot put text of its own into an error.
const (
	codeRateLimited  = "ratelimited"
	codeInternal     = "internal_error"
	codeUnavailable  = "service_unavailable"
	codeTimeout      = "request_timeout"
	codeFatal        = "fatal_error"
	codeAlreadyReact = "already_reacted"
	codeOther        = "other"
)

// codePattern is what an error code looks like.
var codePattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// errBadAnswer means Slack answered with something this client cannot read.
var errBadAnswer = errors.New("the answer cannot be read")

// slackError is a response with "ok": false. It holds only the error code.
type slackError struct {
	Method string
	Code   string
}

// Error gives Slack's error code and the method that failed.
func (e *slackError) Error() string {
	return e.Method + ": Slack answered " + e.Code
}

// client calls the Slack Web API with the bot token.
type client struct {
	apiURL  string
	http    *http.Client
	secrets registry.Secrets
	token   registry.SecretRef
}

// envelope is the part every Web API response has.
type envelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// message is a message of a thread, as conversations.replies returns it.
type message struct {
	TS       string `json:"ts"`
	User     string `json:"user"`
	Metadata *struct {
		EventType    string `json:"event_type"`
		EventPayload struct {
			Key string `json:"idempotency_key"`
		} `json:"event_payload"`
	} `json:"metadata"`
}

// repliesPage is one page of conversations.replies.
type repliesPage struct {
	Messages         []message `json:"messages"`
	HasMore          bool      `json:"has_more"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// repliesQuery asks for messages of a thread.
type repliesQuery struct {
	channel, thread, cursor string
	// oldest and latest, when set, limit the page to that range, inclusive.
	oldest, latest string
	limit          int
	metadata       bool
}

// block is a Block Kit block of a posted message.
type block struct {
	Type     string  `json:"type"`
	Text     *text   `json:"text,omitempty"`
	Elements []*text `json:"elements,omitempty"`
}

// text is a Block Kit text object.
type text struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}

// metadata is the event metadata that marks a message Sidekick posted.
type metadata struct {
	EventType    string         `json:"event_type"`
	EventPayload map[string]any `json:"event_payload"`
}

// postedMessage is the body of chat.postMessage. Names in the text are not
// turned into mentions, links are not expanded, and the fallback text is not
// read as markup.
type postedMessage struct {
	Channel        string    `json:"channel"`
	ThreadTS       string    `json:"thread_ts"`
	Text           string    `json:"text"`
	Blocks         []block   `json:"blocks"`
	Metadata       *metadata `json:"metadata"`
	Mrkdwn         bool      `json:"mrkdwn"`
	LinkNames      bool      `json:"link_names"`
	UnfurlLinks    bool      `json:"unfurl_links"`
	UnfurlMedia    bool      `json:"unfurl_media"`
	ReplyBroadcast bool      `json:"reply_broadcast"`
}

// botUserID asks Slack who the bot token belongs to.
func (c *client) botUserID(ctx context.Context) (string, error) {
	var out struct {
		UserID string `json:"user_id"`
	}

	if err := c.post(ctx, methodAuthTest, struct{}{}, &out); err != nil {
		return "", fmt.Errorf("ask who the bot is: %w", err)
	}

	if out.UserID == "" {
		return "", fmt.Errorf("%s: %w", methodAuthTest, errBadAnswer)
	}

	return out.UserID, nil
}

// replies reads a page of messages of a thread.
func (c *client) replies(ctx context.Context, q repliesQuery) (repliesPage, error) {
	query := url.Values{"channel": {q.channel}, "ts": {q.thread}, "limit": {strconv.Itoa(q.limit)}}
	if q.cursor != "" {
		query.Set("cursor", q.cursor)
	}

	if q.oldest != "" {
		query.Set("oldest", q.oldest)
		query.Set("latest", q.latest)
		query.Set("inclusive", "true")
	}

	if q.metadata {
		query.Set("include_all_metadata", "true")
	}

	var out repliesPage
	if err := c.get(ctx, methodReplies, query, &out); err != nil {
		return repliesPage{}, fmt.Errorf("read the replies: %w", err)
	}

	return out, nil
}

// postMessage posts m and returns the timestamp of the new message.
func (c *client) postMessage(ctx context.Context, m postedMessage) (string, error) {
	var out struct {
		TS string `json:"ts"`
	}

	if err := c.post(ctx, methodPostMessage, m, &out); err != nil {
		return "", fmt.Errorf("post the message: %w", err)
	}

	if out.TS == "" {
		return "", fmt.Errorf("%s: %w", methodPostMessage, errBadAnswer)
	}

	return out.TS, nil
}

// permalink returns a link to a message.
func (c *client) permalink(ctx context.Context, channel, ts string) (string, error) {
	var out struct {
		Permalink string `json:"permalink"`
	}

	query := url.Values{"channel": {channel}, "message_ts": {ts}}
	if err := c.get(ctx, methodGetPermalink, query, &out); err != nil {
		return "", fmt.Errorf("get the permalink: %w", err)
	}

	return out.Permalink, nil
}

// addReaction adds a reaction to a message. A reaction that is there
// already counts as added.
func (c *client) addReaction(ctx context.Context, channel, ts, name string) error {
	body := map[string]string{"channel": channel, "timestamp": ts, "name": name}

	err := c.post(ctx, methodReactionAdd, body, nil)
	if err == nil {
		return nil
	}

	if se, ok := errors.AsType[*slackError](err); ok && se.Code == codeAlreadyReact {
		return nil
	}

	return fmt.Errorf("add the reaction: %w", err)
}

// post sends a method with a JSON body.
func (c *client) post(ctx context.Context, method string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encode the request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+method, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%s: make the request: %w", method, err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	return c.send(ctx, method, req, out)
}

// get sends a method with a query.
func (c *client) get(ctx context.Context, method string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+method+"?"+query.Encode(), http.NoBody)
	if err != nil {
		return fmt.Errorf("%s: make the request: %w", method, err)
	}

	return c.send(ctx, method, req, out)
}

// send adds the bot token, sends the request and decodes the answer into
// out when out is set.
func (c *client) send(ctx context.Context, method string, req *http.Request, out any) error {
	token, err := secrets.ResolveText(ctx, c.secrets, c.token)
	if err != nil {
		return fmt.Errorf("%s: read the bot token: %w", method, err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := actionhttp.Do(c.http, req, maxAnswerBytes)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}

	if statusErr := resp.Err(); statusErr != nil {
		return fmt.Errorf("%s: %w", method, statusErr)
	}

	var env envelope
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return fmt.Errorf("%s: %w: %w: %w", method, action.ErrPermanent, errBadAnswer, err)
	}

	if !env.OK {
		return errorOf(method, env.Error)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%s: %w: %w: %w", method, action.ErrPermanent, errBadAnswer, err)
	}

	return nil
}

// errorOf turns the error code of a response into an error. A code that
// means a wait or a failure on Slack's side can pass when repeated; any
// other ends the proposal.
func errorOf(method, code string) error {
	if !codePattern.MatchString(code) {
		code = codeOther
	}

	se := &slackError{Method: method, Code: code}

	switch code {
	case codeRateLimited, codeInternal, codeUnavailable, codeTimeout, codeFatal:
		return se
	default:
		return fmt.Errorf("%w: %w", action.ErrPermanent, se)
	}
}
