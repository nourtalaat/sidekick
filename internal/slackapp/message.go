package slackapp

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// The Events API payload and event types the app handles.
const (
	payloadEventCallback   = "event_callback"
	payloadURLVerification = "url_verification"
	eventMessage           = "message"
	eventAppMention        = "app_mention"
)

// Message subtypes that are ordinary posts. Every other subtype, such as
// message_changed or channel_join, is not a new message.
const (
	subtypeNone      = ""
	subtypeBot       = "bot_message"
	subtypeBroadcast = "thread_broadcast"
)

// The rich text element types whose text runs on within a line.
const (
	richTextPiece = "text"
	richTextLink  = "link"
)

// maxLinks is the most links taken from one message.
const maxLinks = 10

// linkPattern matches a link in Slack's mrkdwn, <url> or <url|text>.
var linkPattern = regexp.MustCompile(`<(https?://[^|>\s]+)(?:\|([^>]*))?>`)

// envelope is an Events API payload, as Socket Mode and the Events API
// both deliver it.
type envelope struct {
	Type           string          `json:"type"`
	Challenge      string          `json:"challenge"`
	TeamID         string          `json:"team_id"`
	APIAppID       string          `json:"api_app_id"`
	EventID        string          `json:"event_id"`
	Event          json.RawMessage `json:"event"`
	Authorizations []authorization `json:"authorizations"`
}

// authorization is one installation the payload is for; its user is the
// app's bot user.
type authorization struct {
	UserID string `json:"user_id"`
	IsBot  bool   `json:"is_bot"`
}

// message is a message or app_mention event, or a message that
// conversations.replies returns.
type message struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Channel     string          `json:"channel"`
	Team        string          `json:"team"`
	User        string          `json:"user"`
	BotID       string          `json:"bot_id"`
	AppID       string          `json:"app_id"`
	Username    string          `json:"username"`
	BotProfile  *botProfile     `json:"bot_profile"`
	Text        string          `json:"text"`
	TS          string          `json:"ts"`
	ThreadTS    string          `json:"thread_ts"`
	Attachments []attachment    `json:"attachments"`
	Blocks      json.RawMessage `json:"blocks"`
}

// botProfile names the bot that posted a message.
type botProfile struct {
	ID    string `json:"id"`
	AppID string `json:"app_id"`
	Name  string `json:"name"`
}

// attachment is a legacy message attachment, which many alerting bots
// still use.
type attachment struct {
	Fallback   string            `json:"fallback"`
	Pretext    string            `json:"pretext"`
	AuthorName string            `json:"author_name"`
	Title      string            `json:"title"`
	TitleLink  string            `json:"title_link"`
	Text       string            `json:"text"`
	Fields     []attachmentField `json:"fields"`
	Footer     string            `json:"footer"`
	Blocks     json.RawMessage   `json:"blocks"`
}

// attachmentField is one field of an attachment.
type attachmentField struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// botUserID returns the app's bot user ID, or "".
func (e envelope) botUserID() string {
	for _, a := range e.Authorizations {
		if a.IsBot && a.UserID != "" {
			return a.UserID
		}
	}

	return ""
}

// isPost reports whether m is a new top-level message or a reply sent to
// the channel too, rather than an edit, a deletion or a join.
func (m message) isPost() bool {
	return slices.Contains([]string{subtypeNone, subtypeBot, subtypeBroadcast}, m.Subtype)
}

// isThreadReply reports whether m is a reply in a thread rather than the
// thread's first message.
func (m message) isThreadReply() bool {
	return m.ThreadTS != "" && m.ThreadTS != m.TS
}

// isBot reports whether a bot or an app posted m.
func (m message) isBot() bool {
	return m.BotID != "" || m.Subtype == subtypeBot
}

// fromApp reports whether m was posted by this app itself, so Sidekick
// never answers its own messages.
func (m message) fromApp(appID, botUser string) bool {
	switch {
	case appID != "" && (m.AppID == appID || (m.BotProfile != nil && m.BotProfile.AppID == appID)):
		return true
	case botUser != "" && m.User == botUser:
		return true
	default:
		return false
	}
}

