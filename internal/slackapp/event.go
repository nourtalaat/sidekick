package slackapp

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxTitleBytes is the longest title taken from a message's first line.
const maxTitleBytes = 200

// messageSenderType is the fingerprint's sender type for messages that no
// extract pattern gave an alert name or labels.
const messageSenderType = "slack"

// The label names of a fingerprint built from a message's place.
const (
	fingerprintChannel = "channel"
	fingerprintTS      = "ts"
)

// buildEvent returns the event for message m, whose text is text, with
// key. With a listener, its extract patterns fill the alert name, service,
// severity and labels, and its recovery pattern can make the event a
// recovery. The fingerprint is the listener's name with the alert name and
// labels, so the alert and its recovery share it; when nothing was
// extracted, it is the message's own channel and timestamp, so unrelated
// messages never share one. raw is the message as Slack sent it.
func buildEvent(key, team string, m message, text string, l *listener, raw json.RawMessage) event.Event {
	ev := event.Event{
		Key:      key,
		Kind:     event.KindMessage,
		Title:    title(text),
		Text:     text,
		Severity: event.SeverityUnknown,
		Slack: &event.SlackRef{
			TeamID:   cmp.Or(m.Team, team),
			Channel:  m.Channel,
			TS:       m.TS,
			ThreadTS: m.ThreadTS,
			UserID:   m.User,
		},
		Raw: raw,
	}

	ev.Sender = event.Sender{Kind: event.SenderHuman, ID: m.User}
	if m.isBot() {
		ev.Kind = event.KindAlert
		ev.Sender = event.Sender{Kind: event.SenderBot, ID: m.botID(), Name: m.senderName()}
	}

	for _, l := range m.links(text) {
		ev.Links = append(ev.Links, event.Link{Title: l.title, URL: l.url})
	}

	ev.Fingerprint = event.Fingerprint(messageSenderType, "", map[string]string{fingerprintChannel: m.Channel, fingerprintTS: m.TS}, []string{fingerprintChannel, fingerprintTS})

	if l == nil {
		return ev
	}

	f := l.fields(text)
	ev.AlertName, ev.Service, ev.Severity, ev.Labels = f.alert, f.service, f.severity, f.labels

	if f.alert != "" || len(f.labels) > 0 {
		ev.Fingerprint = event.Fingerprint(l.name, f.alert, f.labels, slices.Collect(maps.Keys(f.labels)))
	}

	if l.isRecovery(text) {
		ev.Kind = event.KindResolved
	}

	return ev
}

// title returns the first line of text that is not blank, cut to
// maxTitleBytes.
func title(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return source.CutText(line, maxTitleBytes)
		}
	}

	return ""
}
