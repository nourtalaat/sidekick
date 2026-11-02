package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

const (
	// metadataEventType marks the messages Sidekick posts for an action. The
	// payload holds the idempotency key, so a repeat can find the message.
	metadataEventType = "sidekick_action"
	// metadataKey is the field of the payload that holds the key.
	metadataKey = "idempotency_key"
	// threadPageSize and maxThreadPages bound how much of a thread the
	// search for an earlier reply reads.
	threadPageSize = 200
	maxThreadPages = 5
	// maxOwnMessages is how many of the bot's newest messages in a thread
	// are read with their metadata.
	maxOwnMessages = 10
	// fallbackChars is how much of the text goes in the notification text.
	fallbackChars = 300
	// footerText is the line under every reply.
	footerText = "Sent by Sidekick after an approver approved it."
)

// urlEscaper keeps a URL inside Slack's <url|text> syntax.
var urlEscaper = strings.NewReplacer("&", "&amp;", "<", "%3C", ">", "%3E", "|", "%7C", " ", "%20")

var (
	errEmptyText = errors.New("text: required")
	errLongText  = errors.New("text: too long")
)

// replySchema is the JSON Schema of ReplyParams.
var replySchema = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["text"],
  "properties": {
    "text": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The reply, as plain text. Formatting, links and mentions in it are not interpreted."}
  }
}`, maxReplyChars))

// ReplyConfig is the section of one slack.reply action.
type ReplyConfig struct {
	Connection `yaml:",inline"`
	Limits     ReplyLimits `yaml:"limits"`
}

// ReplyLimits say where the action may post.
type ReplyLimits struct {
	// Channels are the IDs of the channels the action may reply in. The bot
	// must be a member of each.
	Channels []string `yaml:"channels"`
}

// Validate checks the section without secrets or network calls.
func (c *ReplyConfig) Validate() error {
	return errors.Join(append(c.validate(), validateChannels(c.Limits.Channels)...)...)
}

// ReplyParams are what the model fills in for slack.reply.
type ReplyParams struct {
	Text string `json:"text"`
}

// Reply is one configured slack.reply action.
type Reply struct {
	service
}

var _ action.Action = (*Reply)(nil)

// NewReply builds a slack.reply action. It does not call Slack.
func NewReply(_ context.Context, name string, cfg ReplyConfig, deps registry.Deps) (action.Action, error) {
	svc, err := newService(name, cfg.Connection, cfg.Limits.Channels, deps)
	if err != nil {
		return nil, err
	}

	return &Reply{service: svc}, nil
}

// Spec implements action.Action.
func (r *Reply) Spec() action.Spec {
	return action.Spec{
		Name: r.name,
		Description: "Replies in the Slack thread of this investigation. Allowed channels: " + strings.Join(r.channels, ", ") +
			". The text is posted as plain text; formatting, links and mentions in it are not interpreted.",
		ParamsSchema: replySchema,
	}
}

// Validate implements action.Action. The channel is not known until the
// action runs, because it comes from the investigation.
func (r *Reply) Validate(_ context.Context, raw json.RawMessage) error {
	_, err := r.check(raw)

	return err
}

// Execute implements action.Action. It finds the thread of the
// investigation, checks its channel against the limits, and looks in the
// thread for the reply an earlier attempt posted, by the idempotency key in
// the message's metadata, before it posts one.
func (r *Reply) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := r.check(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: validate before execute: %w", action.ErrPermanent, err)
	}

	thread, err := r.targets.Thread(ctx, inv.InvestigationID)
	if err != nil {
		return action.Result{}, fmt.Errorf("find the thread: %w", err)
	}

	if err := r.channelAllowed(thread.Channel); err != nil {
		return action.Result{}, err
	}

	ts, err := r.earlierReply(ctx, thread, inv.IdempotencyKey)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if ts == "" {
		ts, err = r.client.postMessage(ctx, r.message(thread, p.Text, inv))
		if err != nil {
			return action.Result{}, fmt.Errorf("post the reply: %w", err)
		}
	}

	return r.result(ctx, "Replied in the Slack thread of the investigation.", thread.Channel, ts), nil
}

// check decodes the parameters and checks them.
func (r *Reply) check(raw json.RawMessage) (ReplyParams, error) {
	var p ReplyParams
	if err := decodeStrict(raw, &p); err != nil {
		return ReplyParams{}, err
	}

	switch n := utf8.RuneCountInString(p.Text); {
	case strings.TrimSpace(p.Text) == "":
		return ReplyParams{}, errEmptyText
	case n > maxReplyChars:
		return ReplyParams{}, fmt.Errorf("%d characters, at most %d: %w: %w", n, maxReplyChars, action.ErrLimit, errLongText)
	}

	return p, nil
}

// message builds the reply. The text of the model goes in a plain_text
// block, which Slack never reads as markup; the notification text is
// escaped and sent with markup reading off. The line under it is ours.
func (r *Reply) message(thread Target, body string, inv action.Invocation) postedMessage {
	footer := footerText
	if link := weblink.Investigation(r.publicURL, inv.InvestigationID); link != "" {
		footer += " <" + urlEscaper.Replace(link) + "|Investigation>"
	}

	noEmoji := false

	return postedMessage{
		Channel:  thread.Channel,
		ThreadTS: thread.TS,
		Text:     escape(cutRunes(body, fallbackChars)),
		Blocks: []block{
			{Type: "section", Text: &text{Type: "plain_text", Text: body, Emoji: &noEmoji}},
			{Type: "context", Elements: []*text{{Type: "mrkdwn", Text: footer}}},
		},
		Metadata: &metadata{EventType: metadataEventType, EventPayload: map[string]any{metadataKey: inv.IdempotencyKey}},
	}
}

// earlierReply returns the timestamp of the reply with the idempotency key
// in the thread, or "" when there is none. It reads the thread without
// metadata first, to find the bot's own messages, and asks for the metadata
// of only the newest of those, one message at a time, so the pages stay
// small.
func (r *Reply) earlierReply(ctx context.Context, thread Target, key string) (string, error) {
	own, err := r.ownMessages(ctx, thread)
	if err != nil {
		return "", err
	}

	for _, ts := range own {
		page, err := r.client.replies(ctx, repliesQuery{channel: thread.Channel, thread: thread.TS, oldest: ts, latest: ts, limit: 1, metadata: true})
		if err != nil {
			return "", fmt.Errorf("read the metadata of message %s: %w", ts, err)
		}

		for _, m := range page.Messages {
			if m.TS == ts && m.Metadata != nil && m.Metadata.EventType == metadataEventType && m.Metadata.EventPayload.Key == key {
				return ts, nil
			}
		}
	}

	return "", nil
}

// ownMessages returns the timestamps of the newest messages of the bot in
// the thread, newest first.
func (r *Reply) ownMessages(ctx context.Context, thread Target) ([]string, error) {
	bot, err := r.client.botUserID(ctx)
	if err != nil {
		return nil, fmt.Errorf("find the bot user: %w", err)
	}

	var own []string

	cursor := ""

	for range maxThreadPages {
		page, err := r.client.replies(ctx, repliesQuery{channel: thread.Channel, thread: thread.TS, cursor: cursor, limit: threadPageSize})
		if err != nil {
			return nil, fmt.Errorf("read the thread: %w", err)
		}

		for _, m := range page.Messages {
			if m.User == bot && m.TS != thread.TS {
				own = append(own, m.TS)
			}
		}

		cursor = page.ResponseMetadata.NextCursor
		if !page.HasMore || cursor == "" {
			break
		}
	}

	slices.Reverse(own)

	return own[:min(len(own), maxOwnMessages)], nil
}

// result describes a message that exists. The link is left out when Slack
// cannot give one: the message is there either way.
func (s service) result(ctx context.Context, summary, channel, ts string) action.Result {
	res := action.Result{Summary: summary}

	if link, err := s.client.permalink(ctx, channel, ts); err == nil && isWebLink(link) {
		res.Links = []action.Link{{Title: "Slack message", URL: link}}
	}

	return res
}

// isWebLink reports whether raw is an absolute http or https URL.
func isWebLink(raw string) bool {
	u, err := url.Parse(raw)

	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// cutRunes returns s with at most limit characters.
func cutRunes(s string, limit int) string {
	s = strings.TrimSpace(s)

	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}

	return string(runes[:limit])
}