// botID returns the ID of the bot that posted m.
func (m message) botID() string {
	if m.BotID != "" {
		return m.BotID
	}

	if m.BotProfile != nil {
		return m.BotProfile.ID
	}

	return ""
}

// senderName returns the bot's name, or "" for a person.
func (m message) senderName() string {
	if m.BotProfile != nil && m.BotProfile.Name != "" {
		return m.BotProfile.Name
	}

	return m.Username
}

// mentions reports whether m's text mentions the user.
func (m message) mentions(user string) bool {
	return user != "" && strings.Contains(m.Text, mentionOf(user))
}

// flatText returns the message text with the text of its attachments and
// blocks added, one part per line. Parts that repeat an earlier part are
// left out, since Slack often sends the same text as the message text and
// in its blocks. An attachment's fallback is used only when it has no
// other text.
func (m message) flatText() string {
	f := flattener{}
	f.add(m.Text)
	f.addBlocks(m.Blocks)

	for _, a := range m.Attachments {
		before := len(f.parts)

		f.add(a.Pretext)
		f.add(a.AuthorName)
		f.add(a.Title)
		f.add(a.Text)

		for _, field := range a.Fields {
			f.add(field.Title)
			f.add(field.Value)
		}

		f.add(a.Footer)
		f.addBlocks(a.Blocks)

		if len(f.parts) == before {
			f.add(a.Fallback)
		}
	}

	return strings.Join(f.parts, "\n")
}

// links returns the links in the flattened text and the attachments'
// title links, at most maxLinks, without repeats.
func (m message) links(text string) []link {
	var out []link

	add := func(url, title string) {
		if url == "" || len(out) >= maxLinks || slices.ContainsFunc(out, func(l link) bool { return l.url == url }) {
			return
		}

		out = append(out, link{url: url, title: title})
	}

	for _, a := range m.Attachments {
		add(a.TitleLink, a.Title)
	}

	for _, match := range linkPattern.FindAllStringSubmatch(text, -1) {
		add(match[1], match[2])
	}

	return out
}

// link is a URL found in a message.
type link struct {
	url   string
	title string
}

// flattener collects the text parts of a message.
type flattener struct {
	parts []string
}

// add appends s unless it is empty or already there.
func (f *flattener) add(s string) {
	s = strings.TrimSpace(s)
	if s == "" || slices.Contains(f.parts, s) {
		return
	}

	f.parts = append(f.parts, s)
}

// addBlocks appends the text in Block Kit blocks: every string under a
// "text" key at any depth, which covers section, header, context and rich
// text blocks alike. Blocks that do not decode add nothing.
func (f *flattener) addBlocks(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}

	var blocks any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}

	var section strings.Builder

	walkText(blocks, &section)
	f.add(section.String())
}

// walkText writes every string under a "text" key in v to b, visiting the
// keys of each object in name order so the result is always the same. The
// pieces of one rich text section run together, as Slack shows them; every
// other text, and the end of each list of elements, ends a line.
func walkText(v any, b *strings.Builder) {
	switch v := v.(type) {
	case map[string]any:
		if s, ok := v["text"].(string); ok {
			b.WriteString(s)

			if v["type"] != richTextPiece && v["type"] != richTextLink {
				newLine(b)
			}
		}

		for _, key := range textFirst(slices.Sorted(maps.Keys(v))) {
			if _, isText := v[key].(string); key == "text" && isText {
				continue
			}

			walkText(v[key], b)

			if key == "elements" {
				newLine(b)
			}
		}
	case []any:
		for _, child := range v {
			walkText(child, b)
		}
	}
}

// textFirst moves "text" to the front of keys, since a block's own text
// comes before its fields and elements.
func textFirst(keys []string) []string {
	if i := slices.Index(keys, "text"); i > 0 {
		keys = append([]string{"text"}, slices.Delete(keys, i, i+1)...)
	}

	return keys
}

// newLine ends the line in b, unless it is empty or already ended.
func newLine(b *strings.Builder) {
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteByte('\n')
	}
}

// mentionOf returns how Slack writes a mention of user in message text.
func mentionOf(user string) string {
	return "<@" + user + ">"
}
