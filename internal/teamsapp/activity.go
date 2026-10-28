package teamsapp

import (
	"encoding/json"
	"slices"
	"strings"
)

// The activity and conversation types the bot handles.
const (
	activityMessage  = "message"
	conversationKind = "channel"
	entityMention    = "mention"
)

// The prefixes of Teams account IDs.
const (
	// botIDPrefix starts the Teams ID of a bot: "28:" and its app ID.
	botIDPrefix = "28:"
	roleBot     = "bot"
)

// messageIDMarker joins a channel's conversation ID and the ID of a
// thread's first message in the conversation ID of a channel thread.
const messageIDMarker = ";messageid="

// The attachment content types whose text is read.
const (
	contentAdaptiveCard  = "application/vnd.microsoft.card.adaptive"
	contentHeroCard      = "application/vnd.microsoft.card.hero"
	contentThumbnailCard = "application/vnd.microsoft.card.thumbnail"
	contentO365Connector = "application/vnd.microsoft.teams.card.o365connector"
	contentHTML          = "text/html"
)

// maxLinks is the most links taken from one message.
const maxLinks = 10

// activity is a Bot Framework activity, as the Bot Framework posts it to
// the messaging endpoint, with the fields Sidekick reads.
type activity struct {
	Type         string       `json:"type"`
	ID           string       `json:"id"`
	ServiceURL   string       `json:"serviceUrl"`
	ChannelID    string       `json:"channelId"`
	From         account      `json:"from"`
	Recipient    account      `json:"recipient"`
	Conversation conversation `json:"conversation"`
	ReplyToID    string       `json:"replyToId"`
	Text         string       `json:"text"`
	Attachments  []attachment `json:"attachments"`
	Entities     []entity     `json:"entities"`
	ChannelData  channelData  `json:"channelData"`
}

// account is the sender or the recipient of an activity.
type account struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AADObjectID string `json:"aadObjectId"`
	Role        string `json:"role"`
}

// conversation is where an activity was sent.
type conversation struct {
	ID               string `json:"id"`
	ConversationType string `json:"conversationType"`
	TenantID         string `json:"tenantId"`
}

// attachment is a card or a rich text version of the message. Content is
// a JSON object for cards, a JSON string for HTML, and, in Graph
// messages, a JSON string that holds a card.
type attachment struct {
	ContentType string          `json:"contentType"`
	Content     json.RawMessage `json:"content"`
}

// entity is extra data about the message; Sidekick reads mentions.
type entity struct {
	Type      string   `json:"type"`
	Mentioned *account `json:"mentioned"`
	Text      string   `json:"text"`
}

// channelData is what Teams adds to an activity.
type channelData struct {
	Channel *idRef   `json:"channel"`
	Team    *teamRef `json:"team"`
	Tenant  *idRef   `json:"tenant"`
}

// idRef names a channel or a tenant.
type idRef struct {
	ID string `json:"id"`
}

// teamRef names a team.
type teamRef struct {
	ID         string `json:"id"`
	AADGroupID string `json:"aadGroupId"`
}

// tenants returns the tenant IDs the activity states, in channelData and
// in the conversation. Either may be missing, but not both.
func (a activity) tenants() []string {
	var out []string

	if a.ChannelData.Tenant != nil && a.ChannelData.Tenant.ID != "" {
		out = append(out, a.ChannelData.Tenant.ID)
	}

	if a.Conversation.TenantID != "" {
		out = append(out, a.Conversation.TenantID)
	}

	return out
}

// tenant returns the activity's tenant ID, or "" when it states none.
func (a activity) tenant() string {
	if t := a.tenants(); len(t) > 0 {
		return t[0]
	}

	return ""
}

// inChannel reports whether the activity was sent in a team's channel,
// rather than in a chat.
func (a activity) inChannel() bool {
	return a.Conversation.ConversationType == conversationKind && strings.HasPrefix(a.Conversation.ID, "19:")
}

// channel returns the channel's conversation ID, without the thread.
func (a activity) channel() string {
	channel, _, _ := strings.Cut(a.Conversation.ID, messageIDMarker)

	return channel
}

// thread returns the ID of the first message of the activity's thread:
// the one the conversation ID names, the one it replies to, or its own.
func (a activity) thread() string {
	if _, root, ok := strings.Cut(a.Conversation.ID, messageIDMarker); ok && root != "" {
		return root
	}

	if a.ReplyToID != "" {
		return a.ReplyToID
	}

	return a.ID
}

// isThreadReply reports whether the activity is a reply in a thread
// rather than the thread's first message.
func (a activity) isThreadReply() bool {
	return a.thread() != a.ID
}

// team returns the team's Microsoft 365 group ID, which Graph names it by,
// or "".
func (a activity) team() string {
	if a.ChannelData.Team == nil {
		return ""
	}

	return a.ChannelData.Team.AADGroupID
}

// isBot reports whether a bot or an app sent the activity.
func (a activity) isBot() bool {
	return strings.HasPrefix(a.From.ID, botIDPrefix) || strings.EqualFold(a.From.Role, roleBot)
}

// fromRecipient reports whether the bot itself sent the activity, so
// Sidekick never answers its own messages.
func (a activity) fromRecipient() bool {
	return a.From.ID != "" && strings.EqualFold(a.From.ID, a.Recipient.ID)
}

// mentionsRecipient reports whether the message mentions the bot.
func (a activity) mentionsRecipient() bool {
	return slices.ContainsFunc(a.Entities, a.isRecipientMention)
}

// isRecipientMention reports whether e is a mention of the bot.
func (a activity) isRecipientMention(e entity) bool {
	return e.Type == entityMention && e.Mentioned != nil && strings.EqualFold(e.Mentioned.ID, a.Recipient.ID)
}

// flatText returns the message text with the text of its cards added, one
// part per line, as plain text. Mentions of the bot are left out of it.
// The HTML version of the message is read only when the message has no
// text of its own, since it repeats the text.
func (a activity) flatText() string {
	text := a.Text
	for _, e := range a.Entities {
		if a.isRecipientMention(e) && e.Text != "" {
			text = strings.ReplaceAll(text, e.Text, "")
		}
	}

	f := flattener{}
	f.addHTML(text)

	for _, att := range a.Attachments {
		if att.ContentType == contentHTML && text != "" {
			continue
		}

		f.addAttachment(att)
	}

	return f.String()
}

// links returns the links of the message, from its HTML and the actions
// of its cards, at most maxLinks, without repeats.
func (a activity) links() []link {
	var out []link

	add := func(l link) {
		if l.url == "" || len(out) >= maxLinks || slices.ContainsFunc(out, func(o link) bool { return o.url == l.url }) {
			return
		}

		out = append(out, l)
	}

	for _, l := range htmlLinks(a.Text) {
		add(l)
	}

	for _, att := range a.Attachments {
		for _, l := range attachmentLinks(att) {
			add(l)
		}
	}

	return out
}

// link is a URL found in a message.
type link struct {
	url   string
	title string
}
