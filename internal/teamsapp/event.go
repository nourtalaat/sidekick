package teamsapp

import (
	"encoding/json"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/listen"
)

// messageSenderType is the fingerprint's sender type for messages that no
// extract pattern gave an alert name or labels.
const messageSenderType = "teams"

// The label names of a fingerprint built from a message's place.
const (
	fingerprintChannel = "channel"
	fingerprintMessage = "message"
)

// eventKey returns the key of the event for message id in channel. A
// message ID is unique only inside its channel.
func eventKey(channel, id string) string {
	return channel + "/" + id
}

// buildEvent returns the event for a, whose text is text, with key. With a
// listener, its extract patterns fill the alert name, service, severity and
// labels, and its recovery pattern can make the event a recovery; see
// listen.Rules.Apply. Without extracted fields the fingerprint is the
// message's own channel and ID, so unrelated messages never share one. raw
// is the activity as the Bot Framework sent it.
func buildEvent(key string, a activity, text string, l *listener, raw json.RawMessage) event.Event {
	channel := a.channel()

	ev := event.Event{
		Key:      key,
		Kind:     event.KindMessage,
		Title:    listen.Title(text),
		Text:     text,
		Severity: event.SeverityUnknown,
		Teams: &event.TeamsRef{
			TenantID:   a.tenant(),
			TeamID:     a.team(),
			ChannelID:  channel,
			MessageID:  a.ID,
			ThreadID:   a.thread(),
			UserID:     a.From.ID,
			ServiceURL: a.ServiceURL,
		},
		Raw: raw,
	}

	ev.Sender = event.Sender{Kind: event.SenderHuman, ID: a.From.AADObjectID}
	if a.isBot() {
		ev.Kind = event.KindAlert
		ev.Sender = event.Sender{Kind: event.SenderBot, ID: a.From.ID, Name: a.From.Name}
	}

	for _, l := range a.links() {
		ev.Links = append(ev.Links, event.Link{Title: l.title, URL: l.url})
	}

	ev.Fingerprint = event.Fingerprint(messageSenderType, "", map[string]string{fingerprintChannel: channel, fingerprintMessage: a.ID}, []string{fingerprintChannel, fingerprintMessage})

	if l != nil {
		l.Apply(&ev, text)
	}

	return ev
}
